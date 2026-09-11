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

Docker only - no `git clone`, no shell script to inspect and run, no local build toolchain. Both the panel and the node ship as pre-built images, published automatically to GHCR (`ghcr.io`) by [`.github/workflows/docker-publish.yml`](.github/workflows/docker-publish.yml) on every push to `master`. Installing means: copy two small files onto the server, fill in a couple of values, `docker compose up -d`.

> **The images are private** (same visibility as this repository). Before the first `docker compose up`, authenticate once: `echo <a GitHub token with read:packages scope> | docker login ghcr.io -u <your-github-username> --password-stdin`.

#### 1. Install the panel

On the server that will run the panel:

1. Create a directory, e.g. `mkdir rapido-panel && cd rapido-panel`.
2. Save [`docker-compose.prod.yml`](docker-compose.prod.yml) there as `docker-compose.yml` (copy its contents from the repo - the full file is also below).
3. Save [`.env.prod.example`](.env.prod.example) there as `.env` and fill in `POSTGRES_PASSWORD` and `SUDO_PASSWORD` at minimum.
4. `docker compose up -d`

That's it - Postgres, Redis, database migrations, and both the `api` and `backend` roles come up together. The `SUDO_USERNAME`/`SUDO_PASSWORD` you set in `.env` is the **bootstrap login** - see below.

<details>
<summary><code>docker-compose.yml</code> (click to expand)</summary>

```yaml
# Full Rapido-Go panel stack, pre-built images only - no git, no local
# build, no shell script. Copy this file (and .env.prod.example as .env)
# to the server, fill in .env, then:
#
#   docker compose -f docker-compose.prod.yml --env-file .env up -d
#
# This is separate from the root docker-compose.yml on purpose: that one
# is for local development (just Postgres+Redis, so `go run ./cmd/panel`
# can run against them with your own code) - mixing pre-built panel/backend
# containers into that file would fight a locally-run dev process for the
# same ports.
#
# Images are published privately to ghcr.io by .github/workflows/
# docker-publish.yml on every push to master - `docker login ghcr.io`
# with a token that has at least `read:packages` scope before pulling,
# same as any other private GitHub Container Registry image.

services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: rapido
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-rapido}
      POSTGRES_DB: rapido
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U rapido"]
      interval: 5s
      timeout: 5s
      retries: 10

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 10

  # Applies pending migrations and exits - panel/backend wait for this to
  # finish successfully before they start, so a fresh `up -d` always comes
  # up against an up-to-date schema with no separate manual step.
  migrate:
    image: ${RAPIDO_IMAGE:-ghcr.io/legendary1205/rapido-go-panel}:${RAPIDO_TAG:-latest}
    depends_on:
      postgres:
        condition: service_healthy
    entrypoint: ["goose", "-dir", "/app/internal/db/migrations", "postgres"]
    command: ["postgres://rapido:${POSTGRES_PASSWORD:-rapido}@postgres:5432/rapido?sslmode=disable", "up"]
    restart: "no"

  panel:
    image: ${RAPIDO_IMAGE:-ghcr.io/legendary1205/rapido-go-panel}:${RAPIDO_TAG:-latest}
    restart: unless-stopped
    depends_on:
      migrate:
        condition: service_completed_successfully
      redis:
        condition: service_healthy
    ports:
      - "${PANEL_PORT:-8000}:8000"
    environment:
      ROLE: api
      DATABASE_URL: postgres://rapido:${POSTGRES_PASSWORD:-rapido}@postgres:5432/rapido?sslmode=disable
      REDIS_ADDR: redis:6379
      SUDO_USERNAME: ${SUDO_USERNAME:-admin}
      SUDO_PASSWORD: ${SUDO_PASSWORD:?set SUDO_PASSWORD in your .env file - this is the one-time bootstrap login, see the README}
      PUBLIC_IP: ${PUBLIC_IP:-}
      ALLOWED_ORIGINS: ${ALLOWED_ORIGINS:-*}

  backend:
    image: ${RAPIDO_IMAGE:-ghcr.io/legendary1205/rapido-go-panel}:${RAPIDO_TAG:-latest}
    restart: unless-stopped
    depends_on:
      migrate:
        condition: service_completed_successfully
      redis:
        condition: service_healthy
    # No published port - this role is not meant to receive traffic
    # directly, only the "api" role above is (see the README's
    # architecture section on why there are two roles at all).
    environment:
      ROLE: backend
      DATABASE_URL: postgres://rapido:${POSTGRES_PASSWORD:-rapido}@postgres:5432/rapido?sslmode=disable
      REDIS_ADDR: redis:6379
      SUDO_USERNAME: ${SUDO_USERNAME:-admin}
      SUDO_PASSWORD: ${SUDO_PASSWORD:?set SUDO_PASSWORD in your .env file - this is the one-time bootstrap login, see the README}
      PUBLIC_IP: ${PUBLIC_IP:-}

volumes:
  postgres_data:
  redis_data:
```

