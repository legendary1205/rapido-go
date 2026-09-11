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
