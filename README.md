# Rapido-Go

A VPN panel: Go + PostgreSQL + Redis + [sing-box](https://github.com/SagerNet/sing-box) on the backend, React on the dashboard. It manages users, proxy inbounds, multiple relay nodes, subscriptions in six client formats, and (optionally) a multi-panel load-balancing "Gateway" between separate installs.

**[English](#english)** | **[فارسی](#فارسی)**

---

## English

### Contents

- [What this is](#what-this-is)
- [Architecture](#architecture)
- [Quick install](#quick-install)
  - [1. Install the panel](#1-install-the-panel)
  - [2. First login](#2-first-login)
  - [3. Add and install a node](#3-add-and-install-a-node)
- [Configuration reference](#configuration-reference)
- [Development setup](#development-setup)
- [Project layout](#project-layout)
- [Subscription formats](#subscription-formats)
- [Known limitations](#known-limitations)

### What this is

Rapido-Go is a ground-up rewrite of a VPN reseller panel, replacing an older Python/FastAPI/MySQL/Xray stack. It keeps the same job - admins manage users and resold access, nodes carry real proxy traffic, subscribers get one link that works in their client app of choice - on a different, from-scratch foundation:

- **Panel**: Go, [Gin](https://github.com/gin-gonic/gin), PostgreSQL, Redis. Two roles of the same binary: a stateless `api` role (safe to run several of, behind a load balancer) and a singleton `backend` role (owns background jobs and node reporting - never run more than one).
- **Node**: a small Go binary embedding sing-box directly (not a separate Xray process it talks to over RPC) - hot-adds/removes VLESS users on a running inbound with zero connection drop, pushes its own usage/health to the panel every few seconds, and pulls its desired config on the same interval.
- **Dashboard**: React 18 + TypeScript + Vite + Tailwind, served as static files by the panel's own `api` role - no separate web server needed.

### Architecture

```
                         ┌──────────────┐
   admin browser ──────▶ │  panel (api) │──────┐
                         └──────────────┘      │
                                                ▼
   node(s) ───usage/health push──────▶ ┌──────────────┐      ┌────────────┐
   node(s) ◀──desired config pull───── │panel(backend)│◀────▶│  Postgres  │
                                        └──────────────┘      └────────────┘
                                                │                    ▲
   subscriber ──GET /sub/:token────────────────▶│                    │
                                                 ▼              ┌──────────┐
                                          ┌──────────────┐      │  Redis   │
                                          │  panel (api) │◀────▶│ (cache)  │
                                          └──────────────┘      └──────────┘
```

A node is never dialed directly by the panel; it always calls out (usage push, config pull), so nodes work fine behind NAT with only outbound connectivity.

### Quick install

Both installers target a fresh **Ubuntu/Debian** server, run as root, and build the project from source (there is no binary release yet). They are safe to re-run - a second run rebuilds and restarts the service, doubling as an upgrade path.

> **This repository is private.** `git clone https://github.com/legendary1205/rapido-go.git` on a fresh server needs credentials - either run the clone step over SSH with a deploy key already added to the server (`git@github.com:legendary1205/rapido-go.git`), or authenticate the HTTPS clone with a personal access token first (`git clone https://<token>@github.com/legendary1205/rapido-go.git`). The install scripts themselves don't handle authentication - that has to be in place before you run them.

#### 1. Install the panel

On the server that will run the panel (PostgreSQL + Redis included, via Docker Compose):

```bash
git clone https://github.com/legendary1205/rapido-go.git
cd rapido-go
sudo ./scripts/install-panel.sh
```

This installs Docker, Go, Node.js (only needed to build the dashboard), starts Postgres/Redis, applies database migrations, builds the panel binary and the dashboard, and installs two systemd services: `rapido-go-panel` (api role, port 8000) and `rapido-go-backend` (backend role, port 8001, internal only).

At the end it prints a one-time **bootstrap login** - see below.

<details>
<summary>What the script does, step by step (click to expand)</summary>

```bash
#!/usr/bin/env bash
# Rapido-Go panel installer - Ubuntu/Debian, run as root.
# Sets up Postgres+Redis (Docker Compose), builds the panel from source,
# applies migrations, builds the dashboard, and installs two systemd
# services (api role + backend role), matching the exact process shape
# this project runs everywhere else: one stateless "api" replica-shaped
# process and one "backend" singleton that owns background jobs/node
# reporting (see cmd/panel/main.go's own top-of-file doc comment).
#
# Usage:
#   sudo ./scripts/install-panel.sh
#
# Re-running is safe: Docker Compose, goose, and this script's own steps
# are all idempotent - a second run just confirms everything's already in
# place and restarts the services with whatever changed.

set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-/opt/rapido-go}"
REPO_URL="${REPO_URL:-https://github.com/legendary1205/rapido-go.git}"
GO_VERSION="1.27.0"
NODE_MAJOR="18"
DB_URL="postgres://rapido:rapido@127.0.0.1:5432/rapido?sslmode=disable"
REDIS_ADDR="127.0.0.1:6379"

log() { echo -e "\033[1;36m==>\033[0m $*"; }
die() { echo -e "\033[1;31mERROR:\033[0m $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run this script as root (sudo ./install-panel.sh)"

# --- 1. OS packages -----------------------------------------------------
log "Installing base packages (docker, git, curl, build tools)..."
apt-get update -qq
apt-get install -y -qq ca-certificates curl git gnupg build-essential >/dev/null

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  log "Installing Docker Engine + Compose plugin..."
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  . /etc/os-release
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin >/dev/null
  systemctl enable --now docker >/dev/null
fi

# --- 2. Go toolchain ------------------------------------------------------
if ! command -v go >/dev/null 2>&1 || [ "$(go env GOVERSION 2>/dev/null)" != "go${GO_VERSION}" ]; then
  log "Installing Go ${GO_VERSION}..."
  ARCH=$(dpkg --print-architecture)
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi
export PATH="/usr/local/go/bin:$PATH"

# --- 3. Node.js (for building the dashboard) -------------------------------
if ! command -v node >/dev/null 2>&1; then
  log "Installing Node.js ${NODE_MAJOR}.x..."
  curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null
  apt-get install -y -qq nodejs >/dev/null
fi

# --- 4. Fetch source --------------------------------------------------------
if [ ! -d "$INSTALL_DIR/.git" ]; then
  log "Cloning rapido-go into $INSTALL_DIR..."
  git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
else
  log "Updating existing checkout at $INSTALL_DIR..."
  git -C "$INSTALL_DIR" pull --ff-only
fi
cd "$INSTALL_DIR"

# --- 5. Postgres + Redis (Docker Compose) -----------------------------------
log "Starting Postgres + Redis..."
docker compose up -d
log "Waiting for Postgres to accept connections..."
for i in $(seq 1 30); do
  docker compose exec -T postgres pg_isready -U rapido >/dev/null 2>&1 && break
  sleep 1
  [ "$i" -eq 30 ] && die "Postgres did not become ready in time"
done

# --- 6. Migrations -----------------------------------------------------------
if ! command -v goose >/dev/null 2>&1; then
  log "Installing goose (migration tool)..."
  go install github.com/pressly/goose/v3/cmd/goose@latest
  export PATH="$(go env GOPATH)/bin:$PATH"
fi
log "Applying database migrations..."
goose -dir internal/db/migrations postgres "$DB_URL" up

# --- 7. Build the panel binary ------------------------------------------------
log "Building the panel binary..."
go build -o "$INSTALL_DIR/panel" ./cmd/panel

# --- 8. Build the dashboard ----------------------------------------------------
log "Building the dashboard (this can take a minute)..."
( cd web && npm ci --silent && npm run build --silent )

# --- 9. Bootstrap sudo admin credentials ----------------------------------------
# There is no way to create the first admin except this env-var "break-glass"
# login (internal/httpapi/admin.go's handleLogin checks it before ever
# touching the admins table) - generate one if the operator didn't set
# RAPIDO_SUDO_USERNAME/RAPIDO_SUDO_PASSWORD themselves.
SUDO_USERNAME="${RAPIDO_SUDO_USERNAME:-admin}"
if [ -z "${RAPIDO_SUDO_PASSWORD:-}" ]; then
  SUDO_PASSWORD="$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 20)"
else
  SUDO_PASSWORD="$RAPIDO_SUDO_PASSWORD"
fi

mkdir -p "$INSTALL_DIR/db_backups"

# --- 10. systemd units -----------------------------------------------------------
log "Installing systemd services..."
PUBLIC_IP="${RAPIDO_PUBLIC_IP:-$(curl -fsSL -4 ifconfig.me || echo "")}"

cat > /etc/systemd/system/rapido-go-panel.service <<EOF
[Unit]
Description=Rapido Go Panel (api role)
After=network.target docker.service

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/panel
Restart=on-failure
Environment=ROLE=api
Environment=UVICORN_HOST=0.0.0.0
Environment=UVICORN_PORT=8000
Environment=DATABASE_URL=$DB_URL
Environment=REDIS_ADDR=$REDIS_ADDR
Environment=SUDO_USERNAME=$SUDO_USERNAME
Environment=SUDO_PASSWORD=$SUDO_PASSWORD
Environment=DASHBOARD_DIR=$INSTALL_DIR/web/dist
Environment=BACKUP_DIR=$INSTALL_DIR/db_backups
Environment=PUBLIC_IP=$PUBLIC_IP

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/rapido-go-backend.service <<EOF
[Unit]
Description=Rapido Go Panel (backend role - background jobs singleton)
After=network.target docker.service

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/panel
Restart=on-failure
Environment=ROLE=backend
Environment=UVICORN_HOST=0.0.0.0
Environment=UVICORN_PORT=8001
Environment=DATABASE_URL=$DB_URL
Environment=REDIS_ADDR=$REDIS_ADDR
Environment=SUDO_USERNAME=$SUDO_USERNAME
Environment=SUDO_PASSWORD=$SUDO_PASSWORD
Environment=DASHBOARD_DIR=$INSTALL_DIR/web/dist
Environment=BACKUP_DIR=$INSTALL_DIR/db_backups
Environment=PUBLIC_IP=$PUBLIC_IP

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable rapido-go-panel rapido-go-backend
# restart, not just "enable --now": a re-run of this script rebuilds the
# binary/dashboard above, and enable --now is a no-op for a service that's
# already active - restart is what actually makes a re-run double as an
# upgrade mechanism, not just a first-install one.
systemctl restart rapido-go-panel rapido-go-backend

# --- 11. Health check -----------------------------------------------------------
log "Waiting for the panel to come up..."
for i in $(seq 1 15); do
  curl -fsS -o /dev/null http://127.0.0.1:8000/dashboard/ 2>/dev/null && break
  sleep 1
  [ "$i" -eq 15 ] && die "panel did not respond on :8000 - check: journalctl -u rapido-go-panel -n 50"
done

echo
echo "=========================================================="
echo " Rapido-Go panel installed."
echo "=========================================================="
echo " Dashboard:      http://$PUBLIC_IP:8000/dashboard/"
echo " Bootstrap login: $SUDO_USERNAME / $SUDO_PASSWORD"
echo
echo " This login is an env-var \"break-glass\" account, not a real"
echo " admin row in the database - log in with it once, create a"
echo " real sudo admin from the Admins page, then either leave the"
echo " break-glass login as an emergency fallback or clear"
echo " SUDO_PASSWORD in both systemd unit files if you don't want it."
echo "=========================================================="
```

</details>

Full script: [`scripts/install-panel.sh`](scripts/install-panel.sh).

#### 2. First login

The install script prints a **bootstrap login** (`SUDO_USERNAME`/`SUDO_PASSWORD`, set as plain systemd `Environment=` lines). This is not a database row - it's checked in-memory before the `admins` table is ever queried, specifically so there's always a way in even from a completely empty database. Use it once to:

1. Log in at `http://<server>:8000/dashboard/`.
2. Create a real sudo admin from the **Admins** page.
3. Optionally clear `SUDO_PASSWORD` in both `/etc/systemd/system/rapido-go-panel.service` and `rapido-go-backend.service` (then `systemctl daemon-reload && systemctl restart rapido-go-panel rapido-go-backend`) if you don't want the break-glass login to keep working.

#### 3. Add and install a node

1. In the dashboard, go to **Nodes → Add Node**, give it a name and address, save.
2. The one-time reveal panel shows a single **setup_blob** value (base64) - this bundles the node's certificate, private key, the panel's CA, and its report secret together. Copy it now; it is never shown again.
3. On the node server:

```bash
git clone https://github.com/legendary1205/rapido-go.git
cd rapido-go
sudo ./scripts/install-node.sh
# paste the setup_blob when prompted, then pick a listen port (default 0.0.0.0:62051)
```

or fully non-interactively:

```bash
sudo NODE_SETUP_BLOB='<paste the blob here>' ./scripts/install-node.sh
```

<details>
<summary>What the script does, step by step (click to expand)</summary>

```bash
#!/usr/bin/env bash
# Rapido-Go node installer - Ubuntu/Debian, run as root.
# Builds the node agent from source and installs it as a systemd service,
# provisioned from ONE pasted value (setup_blob) - the base64 bundle the
# panel's "Add Node" screen returns, containing this node's cert/key, the
# panel's CA, and its report secret together. No separate cert files, no
# hand-editing multiple env vars: paste the blob, pick a port, done.
#
# Usage:
#   sudo ./scripts/install-node.sh
#   (it will prompt you to paste the setup_blob)
#
# or non-interactively:
#   sudo NODE_SETUP_BLOB='<blob from the panel>' ./scripts/install-node.sh

set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-/opt/rapido-node}"
REPO_URL="${REPO_URL:-https://github.com/legendary1205/rapido-go.git}"
GO_VERSION="1.27.0"

log() { echo -e "\033[1;36m==>\033[0m $*"; }
die() { echo -e "\033[1;31mERROR:\033[0m $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run this script as root (sudo ./install-node.sh)"

# --- 1. setup_blob ------------------------------------------------------------
# A setup_blob is only shown ONCE by the panel, at node-creation time - if
# this box was already provisioned by a previous run of this script (its
# cert/key/ca already on disk) and NODE_SETUP_BLOB isn't explicitly given
# again, skip straight to rebuilding/restarting: re-pasting isn't possible
# (the operator likely doesn't have it anymore) and isn't needed either,
# since the node binary falls back to the existing on-disk files.
ALREADY_PROVISIONED=false
if [ -f /etc/rapido-node/cert.pem ] && [ -z "${NODE_SETUP_BLOB:-}" ]; then
  ALREADY_PROVISIONED=true
  log "Existing cert/key/ca found at /etc/rapido-node/ - treating this as an upgrade, not a fresh provision. Set NODE_SETUP_BLOB to force re-provisioning."
elif [ -z "${NODE_SETUP_BLOB:-}" ]; then
  if [ -t 0 ]; then
    echo "Paste the setup_blob shown when you created this node in the Rapido"
    echo "dashboard (Nodes -> Add Node -> the one-time reveal panel), then"
    echo "press Enter:"
    read -r NODE_SETUP_BLOB
  fi
  [ -n "${NODE_SETUP_BLOB:-}" ] || die "no setup_blob given and none already provisioned - nothing to provision this node with. Re-run with NODE_SETUP_BLOB='<blob>' set."
fi

if [ -t 0 ]; then
  read -rp "Listen address for this node [0.0.0.0:62051]: " LISTEN_ADDR
fi
LISTEN_ADDR="${LISTEN_ADDR:-0.0.0.0:62051}"

# --- 2. OS packages -----------------------------------------------------------
log "Installing base packages..."
apt-get update -qq
apt-get install -y -qq ca-certificates curl git build-essential >/dev/null

# --- 3. Go toolchain -----------------------------------------------------------
if ! command -v go >/dev/null 2>&1 || [ "$(go env GOVERSION 2>/dev/null)" != "go${GO_VERSION}" ]; then
  log "Installing Go ${GO_VERSION}..."
  ARCH=$(dpkg --print-architecture)
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
fi
export PATH="/usr/local/go/bin:$PATH"

# --- 4. Fetch source ------------------------------------------------------------
if [ ! -d "$INSTALL_DIR/.git" ]; then
  log "Cloning rapido-go into $INSTALL_DIR..."
  git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
else
  log "Updating existing checkout at $INSTALL_DIR..."
  git -C "$INSTALL_DIR" pull --ff-only
fi
cd "$INSTALL_DIR"

# --- 5. Build the node binary -----------------------------------------------------
log "Building the node binary..."
go build -o /usr/local/bin/rapido-node ./cmd/node

# --- 6. systemd unit ---------------------------------------------------------------
log "Installing the systemd service..."
SETUP_BLOB_LINE=""
if [ "$ALREADY_PROVISIONED" = false ]; then
  SETUP_BLOB_LINE="Environment=NODE_SETUP_BLOB=$NODE_SETUP_BLOB"
fi
cat > /etc/systemd/system/rapido-node.service <<EOF
[Unit]
Description=Rapido Go Node Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rapido-node
Restart=on-failure
Environment=NODE_LISTEN_ADDR=$LISTEN_ADDR
$SETUP_BLOB_LINE

[Install]
WantedBy=multi-user.target
EOF
# NODE_SETUP_BLOB (when present at all) is only ever needed on the very
# first boot - it writes cert/key/ca to /etc/rapido-node/ and is never read
# again - but the unit file is kept root-only regardless, same as any other
# secret-bearing unit on this box.
chmod 600 /etc/systemd/system/rapido-node.service

systemctl daemon-reload
systemctl enable rapido-node
# restart, not just "enable --now": a re-run rebuilds the binary above, and
# enable --now is a no-op for a service that's already active.
systemctl restart rapido-node

# --- 7. Health check -----------------------------------------------------------------
log "Checking the node started..."
sleep 2
if ! systemctl is-active --quiet rapido-node; then
  echo
  journalctl -u rapido-node -n 30 --no-pager
  die "rapido-node failed to start - see the log above"
fi

echo
echo "=========================================================="
echo " Rapido-Go node installed and running on $LISTEN_ADDR"
echo " Certificate/key/CA written to: /etc/rapido-node/"
echo " (override with NODE_CERT_FILE/NODE_KEY_FILE/NODE_CA_FILE"
echo " env vars in the unit file if you need a different path)"
echo " Check status any time with: systemctl status rapido-node"
echo "=========================================================="
```

</details>

Full script: [`scripts/install-node.sh`](scripts/install-node.sh).

4. Back in the dashboard, the node's status flips to **Connected** once its first push arrives (a few seconds).
5. Create an inbound (e.g. VLESS) and a host under **Hosts** - the node picks up the new config on its next poll (also a few seconds), no restart needed.

### Configuration reference

Everything is env-var driven (`internal/config/config.go`). The panel refuses to start without `DATABASE_URL`; every other variable has a working default.

| Variable | Default | Purpose |
|---|---|---|
| `ROLE` | `api` | `api` (stateless, safe to scale out) or `backend` (singleton - background jobs, node reporting; **never run more than one**). |
| `DATABASE_URL` | *(required)* | `postgres://user:pass@host:5432/dbname?sslmode=disable` |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | `127.0.0.1:6379` / *(none)* / `0` | Cache-aside layer for hot read paths. |
| `SUDO_USERNAME` / `SUDO_PASSWORD` | *(none)* | Break-glass bootstrap admin login - see [First login](#2-first-login). |
| `UVICORN_HOST` / `UVICORN_PORT` | `0.0.0.0` / `8000` | HTTP bind address (name kept from the Python-era config for continuity). |
| `ALLOWED_ORIGINS` | `*` | Comma-separated CORS allow-list. Lock this down for a real public deployment. |
| `PUBLIC_IP` | *(none)* | Feeds the `{SERVER_IP}` subscription remark placeholder. |
| `XRAY_SUBSCRIPTION_URL_PREFIX` | *(none, relative)* | Prepended to `/sub/<token>` in generated subscription URLs. |
| `DASHBOARD_DIR` | `./web/dist` | Built dashboard static files. |
| `BACKUP_DIR` / `DB_BACKUP_KEEP` | `./db_backups` / `5` | `pg_dump` output location and retention count. |
| `KIRBOT_URL` / `KIRBOT_SECRET` / `KIRBOT_LICENSE` | `http://127.0.0.1:8080` / *(none)* | Reseller-bot integration (per-admin user-count gating). |
| `TELEGRAM_API_TOKEN` / `TELEGRAM_ADMIN_ID` / `TELEGRAM_PROXY_URL` / `TELEGRAM_DEFAULT_VLESS_FLOW` / `TELEGRAM_LOGGER_CHANNEL_ID` / `TELEGRAM_LOGGER_TOPIC_ID` | *(none)* | Outgoing Telegram notifications (send-only - no interactive bot commands). |
| `WEBHOOK_ADDRESS` / `WEBHOOK_SECRET` | *(none)* | Generic outgoing webhooks. |
| `DISCORD_WEBHOOK_URL` | *(none)* | Discord notifications. |
| `LOGIN_NOTIFY_WHITE_LIST` | *(none)* | IPs that never trigger a "login succeeded" notification (a failed login always does). |
| `NOTIFY_STATUS_CHANGE` / `NOTIFY_USER_CREATED` / `NOTIFY_USER_UPDATED` / `NOTIFY_USER_DELETED` / `NOTIFY_USER_DATA_USED_RESET` / `NOTIFY_USER_SUB_REVOKED` / `NOTIFY_LOGIN` | `true` | Per-event notification gates. |

Every integration variable above is also editable live from the dashboard's **Integrations** page (stored in Postgres, overriding the env default) - the env vars are just the starting values.

**Node** (`cmd/node`, `internal/config` is separate from the panel's):

| Variable | Default | Purpose |
|---|---|---|
| `NODE_SETUP_BLOB` | *(none)* | One base64 value carrying cert+key+ca+report-secret+panel_url - see [Add and install a node](#3-add-and-install-a-node). Only needed once; ignored on later boots if the files it writes already exist. |
| `NODE_LISTEN_ADDR` | `0.0.0.0:62051` | Where the node's own control API listens. |
| `NODE_CERT_FILE` / `NODE_KEY_FILE` / `NODE_CA_FILE` | `/etc/rapido-node/{cert,key,ca}.pem` | Written automatically by `NODE_SETUP_BLOB`; only set these by hand if provisioning without a blob. |
| `PANEL_URL` / `NODE_REPORT_SECRET` | *(from the blob)* | Where and how the node reports usage/health and pulls config. |
| `NODE_REPORT_INTERVAL_SECONDS` | `10` | Push/pull cadence. |

### Development setup

```bash
# Backend
go build ./...
go vet ./...
go test ./...                    # unit tests, no DB needed
TEST_DATABASE_URL=postgres://rapido:rapido@127.0.0.1:5432/rapido_test?sslmode=disable \
TEST_REDIS_ADDR=127.0.0.1:6379 \
  go test ./... -p 1             # full suite incl. real-Postgres integration tests
                                  # -p 1: several packages share one DB and truncate
                                  # tables between tests - running them concurrently
                                  # makes them stomp on each other's fixtures.

# Dashboard
cd web
npm install
npm run dev                      # http://localhost:3000, proxies /api to :8000
npm test                         # Vitest
npm run build                    # -> web/dist, what DASHBOARD_DIR serves

# Regenerating query code after editing internal/db/queries/*.sql
sqlc generate
```

**Never point `TEST_DATABASE_URL` at your real `rapido` database.** The test suite truncates tables between tests; use a separate `rapido_test` database (`createdb rapido_test`, then `goose -dir internal/db/migrations postgres <url> up`).

### Project layout

```
cmd/panel/          panel entrypoint (api + backend roles)
cmd/node/            node agent entrypoint
internal/httpapi/    HTTP handlers, routing, the Store (DB + cache-aside)
internal/db/          sqlc queries (queries/*.sql) + generated code + goose migrations
internal/nodecore/    sing-box embedding, the hot-user-update VLESS fork
internal/subscription/ per-format link/config builders (v2ray, sing-box, Clash, Outline, ...)
internal/gatewayclient, gatewayjob/  multi-panel load-balancer ("Gateway")
internal/reviewjob/   background user status/expiry state machine
internal/hostmetrics/ CPU/mem/disk/network sampling (panel self + nodes)
internal/*settings, telegram, discord, kirbot, report/  integrations
web/                  the dashboard (React/Vite/Tailwind)
scripts/               install-panel.sh, install-node.sh
```

### Subscription formats

`GET /sub/:token` auto-detects the client from its User-Agent; `GET /sub/:token/<format>` picks explicitly. Supported: **v2ray share links**, **sing-box**, **Clash**, **Clash Meta**, **Outline** (real SIP008, every host included), **v2ray-json**.

### Known limitations

- No per-node independent configuration yet - every node in a fleet runs the identical config (matches the panel's current architecture, not a regression).
- The old system's interactive Telegram bot console (create/suspend/bulk-manage users via chat) isn't ported - only outgoing notifications are. The dashboard is the intended replacement.
- Byte-accurate traffic counting is currently VLESS-only (the protocol the hot-update fork wraps); other protocols route through unmodified sing-box.

---

## فارسی

### فهرست

- [این پروژه چیست](#این-پروژه-چیست)
- [معماری](#معماری)
- [نصب سریع](#نصب-سریع)
  - [۱. نصب پنل](#۱-نصب-پنل)
  - [۲. اولین ورود](#۲-اولین-ورود)
  - [۳. افزودن و نصب نود](#۳-افزودن-و-نصب-نود)
- [مرجع پیکربندی](#مرجع-پیکربندی)
- [راه‌اندازی محیط توسعه](#راهاندازی-محیط-توسعه)
- [ساختار پروژه](#ساختار-پروژه)
- [فرمت‌های سابسکریپشن](#فرمتهای-سابسکریپشن)
- [محدودیت‌های شناخته‌شده](#محدودیتهای-شناختهشده)

### این پروژه چیست

Rapido-Go یک بازنویسی کامل و از صفر یک پنل VPN ری‌سلری است، جایگزین یک استک قدیمی‌تر پایتون/FastAPI/MySQL/Xray. کار اصلی همون قبلیه - ادمین‌ها کاربران و دسترسی‌های واگذارشده رو مدیریت می‌کنن، نودها ترافیک واقعی پروکسی رو حمل می‌کنن، مشترک‌ها یک لینک می‌گیرن که توی هر کلاینتی که بخوان کار می‌کنه - ولی روی یک پایه‌ی کاملاً متفاوت و از نو ساخته‌شده:

- **پنل**: Go، [Gin](https://github.com/gin-gonic/gin)، PostgreSQL، Redis. یک باینری با دو نقش: نقش `api` (بدون حالت، اجرای چند نسخه‌ی هم‌زمان پشت لودبالانسر بی‌خطره) و نقش `backend` (تک‌نمونه - کارهای پس‌زمینه و گزارش‌گیری نودها رو انجام می‌ده - **هرگز بیشتر از یکی اجرا نشه**).
- **نود**: یک باینری کوچک Go که sing-box رو مستقیماً درون خودش دارد (نه یک پروسه‌ی Xray جدا که باهاش از طریق RPC حرف بزنه) - کاربران VLESS رو روی یک اینباند در حال اجرا، بدون قطع هیچ اتصالی، اضافه/حذف می‌کنه، هر چند ثانیه مصرف/سلامت خودش رو به پنل push می‌کنه، و با همون فاصله پیکربندی موردنیازش رو pull می‌کنه.
- **داشبورد**: React 18 + TypeScript + Vite + Tailwind، به‌صورت فایل استاتیک توسط همون نقش `api` پنل سرو می‌شه - نیازی به وب‌سرور جدا نیست.

### معماری

```
                         ┌──────────────┐
   مرورگر ادمین ───────▶ │  پنل (api)   │──────┐
                         └──────────────┘      │
                                                ▼
   نود(ها) ──push مصرف/سلامت─────────▶ ┌──────────────┐      ┌────────────┐
   نود(ها) ◀──pull پیکربندی───────────│پنل(backend)  │◀────▶│  Postgres  │
                                        └──────────────┘      └────────────┘
                                                │                    ▲
   مشترک ──GET /sub/:token──────────────────────▶│                    │
                                                 ▼              ┌──────────┐
                                          ┌──────────────┐      │  Redis   │
                                          │  پنل (api)   │◀────▶│ (کش)     │
                                          └──────────────┘      └──────────┘
```

پنل هیچ‌وقت مستقیماً به نود وصل نمی‌شه؛ همیشه این نود است که تماس می‌گیره (push مصرف، pull پیکربندی)، پس نودها پشت NAT هم فقط با اتصال خروجی به‌خوبی کار می‌کنن.

### نصب سریع

هر دو اسکریپت نصب برای یک سرور تازه‌ی **اوبونتو/دبیان** طراحی شدن، به‌عنوان root اجرا می‌شن، و پروژه رو از سورس می‌سازن (فعلاً باینری آماده‌ای منتشر نشده). اجرای دوباره‌شون بی‌خطره - هر بار دوباره build و restart می‌کنن، یعنی برای آپدیت هم قابل استفاده‌ان.

> **این ریپو خصوصیه.** اجرای `git clone https://github.com/legendary1205/rapido-go.git` روی یک سرور تازه به احراز هویت نیاز داره - یا clone رو از طریق SSH با یک deploy key از قبل اضافه‌شده به سرور انجام بدید (`git@github.com:legendary1205/rapido-go.git`)، یا clone روی HTTPS رو با یک personal access token احراز هویت کنید (`git clone https://<token>@github.com/legendary1205/rapido-go.git`). خود اسکریپت‌های نصب احراز هویت رو مدیریت نمی‌کنن - باید قبل از اجرای اون‌ها آماده باشه.

#### ۱. نصب پنل

روی سروری که قراره پنل روش اجرا بشه (Postgres + Redis هم از طریق Docker Compose نصب می‌شن):

```bash
git clone https://github.com/legendary1205/rapido-go.git
cd rapido-go
sudo ./scripts/install-panel.sh
```

این اسکریپت Docker، Go، Node.js (فقط برای build داشبورد لازمه)، Postgres/Redis رو نصب و اجرا می‌کنه، migration های دیتابیس رو اعمال می‌کنه، باینری پنل و داشبورد رو می‌سازه، و دو سرویس systemd نصب می‌کنه: `rapido-go-panel` (نقش api، پورت ۸۰۰۰) و `rapido-go-backend` (نقش backend، پورت ۸۰۰۱، فقط داخلی).

در پایان یک **لاگین بوت‌استرپ یک‌بار مصرف** چاپ می‌کنه - در ادامه توضیح داده شده.

محتوای کامل اسکریپت: [`scripts/install-panel.sh`](scripts/install-panel.sh) (همون فایلی که در بخش انگلیسی بالا هم به‌طور کامل نشون داده شده).

#### ۲. اولین ورود

اسکریپت نصب یک **لاگین بوت‌استرپ** چاپ می‌کنه (`SUDO_USERNAME`/`SUDO_PASSWORD`، به‌صورت خط‌های ساده‌ی `Environment=` در systemd). این یک ردیف واقعی توی دیتابیس نیست - قبل از اینکه اصلاً جدول `admins` کوئری بشه، در حافظه چک می‌شه، دقیقاً به همین دلیل که همیشه یک راه ورود وجود داشته باشه حتی از یک دیتابیس کاملاً خالی. یک‌بار ازش استفاده کنید تا:

1. توی `http://<آدرس-سرور>:8000/dashboard/` لاگین کنید.
2. از صفحه‌ی **Admins** یک ادمین سودوی واقعی بسازید.
3. اختیاری: `SUDO_PASSWORD` رو توی هر دو فایل `/etc/systemd/system/rapido-go-panel.service` و `rapido-go-backend.service` خالی کنید (بعد `systemctl daemon-reload && systemctl restart rapido-go-panel rapido-go-backend`) اگه نمی‌خواید این لاگین اضطراری همچنان فعال بمونه.

#### ۳. افزودن و نصب نود

۱. توی داشبورد، به **Nodes → Add Node** برید، یک اسم و آدرس بدید و ذخیره کنید.
۲. پنل نمایش یک‌باره یک مقدار **setup_blob** (به‌صورت base64) رو نشون می‌ده - این مقدار گواهی نود، کلید خصوصی، CA پنل، و سکرت گزارش‌دهی رو همه با هم بسته‌بندی می‌کنه. همین الان کپی‌ش کنید؛ دیگه هیچ‌وقت نشون داده نمی‌شه.
۳. روی سرور نود:

```bash
git clone https://github.com/legendary1205/rapido-go.git
cd rapido-go
sudo ./scripts/install-node.sh
# وقتی خواست، setup_blob رو پیست کنید، بعد یک پورت گوش‌دادن انتخاب کنید (پیش‌فرض 0.0.0.0:62051)
```

یا کاملاً بدون تعامل:

```bash
sudo NODE_SETUP_BLOB='<اینجا blob رو پیست کنید>' ./scripts/install-node.sh
```

محتوای کامل اسکریپت: [`scripts/install-node.sh`](scripts/install-node.sh) (همون فایلی که در بخش انگلیسی بالا هم به‌طور کامل نشون داده شده).

۴. توی داشبورد، وضعیت نود بعد از اولین push (چند ثانیه) به **Connected** تغییر می‌کنه.
۵. یک inbound بسازید (مثلاً VLESS) و یک host زیر **Hosts** - نود توی pull بعدیش (چند ثانیه‌ی دیگه) پیکربندی جدید رو می‌گیره، بدون نیاز به ری‌استارت.

### مرجع پیکربندی

همه‌چیز از طریق متغیر محیطی کنترل می‌شه (`internal/config/config.go`). پنل بدون `DATABASE_URL` اصلاً بالا نمیاد؛ هر متغیر دیگه‌ای مقدار پیش‌فرض کاری داره.

| متغیر | پیش‌فرض | کاربرد |
|---|---|---|
| `ROLE` | `api` | `api` (بدون حالت، مقیاس‌پذیر) یا `backend` (تک‌نمونه - کارهای پس‌زمینه، گزارش نودها؛ **هرگز بیشتر از یکی اجرا نشه**). |
| `DATABASE_URL` | *(الزامی)* | `postgres://user:pass@host:5432/dbname?sslmode=disable` |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | `127.0.0.1:6379` / *(هیچ)* / `0` | لایه‌ی کش برای مسیرهای پرترافیک خواندن. |
| `SUDO_USERNAME` / `SUDO_PASSWORD` | *(هیچ)* | لاگین اضطراری بوت‌استرپ - ببینید [اولین ورود](#۲-اولین-ورود). |
| `UVICORN_HOST` / `UVICORN_PORT` | `0.0.0.0` / `8000` | آدرس bind سرور HTTP (اسم از دوران پایتون باقی مونده). |
| `ALLOWED_ORIGINS` | `*` | لیست مجاز CORS با کاما جدا شده. برای دیپلوی واقعی عمومی محدودش کنید. |
| `PUBLIC_IP` | *(هیچ)* | مقدار placeholder ‏`{SERVER_IP}` توی remark سابسکریپشن رو پر می‌کنه. |
| `XRAY_SUBSCRIPTION_URL_PREFIX` | *(هیچ، نسبی)* | قبل از `/sub/<token>` توی لینک سابسکریپشن ساخته‌شده اضافه می‌شه. |
| `DASHBOARD_DIR` | `./web/dist` | فایل‌های استاتیک build شده‌ی داشبورد. |
| `BACKUP_DIR` / `DB_BACKUP_KEEP` | `./db_backups` / `5` | محل خروجی `pg_dump` و تعداد نسخه‌های نگه‌داری‌شده. |
| `KIRBOT_URL` / `KIRBOT_SECRET` / `KIRBOT_LICENSE` | `http://127.0.0.1:8080` / *(هیچ)* | اتصال به بات فروش (محدودیت تعداد کاربر هر ادمین). |
| `TELEGRAM_API_TOKEN` / `TELEGRAM_ADMIN_ID` / `TELEGRAM_PROXY_URL` / `TELEGRAM_DEFAULT_VLESS_FLOW` / `TELEGRAM_LOGGER_CHANNEL_ID` / `TELEGRAM_LOGGER_TOPIC_ID` | *(هیچ)* | نوتیفیکیشن خروجی تلگرام (فقط ارسال - بدون دستورات تعاملی بات). |
| `WEBHOOK_ADDRESS` / `WEBHOOK_SECRET` | *(هیچ)* | webhook‌های خروجی عمومی. |
| `DISCORD_WEBHOOK_URL` | *(هیچ)* | نوتیفیکیشن دیسکورد. |
| `LOGIN_NOTIFY_WHITE_LIST` | *(هیچ)* | آی‌پی‌هایی که هیچ‌وقت نوتیفیکیشن «ورود موفق» رو تریگر نمی‌کنن (ورود ناموفق همیشه تریگر می‌شه). |
| `NOTIFY_STATUS_CHANGE` / `NOTIFY_USER_CREATED` / `NOTIFY_USER_UPDATED` / `NOTIFY_USER_DELETED` / `NOTIFY_USER_DATA_USED_RESET` / `NOTIFY_USER_SUB_REVOKED` / `NOTIFY_LOGIN` | `true` | سوییچ نوتیفیکیشن به‌ازای هر رویداد. |

هر متغیر یکپارچه‌سازی بالا از صفحه‌ی **Integrations** توی داشبورد هم به‌صورت زنده قابل ویرایشه (توی Postgres ذخیره می‌شه و مقدار env رو override می‌کنه) - env varها فقط مقدار شروع هستن.

**نود** (`cmd/node`، پیکربندیش جدا از پنله):

| متغیر | پیش‌فرض | کاربرد |
|---|---|---|
| `NODE_SETUP_BLOB` | *(هیچ)* | یک مقدار base64 که گواهی+کلید+CA+سکرت گزارش+panel_url رو با هم حمل می‌کنه - ببینید [افزودن و نصب نود](#۳-افزودن-و-نصب-نود). فقط یک‌بار لازمه؛ توی بوت‌های بعدی اگه فایل‌هاش از قبل باشن نادیده گرفته می‌شه. |
| `NODE_LISTEN_ADDR` | `0.0.0.0:62051` | آدرسی که API کنترلی خود نود روی اون گوش می‌ده. |
| `NODE_CERT_FILE` / `NODE_KEY_FILE` / `NODE_CA_FILE` | `/etc/rapido-node/{cert,key,ca}.pem` | با `NODE_SETUP_BLOB` خودکار نوشته می‌شن؛ فقط اگه بدون blob دارید provision می‌کنید دستی تنظیمشون کنید. |
| `PANEL_URL` / `NODE_REPORT_SECRET` | *(از داخل blob)* | نود کجا و چطور مصرف/سلامت گزارش می‌ده و پیکربندی pull می‌کنه. |
| `NODE_REPORT_INTERVAL_SECONDS` | `10` | فاصله‌ی push/pull. |

### راه‌اندازی محیط توسعه

```bash
# بک‌اند
go build ./...
go vet ./...
go test ./...                    # تست‌های واحد، بدون نیاز به دیتابیس
TEST_DATABASE_URL=postgres://rapido:rapido@127.0.0.1:5432/rapido_test?sslmode=disable \
TEST_REDIS_ADDR=127.0.0.1:6379 \
  go test ./... -p 1             # مجموعه‌ی کامل شامل تست‌های یکپارچگی روی Postgres واقعی
                                  # -p 1: چند پکیج یک دیتابیس مشترک دارن و بین
                                  # تست‌ها جدول‌ها رو truncate می‌کنن - اجرای هم‌زمانشون
                                  # باعث خراب شدن fixture های همدیگه می‌شه.

# داشبورد
cd web
npm install
npm run dev                      # http://localhost:3000، درخواست‌های /api رو به :8000 پراکسی می‌کنه
npm test                         # Vitest
npm run build                    # -> web/dist، همون چیزی که DASHBOARD_DIR سرو می‌کنه

# بازتولید کد کوئری بعد از ویرایش internal/db/queries/*.sql
sqlc generate
```

**هیچ‌وقت `TEST_DATABASE_URL` رو به دیتابیس واقعی `rapido` اشاره ندید.** مجموعه تست بین تست‌ها جدول‌ها رو truncate می‌کنه؛ از یک دیتابیس جدا به اسم `rapido_test` استفاده کنید (`createdb rapido_test`، بعد `goose -dir internal/db/migrations postgres <url> up`).

### ساختار پروژه

```
cmd/panel/          نقطه‌ی ورود پنل (نقش‌های api و backend)
cmd/node/            نقطه‌ی ورود ایجنت نود
internal/httpapi/    هندلرهای HTTP، روتینگ، Store (دیتابیس + کش)
internal/db/          کوئری‌های sqlc (queries/*.sql) + کد تولیدشده + migration های goose
internal/nodecore/    embedding سینگ‌باکس، فورک VLESS برای آپدیت زنده‌ی کاربر
internal/subscription/ سازنده‌های لینک/کانفیگ هر فرمت (v2ray، سینگ‌باکس، Clash، Outline، ...)
internal/gatewayclient, gatewayjob/  لودبالانسر چندپنلی ("Gateway")
internal/reviewjob/   ماشین‌حالت پس‌زمینه‌ی وضعیت/انقضای کاربر
internal/hostmetrics/ نمونه‌برداری CPU/حافظه/دیسک/شبکه (خود پنل و نودها)
internal/*settings, telegram, discord, kirbot, report/  یکپارچه‌سازی‌ها
web/                  داشبورد (React/Vite/Tailwind)
scripts/               install-panel.sh, install-node.sh
```

### فرمت‌های سابسکریپشن

`GET /sub/:token` فرمت کلاینت رو از روی User-Agent خودکار تشخیص می‌ده؛ `GET /sub/:token/<format>` صریح انتخاب می‌کنه. فرمت‌های پشتیبانی‌شده: **لینک‌های اشتراکی v2ray**، **sing-box**، **Clash**، **Clash Meta**، **Outline** (SIP008 واقعی، شامل همه‌ی هاست‌ها)، **v2ray-json**.

### محدودیت‌های شناخته‌شده

- هنوز پیکربندی مستقل به‌ازای هر نود وجود نداره - همه‌ی نودهای یک فلیت دقیقاً یک پیکربندی یکسان اجرا می‌کنن (با معماری فعلی پنل هم‌خوانه، نه یک عقب‌گرد).
- کنسول تعاملی بات تلگرام سیستم قدیمی (ساخت/تعلیق/مدیریت گروهی کاربر از طریق چت) پورت نشده - فقط نوتیفیکیشن خروجی وجود داره. داشبورد جایگزین در نظر گرفته‌شده است.
- شمارش دقیق بایت ترافیک فعلاً فقط برای VLESS هست (پروتکلی که فورک آپدیت زنده دورش پیچیده شده)؛ بقیه‌ی پروتکل‌ها از طریق sing-box دست‌نخورده مسیر می‌شن.