</details>

<details>
<summary><code>.env</code> (click to expand)</summary>

```bash
# Copy this file to .env next to docker-compose.prod.yml and fill it in,
# then: docker compose -f docker-compose.prod.yml --env-file .env up -d

# A real password for the Postgres container - the "rapido" default only
# exists for local development, change it for anything reachable from the
# internet.
POSTGRES_PASSWORD=change-me

# The one-time bootstrap admin login (checked in-memory before the admins
# table is ever queried, so there is always a way in from an empty
# database - see the README's "First login" section). Log in with this
# once, create a real sudo admin from the dashboard, then consider
# rotating this value or removing it from .env afterward.
SUDO_USERNAME=admin
SUDO_PASSWORD=change-me

# This server's public IP - feeds the {SERVER_IP} subscription remark
# placeholder. Leave blank if you don't use that placeholder.
PUBLIC_IP=

# Port the dashboard/API is published on (host side).
PANEL_PORT=8000

# Comma-separated CORS allow-list. "*" is fine to start; lock this down
# for a real deployment.
ALLOWED_ORIGINS=*
```

</details>

#### 2. First login

`SUDO_USERNAME`/`SUDO_PASSWORD` from `.env` is a **bootstrap login** - not a database row, checked in-memory before the `admins` table is ever queried, specifically so there's always a way in even from a completely empty database. Use it once to:

1. Log in at `http://<server>:8000/dashboard/`.
2. Create a real sudo admin from the **Admins** page.
3. Optionally rotate `SUDO_PASSWORD` in `.env` and `docker compose up -d` again if you don't want the break-glass login to keep working with its original value.

#### 3. Add and install a node

1. In the dashboard, go to **Nodes → Add Node**, give it a name and address, save.
2. The one-time reveal panel shows a single **setup_blob** value (base64) - this bundles the node's certificate, private key, the panel's CA, and its report secret together. Copy it now; it is never shown again.
3. On the node server:
   1. Create a directory, e.g. `mkdir rapido-node && cd rapido-node`.
   2. Save [`docker-compose.node.yml`](docker-compose.node.yml) there as `docker-compose.yml`.
   3. Save [`.env.node.example`](.env.node.example) there as `.env`, paste the setup_blob into `NODE_SETUP_BLOB`.
   4. `docker compose up -d`

<details>
<summary><code>docker-compose.yml</code> (node, click to expand)</summary>

```yaml
# Rapido-Go node agent, pre-built image. Copy this file (and
# .env.node.example as .env) to the node server, fill in NODE_SETUP_BLOB
# from the panel's Add Node screen, then:
#
#   docker compose -f docker-compose.node.yml --env-file .env up -d
#
# `docker login ghcr.io` first if the image is private - see
# docker-compose.prod.yml's own top-of-file comment.

services:
  node:
    image: ${RAPIDO_NODE_IMAGE:-ghcr.io/legendary1205/rapido-go-node}:${RAPIDO_NODE_TAG:-latest}
    restart: unless-stopped
    ports:
      - "${NODE_PORT:-62051}:62051"
    environment:
      NODE_LISTEN_ADDR: 0.0.0.0:62051
      # Only needed on the very first `up` - the container writes
      # cert/key/ca into the named volume below and never reads this
      # again afterward. Required for a genuinely fresh volume (the node
      # process itself refuses to start with no blob AND no existing
      # on-disk cert - a clear error in its own logs either way); safe to
      # blank out in .env on a later `up` once the volume already has
      # certs in it.
      NODE_SETUP_BLOB: ${NODE_SETUP_BLOB:-}
    volumes:
      - node_certs:/etc/rapido-node

volumes:
  node_certs:
```

</details>

<details>
<summary><code>.env</code> (node, click to expand)</summary>

```bash
# Copy this file to .env next to docker-compose.node.yml and fill it in,
# then: docker compose -f docker-compose.node.yml --env-file .env up -d

# Paste the setup_blob shown once when you create this node in the panel's
# dashboard (Nodes -> Add Node -> the one-time reveal panel). Only needed
# for the very first `up` against a fresh volume - see docker-compose.node.yml's
# own comment.
NODE_SETUP_BLOB=

# Host port to publish the node's control API on.
NODE_PORT=62051
```

</details>

4. Back in the dashboard, the node's status flips to **Connected** once its first push arrives (a few seconds).
5. Create an inbound (e.g. VLESS) and a host under **Hosts** - the node picks up the new config on its next poll (also a few seconds), no restart needed.

#### Upgrading

Pull the newer image and recreate: `docker compose pull && docker compose up -d` (panel) or the same against `docker-compose.node.yml` (node) - the `migrate` service re-applies any new migrations automatically before `panel`/`backend` start.

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
docker/                Dockerfile.panel, Dockerfile.node
docker-compose.yml     local dev only (Postgres+Redis)
docker-compose.prod.yml, docker-compose.node.yml   production images, see Quick install
.github/workflows/     CI - builds and publishes both images to GHCR
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

فقط داکر - نه `git clone`ای، نه اسکریپتی که لازم باشه بررسی و اجراش کنید، نه ابزار build محلی. هم پنل هم نود به‌صورت ایمیج آماده منتشر می‌شن، خودکار به‌وسیله‌ی [`.github/workflows/docker-publish.yml`](.github/workflows/docker-publish.yml) روی هر push به `master` توی GHCR (`ghcr.io`) ساخته و push می‌شن. نصب یعنی: دو تا فایل کوچیک رو روی سرور کپی کنید، چند مقدار پر کنید، `docker compose up -d`.

> **ایمیج‌ها خصوصی‌ان** (همون سطح دسترسی این ریپو). قبل از اولین `docker compose up`، یک‌بار احراز هویت کنید: `echo <یک توکن گیت‌هاب با اسکوپ read:packages> | docker login ghcr.io -u <یوزرنیم گیت‌هابتون> --password-stdin`.

#### ۱. نصب پنل

روی سروری که قراره پنل روش اجرا بشه:

۱. یک پوشه بسازید، مثلاً `mkdir rapido-panel && cd rapido-panel`.
۲. محتوای [`docker-compose.prod.yml`](docker-compose.prod.yml) رو اونجا با اسم `docker-compose.yml` ذخیره کنید (محتوای کامل فایل توی بخش انگلیسی بالا هم هست).
۳. محتوای [`.env.prod.example`](.env.prod.example) رو اونجا با اسم `.env` ذخیره کنید و حداقل `POSTGRES_PASSWORD` و `SUDO_PASSWORD` رو پر کنید.
۴. `docker compose up -d`

همین. Postgres، Redis، migration های دیتابیس، و هر دو نقش `api` و `backend` با هم بالا میان. مقدار `SUDO_USERNAME`/`SUDO_PASSWORD` که توی `.env` گذاشتید، همون **لاگین بوت‌استرپ** هست - در ادامه توضیح داده شده.

#### ۲. اولین ورود

`SUDO_USERNAME`/`SUDO_PASSWORD` توی `.env` یک **لاگین بوت‌استرپ** هست - نه یک ردیف واقعی توی دیتابیس، قبل از اینکه اصلاً جدول `admins` کوئری بشه در حافظه چک می‌شه، دقیقاً به همین دلیل که همیشه یک راه ورود وجود داشته باشه حتی از یک دیتابیس کاملاً خالی. یک‌بار ازش استفاده کنید تا:

1. توی `http://<آدرس-سرور>:8000/dashboard/` لاگین کنید.
2. از صفحه‌ی **Admins** یک ادمین سودوی واقعی بسازید.
3. اختیاری: `SUDO_PASSWORD` رو توی `.env` عوض کنید و دوباره `docker compose up -d` بزنید، اگه نمی‌خواید این لاگین اضطراری با مقدار اولیه‌ش همچنان فعال بمونه.

#### ۳. افزودن و نصب نود

۱. توی داشبورد، به **Nodes → Add Node** برید، یک اسم و آدرس بدید و ذخیره کنید.
۲. پنل نمایش یک‌باره یک مقدار **setup_blob** (به‌صورت base64) رو نشون می‌ده - این مقدار گواهی نود، کلید خصوصی، CA پنل، و سکرت گزارش‌دهی رو همه با هم بسته‌بندی می‌کنه. همین الان کپی‌ش کنید؛ دیگه هیچ‌وقت نشون داده نمی‌شه.
۳. روی سرور نود:
   ۱. یک پوشه بسازید، مثلاً `mkdir rapido-node && cd rapido-node`.
   ۲. محتوای [`docker-compose.node.yml`](docker-compose.node.yml) رو اونجا با اسم `docker-compose.yml` ذخیره کنید.
   ۳. محتوای [`.env.node.example`](.env.node.example) رو اونجا با اسم `.env` ذخیره کنید، و setup_blob رو توی `NODE_SETUP_BLOB` پیست کنید.
   ۴. `docker compose up -d`

۴. توی داشبورد، وضعیت نود بعد از اولین push (چند ثانیه) به **Connected** تغییر می‌کنه.
۵. یک inbound بسازید (مثلاً VLESS) و یک host زیر **Hosts** - نود توی pull بعدیش (چند ثانیه‌ی دیگه) پیکربندی جدید رو می‌گیره، بدون نیاز به ری‌استارت.

#### آپدیت

ایمیج جدید رو pull کنید و دوباره بسازید: `docker compose pull && docker compose up -d` (پنل) یا همین دستور روی `docker-compose.node.yml` (نود) - سرویس `migrate` هر migration جدیدی رو خودکار قبل از بالا اومدن `panel`/`backend` اعمال می‌کنه.

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
docker/                Dockerfile.panel, Dockerfile.node
docker-compose.yml     فقط برای توسعه‌ی محلی (Postgres+Redis)
docker-compose.prod.yml, docker-compose.node.yml   ایمیج‌های پروداکشن، ببینید نصب سریع
.github/workflows/     CI - هر دو ایمیج رو می‌سازه و توی GHCR منتشر می‌کنه
```

### فرمت‌های سابسکریپشن

`GET /sub/:token` فرمت کلاینت رو از روی User-Agent خودکار تشخیص می‌ده؛ `GET /sub/:token/<format>` صریح انتخاب می‌کنه. فرمت‌های پشتیبانی‌شده: **لینک‌های اشتراکی v2ray**، **sing-box**، **Clash**، **Clash Meta**، **Outline** (SIP008 واقعی، شامل همه‌ی هاست‌ها)، **v2ray-json**.

### محدودیت‌های شناخته‌شده

- هنوز پیکربندی مستقل به‌ازای هر نود وجود نداره - همه‌ی نودهای یک فلیت دقیقاً یک پیکربندی یکسان اجرا می‌کنن (با معماری فعلی پنل هم‌خوانه، نه یک عقب‌گرد).
- کنسول تعاملی بات تلگرام سیستم قدیمی (ساخت/تعلیق/مدیریت گروهی کاربر از طریق چت) پورت نشده - فقط نوتیفیکیشن خروجی وجود داره. داشبورد جایگزین در نظر گرفته‌شده است.
- شمارش دقیق بایت ترافیک فعلاً فقط برای VLESS هست (پروتکلی که فورک آپدیت زنده دورش پیچیده شده)؛ بقیه‌ی پروتکل‌ها از طریق sing-box دست‌نخورده مسیر می‌شن.
