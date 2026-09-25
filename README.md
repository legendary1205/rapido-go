# Rapido-Go

A self-hosted proxy panel. It manages users and reseller admins, runs proxy inbounds across any number of relay nodes, meters per-user traffic, and hands every subscriber one subscription link that works in their client of choice.

Go + PostgreSQL + Redis on the backend, [sing-box](https://github.com/SagerNet/sing-box) embedded in the node agent, React on the dashboard.

**[English](#english)** | **[فارسی](#فارسی)**

---

## English

### What you get

- **Users and resellers** - per-user data limits, expiry, on-hold accounts, and reseller admins who only see their own customers.
- **Multiple nodes** - each node carries real traffic and reports its own usage and health. Nodes only ever dial *out*, so they work behind NAT. Every node can have its own configuration: which inbounds and ports it serves, and per-node overrides of routing, outbounds, DNS and log level.
- **Live user updates** - adding or removing a user (VLESS, VMess, Trojan or Shadowsocks) takes effect on a running inbound without dropping anyone's connection, and every byte is counted per user for all four protocols.
- **WireGuard exits that fail open** - an exit whose tunnel dies falls back to a direct connection by itself and returns when the tunnel recovers; tunnel health is probed for real and shown per tunnel in the dashboard.
- **Six subscription formats** - v2ray share links, sing-box, Clash, Clash Meta, Outline (SIP008), v2ray-json. `GET /sub/:token` picks one from the client's User-Agent; `GET /sub/:token/<format>` forces it.
- **Dashboard** - React/Vite/Tailwind, served by the panel itself; no extra web server.
- **Notifications and a Telegram console** - Telegram, Discord and generic webhooks on user and login events, plus an interactive Telegram bot for admins: system status, user search and cards with QR codes, create/extend/disable/reset/delete, node and tunnel health, and database backups.
- **Gateway (optional)** - merge several independent installs so one subscription link carries hosts from all of them.

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
   subscriber ──GET /sub/:token───────────────▶│                    │
                                               ▼              ┌──────────┐
                                        ┌──────────────┐      │  Redis   │
                                        │  panel (api) │◀────▶│ (cache)  │
                                        └──────────────┘      └──────────┘
```

The panel binary runs in one of two roles: `api` (stateless, safe to run several of) and `backend` (a singleton that owns background jobs and node reporting - never run more than one).

### Requirements

- A Linux server on x86_64 or aarch64 (Ubuntu, Debian or the RHEL family - anything with `apt-get`, `dnf` or `yum`) and root access. Docker is installed for you if it is missing.
- At least 1 GB of RAM (2 GB or more recommended) and 2 GB of free disk (5 GB or more recommended, for the database and backups).
- Ports 80 and 443 free and reachable from the internet - Caddy uses them to get and renew certificates.
- A domain whose A record points at the server.

### Install

On the fresh server, as root:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install --domain panel.example.com
```

Before it changes anything it checks the OS, CPU architecture, RAM, disk and ports 80/443, and looks up your domain's DNS. If a record does not point at this server it tells you the exact A record to add and lets you continue (Caddy keeps retrying its certificate until the record exists). Whatever you did not pass as an option is asked for - but only when a terminal is attached, so the same command also runs unattended.

| Option | Meaning |
|---|---|
| `--domain <name>` | Panel domain (asked for if omitted). |
| `--extra-domains <a,b>` | More names that serve the same dashboard. |
| `--sub-domain <name[,name]>` | A separate domain for subscription links only. |
| `--admin-user <name>`, `--admin-pass <pass>` | First admin login. Default: `admin` and a generated password, printed once at the end. |
| `--token <pat>` | GitHub token - only if the repository or its images are private. |
| `--yes` | Never ask; take the defaults. |
| `--skip-preflight` | Turn a failed server check (RAM, disk, ports, ...) into a warning. |

Each option also has an environment variable (`RAPIDO_DOMAIN`, `RAPIDO_EXTRA_DOMAINS`, `RAPIDO_SUB_DOMAIN`, `RAPIDO_ADMIN_USER`, `RAPIDO_ADMIN_PASS`, `RAPIDO_REPO_TOKEN`). Prefer those for secrets - a command-line argument is visible in the process list.

**Private repository or images.** Give the installer a GitHub token that can read them (classic token: `repo` and `read:packages`). Export it first - `VAR=x bash <(curl ... $VAR)` expands `$VAR` *before* it is set, so the download itself would go out without it - and fetch the script through the GitHub API, which accepts every kind of token:

```bash
export RAPIDO_REPO_TOKEN=<token>
bash <(curl -fsSL -H "Authorization: Bearer $RAPIDO_REPO_TOKEN" -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go.sh?ref=master") install --domain panel.example.com
```

The installer only asks for (or uses) a token when the repository or images are actually private; it never prints it, and stores it in `.env` (mode 600, for `rapido-go update`) only in that case.

**What it sets up**

- Docker and the Compose plugin, if missing.
- `/opt/rapido-go` with the compose file, `.env` (generated secrets, mode 600), the `Caddyfile`, and the `rapido-go` command in `/usr/local/bin`. A new install downloads just those files - it does not clone the repository.
- The stack: PostgreSQL 16 (memory tuned to your RAM), Redis (cache only, never written to disk), the migrations, the panel's `api` and `backend` roles, and Caddy (automatic HTTPS, gzip/zstd compression).
- `/var/lib/rapido-go` for backups.

It ends with a summary: dashboard URL(s), admin login, subscription base URL, backup location and the commands below.

### Domains

- **Primary domain** (`--domain`) - the dashboard and the admin API.
- **Extra domains** (`--extra-domains a.example.com,b.example.com`) - more names for the same dashboard. They share the primary domain's site block.
- **Subscription domain(s)** (`--sub-domain s.example.com`) - a separate block that serves only `/sub/*` (plus the fonts the subscription page uses and a health probe); the dashboard and admin API are not reachable through it. Subscription links are built on it. Without one, links use the panel domain(s).

All names are stored in `.env` (`RAPIDO_DOMAIN`, `RAPIDO_EXTRA_DOMAINS`, `RAPIDO_SUB_DOMAIN`) and written to `/opt/rapido-go/Caddyfile`; `Caddyfile.example` shows the shape. To change them later, edit the Caddyfile and `.env`, then `cd /opt/rapido-go && docker compose -p rapido-go up -d --force-recreate caddy` (certificates are kept).

### Commands

`rapido-go` on its own shows this list.

| Command | Does |
|---|---|
| `install [options]` | Install (above). |
| `update` | Pull the latest images and restart if anything changed. |
| `status` / `logs [service]` | What is running / follow the logs. |
| `up` / `down` / `restart` | Start, stop, restart (re-reads `.env`). |
| `backup [file]` / `restore <file>` | Dump / replace the database. |
| `doctor` | Read-only health check: containers, database, disk, certificate days left, the dashboard answering, installed vs. latest version. Exits 1 on a failure. |
| `edit-env` | Edit `.env`, then offers to apply it. |
| `version` | Script, source and image versions. |
| `uninstall` | Remove the containers and `/opt/rapido-go` (backups are kept). |

### Update

```bash
rapido-go update
```

It refreshes the installer files, pulls every image in parallel and stops there if nothing changed. Otherwise it backs up the database first (the last 5 backups are kept), recreates what changed - migrations run automatically - and checks the stack came back healthy. Installs made by older versions (a git checkout in `/opt/rapido-go`) update the same way.

### Backup and restore

```bash
rapido-go backup                          # /var/lib/rapido-go/backup-<date>.sql.gz
rapido-go backup /root/rapido.sql.gz      # or choose the file
rapido-go restore /root/rapido.sql.gz     # replaces the database, asks first
```

Every `update` takes a backup first. Copy backups off the server - they live on the same disk as the database.

### Uninstall

`rapido-go uninstall` removes the containers, their volumes (**including the database**) and `/opt/rapido-go`. Backups in `/var/lib/rapido-go` are kept - run `rapido-go backup` first if you may want the data.

### Add a node

Open **Nodes** in the dashboard: it shows a single install command for the node, in the form

```bash
curl -fsSL https://<panel>/install/node.sh | sudo bash -s -- '<setup_blob>'
```

Run it as shown on the node server. The setup blob carries the certificate, key, CA, report secret and panel URL, so there is nothing else to configure, and the node only ever dials out to the panel. The script and the node binary are both served by your own panel, so the server needs no GitHub access.

Afterwards the node manages itself with `rapido-go-node status`, `rapido-go-node update` (downloads the version your panel ships, verifies it, and rolls back if it does not start), `rapido-go-node tunnels` (brings up and enables WireGuard exits) and `rapido-go-node uninstall`.

### Migrating from the Python panel

If you are moving from the older Python/MySQL/Xray Rapido (or Marzban), run this **on the old panel server** once Rapido-Go is installed on the new one:

```bash
bash migrate-from-rapido.sh --target https://newpanel.example.com --user admin --pass '...'
```

It moves all three things a migration needs - the database, what each inbound actually is (only the live `xray_config.json` knows), and each host's real address and remark (only the old `hosts` table knows) - and never writes to the old panel, so you can run it as often as you like. Add `--dry-run` to export and report without pushing. Run it once more right before you move DNS: the old panel keeps taking writes until then.

### Configuration

Everything is env-var driven (`internal/config/config.go`), and the installer writes a working `.env` for you. `DATABASE_URL` is the only required variable; the ones you are most likely to touch:

| Variable | Default | Purpose |
|---|---|---|
| `ROLE` | `api` | `api` (scalable) or `backend` (singleton). |
| `DATABASE_URL` | *(required)* | `postgres://user:pass@host:5432/db?sslmode=disable` |
| `REDIS_ADDR` | `127.0.0.1:6379` | Cache for hot read paths. |
| `SUDO_USERNAME` / `SUDO_PASSWORD` | *(none)* | Bootstrap admin, checked before the database - the way in on an empty install. |
| `UVICORN_HOST` / `UVICORN_PORT` | `0.0.0.0` / `8000` | HTTP bind address. |
| `ALLOWED_ORIGINS` | `*` | CORS allow-list - lock this down in production. |
| `XRAY_SUBSCRIPTION_URL_PREFIX` | *(relative)* | Prepended to `/sub/<token>` in generated links. |
| `XRAY_SUBSCRIPTION_URL_PREFIXES` | *(none)* | Every address a subscription answers on, comma-separated, in dashboard order; the first is what reseller bots read. |
| `USAGE_RETENTION_DAYS` | `90` | Days of usage history to keep. |
| `POSTGRES_SHARED_BUFFERS`, `POSTGRES_EFFECTIVE_CACHE_SIZE` | sized from RAM | Postgres memory in the compose stack (`256MB` / `1GB` if unset); the installer sets them from the server's RAM. |

Telegram, Discord and webhook settings exist as env vars too, but are also editable live from the dashboard's **Integrations** page, which overrides them.

Node settings are separate and almost always come from the setup blob; `NODE_LISTEN_ADDR` (`0.0.0.0:62051`) and `NODE_REPORT_INTERVAL_SECONDS` (`10`) are the only ones usually worth changing.

### Development

```bash
docker compose up -d              # Postgres + Redis only
go run ./cmd/panel                # panel, ROLE=api by default
cd web && npm install && npm run dev
```

Database queries are generated with [sqlc](https://sqlc.dev) from `internal/db/queries/*.sql` (`sqlc generate`), migrations run with [goose](https://github.com/pressly/goose) from `internal/db/migrations/`. The generated code is not committed - the Docker build regenerates it.

Tests need a real Postgres and Redis:

```bash
TEST_DATABASE_URL=postgres://... TEST_REDIS_ADDR=127.0.0.1:6379 go test ./... -p 1
```

### Project layout

```
cmd/panel/, cmd/node/       entrypoints
internal/httpapi/           HTTP handlers, routing, the Store (DB + cache)
internal/db/                sqlc queries + generated code + goose migrations
internal/nodecore/          sing-box embedding, the hot-user-update VLESS fork
internal/subscription/      per-format link builders
internal/reviewjob/         user status/expiry state machine
internal/hostmetrics/       CPU/mem/disk/network sampling
web/                        the dashboard
rapido-go.sh                installer / management CLI (the panel)
docker/, docker-compose.prod.yml, Caddyfile.example   images and the production stack
.github/workflows/          image build on every push, tagged releases (v*)
```

### Known limitations

- Shadowsocks 2022 ciphers (`2022-blake3-*`) are not supported; use the classic AEAD ciphers (the default is `chacha20-ietf-poly1305`).
- Per-user usage history older than `USAGE_RETENTION_DAYS` (default 90; `0` keeps everything) is pruned; totals and limits live in counters and are not affected.

---

## فارسی

### این پروژه چیست

یک پنل پروکسی self-hosted. کاربران و ادمین‌های فروشنده را مدیریت می‌کند، اینباندها را روی هر تعداد نود اجرا می‌کند، ترافیک هر کاربر را می‌شمارد و به هر مشترک یک لینک اشتراک می‌دهد که در کلاینت دلخواهش کار می‌کند.

Go و PostgreSQL و Redis در بک‌اند، [sing-box](https://github.com/SagerNet/sing-box) داخل ایجنت نود، و React برای داشبورد.

### امکانات

- **کاربر و فروشنده** — محدودیت حجم، تاریخ انقضا، اکانت on-hold، و ادمین فروشنده‌ای که فقط کاربران خودش را می‌بیند.
- **چند نود** — هر نود ترافیک واقعی را حمل و مصرف و سلامت خودش را گزارش می‌کند. نودها فقط اتصال خروجی می‌زنند، پس پشت NAT هم کار می‌کنند. هر نود می‌تواند کانفیگ خودش را داشته باشد: کدام اینباندها و پورت‌ها را سرو کند، و تنظیمات جدای روت، خروجی، DNS و سطح لاگ.
- **به‌روزرسانی زنده** — افزودن یا حذف کاربر (VLESS، VMess، Trojan یا Shadowsocks) روی اینباند در حال اجرا اعمال می‌شود، بدون قطع‌شدن اتصال کسی؛ و مصرف هر کاربر برای هر چهار پروتکل بایت‌به‌بایت شمرده می‌شود.
- **خروجی‌های وایرگارد که قطع نمی‌شوند** — اگر تونل یک خروجی بمیرد، خودش روی اتصال مستقیم می‌افتد و با برگشتن تونل به آن برمی‌گردد؛ سلامت هر تونل واقعاً تست می‌شود و در داشبورد جدا نشان داده می‌شود.
- **شش فرمت اشتراک** — v2ray، sing-box، Clash، Clash Meta، Outline و v2ray-json.
- **داشبورد** — با React، که خود پنل سرو می‌کند؛ وب‌سرور جدا لازم نیست.
- **اعلان‌ها و کنسول تلگرام** — تلگرام، دیسکورد و وب‌هوک؛ و یک ربات تلگرام تعاملی برای ادمین‌ها: وضعیت سیستم، جست‌وجوی کاربر و کارت کاربر با QR، ساخت/تمدید/غیرفعال/ریست/حذف، سلامت نودها و تونل‌ها، و بکاپ دیتابیس.
- **Gateway (اختیاری)** — چند نصب مستقل را طوری ترکیب می‌کند که یک لینک اشتراک، هاست‌های همه‌شان را داشته باشد.

### پیش‌نیازها

- سرور لینوکس با معماری x86_64 یا aarch64 (اوبونتو، دبیان یا خانواده‌ی RHEL؛ هر چیزی که `apt-get`، `dnf` یا `yum` داشته باشد) و دسترسی root. اگر داکر نصب نباشد، خود اسکریپت نصبش می‌کند.
- دست‌کم ۱ گیگابایت RAM (۲ گیگ یا بیشتر توصیه می‌شود) و ۲ گیگابایت فضای خالی دیسک (۵ گیگ یا بیشتر برای دیتابیس و بکاپ‌ها بهتر است).
- پورت‌های 80 و 443 آزاد و از اینترنت در دسترس باشند؛ Caddy با آن‌ها گواهی می‌گیرد و تمدید می‌کند.
- دامنه‌ای که رکورد A آن به سرور اشاره کند.

### نصب

روی سرور تازه، با کاربر root:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install --domain panel.example.com
```

پیش از هر تغییری، سیستم‌عامل، معماری CPU، RAM، دیسک و پورت‌های 80/443 را بررسی و DNS دامنه را چک می‌کند. اگر رکوردی به این سرور اشاره نکند، دقیقاً همان رکورد A را که باید اضافه کنید نشان می‌دهد و اجازه می‌دهد ادامه بدهید (Caddy تا وقتی رکورد برقرار شود گرفتن گواهی را تکرار می‌کند). هر چیزی که به‌صورت گزینه ندهید پرسیده می‌شود، ولی فقط وقتی ترمینال وصل باشد؛ پس همین دستور بدون نظارت هم اجرا می‌شود.

| گزینه | معنی |
|---|---|
| `--domain <name>` | دامنه‌ی پنل (اگر ندهید پرسیده می‌شود). |
| `--extra-domains <a,b>` | نام‌های بیشتر برای همان داشبورد. |
| `--sub-domain <name[,name]>` | دامنه‌ی جدا فقط برای لینک‌های اشتراک. |
| `--admin-user <name>`، `--admin-pass <pass>` | ورود ادمین اول. پیش‌فرض: `admin` و یک رمز تصادفی که یک بار در پایان چاپ می‌شود. |
| `--token <pat>` | توکن گیت‌هاب؛ فقط اگر مخزن یا ایمیج‌ها خصوصی باشند. |
| `--yes` | هیچ‌چیز نپرس؛ مقدارهای پیش‌فرض را بردار. |
| `--skip-preflight` | بررسی‌های ناموفق سرور (RAM، دیسک، پورت و …) را به هشدار تبدیل می‌کند. |

هر گزینه یک متغیر محیطی هم دارد (`RAPIDO_DOMAIN`، `RAPIDO_EXTRA_DOMAINS`، `RAPIDO_SUB_DOMAIN`، `RAPIDO_ADMIN_USER`، `RAPIDO_ADMIN_PASS`، `RAPIDO_REPO_TOKEN`). برای رمزها از آن‌ها استفاده کنید، چون آرگومان خط فرمان در لیست پروسه‌ها دیده می‌شود.

**مخزن یا ایمیج خصوصی.** به اسکریپت توکنی بدهید که بتواند آن‌ها را بخواند (توکن classic با `repo` و `read:packages`). اول export کنید (اگر بنویسید `VAR=x bash <(curl ... $VAR)` مقدار `$VAR` *پیش از* تنظیم‌شدن باز می‌شود و خود دانلود بدون توکن می‌رود) و اسکریپت را از API گیت‌هاب بگیرید که هر نوع توکنی را می‌پذیرد:

```bash
export RAPIDO_REPO_TOKEN=<token>
bash <(curl -fsSL -H "Authorization: Bearer $RAPIDO_REPO_TOKEN" -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go.sh?ref=master") install --domain panel.example.com
```

اسکریپت فقط وقتی مخزن یا ایمیج‌ها واقعاً خصوصی باشند توکن می‌خواهد یا به‌کار می‌برد؛ آن را چاپ نمی‌کند و فقط در همان حالت داخل `.env` (با دسترسی 600، برای `rapido-go update`) ذخیره می‌کند.

**چه چیزهایی راه‌اندازی می‌شود**

- داکر و افزونه‌ی Compose، اگر نباشند.
- پوشه‌ی `/opt/rapido-go` با فایل compose، فایل `.env` (رمزهای تصادفی، دسترسی 600)، `Caddyfile` و دستور `rapido-go` در `/usr/local/bin`. نصب تازه فقط همین فایل‌ها را دانلود می‌کند و مخزن را clone نمی‌کند.
- خود مجموعه: PostgreSQL 16 (حافظه‌اش بر اساس RAM سرور تنظیم می‌شود)، Redis (فقط کش، هرگز روی دیسک نمی‌نویسد)، مایگریشن‌ها، نقش‌های `api` و `backend` پنل، و Caddy (HTTPS خودکار و فشرده‌سازی gzip/zstd).
- پوشه‌ی `/var/lib/rapido-go` برای بکاپ‌ها.

در پایان یک خلاصه چاپ می‌شود: آدرس داشبورد، ورود ادمین، آدرس پایه‌ی اشتراک، محل بکاپ‌ها و دستورهای زیر.

### دامنه‌ها

- **دامنه‌ی اصلی** (`--domain`) — داشبورد و API مدیریت.
- **دامنه‌های اضافه** (`--extra-domains a.example.com,b.example.com`) — نام‌های بیشتر برای همان داشبورد؛ در یک بلوک سایت با دامنه‌ی اصلی مشترک‌اند.
- **دامنه‌ی اشتراک** (`--sub-domain s.example.com`) — بلوک جدا که فقط `/sub/*` (به‌علاوه‌ی فونت‌های صفحه‌ی اشتراک و یک مسیر سلامت) را سرو می‌کند؛ داشبورد و API مدیریت از آن در دسترس نیست. لینک‌های اشتراک روی همین دامنه ساخته می‌شوند. اگر ندهید، لینک‌ها روی دامنه‌ی پنل ساخته می‌شوند.

همه‌ی نام‌ها در `.env` (`RAPIDO_DOMAIN`، `RAPIDO_EXTRA_DOMAINS`، `RAPIDO_SUB_DOMAIN`) ذخیره و در `/opt/rapido-go/Caddyfile` نوشته می‌شوند؛ `Caddyfile.example` ساختارش را نشان می‌دهد. برای تغییر بعدی، Caddyfile و `.env` را ویرایش کنید و سپس `cd /opt/rapido-go && docker compose -p rapido-go up -d --force-recreate caddy` را بزنید (گواهی‌ها می‌مانند).

### دستورها

اجرای `rapido-go` به‌تنهایی همین فهرست را نشان می‌دهد.

| دستور | کار |
|---|---|
| `install [options]` | نصب (بالا). |
| `update` | ایمیج‌های تازه را می‌گیرد و اگر چیزی عوض شده باشد ری‌استارت می‌کند. |
| `status` / `logs [service]` | وضعیت اجرا / دنبال‌کردن لاگ. |
| `up` / `down` / `restart` | روشن، خاموش، ری‌استارت (`.env` دوباره خوانده می‌شود). |
| `backup [file]` / `restore <file>` | گرفتن بکاپ دیتابیس / جایگزینی دیتابیس با بکاپ. |
| `doctor` | بررسی فقط‌خواندنی: کانتینرها، دیتابیس، دیسک، روزهای باقی‌مانده‌ی گواهی، پاسخ‌دادن داشبورد، نسخه‌ی نصب‌شده در برابر آخرین نسخه. در صورت خطا با کد 1 خارج می‌شود. |
| `edit-env` | ویرایش `.env` و سپس پیشنهاد اعمال. |
| `version` | نسخه‌ی اسکریپت، سورس و ایمیج. |
| `uninstall` | کانتینرها و `/opt/rapido-go` را پاک می‌کند (بکاپ‌ها می‌مانند). |

### به‌روزرسانی

```bash
rapido-go update
```

فایل‌های نصب‌کننده را تازه می‌کند، همه‌ی ایمیج‌ها را هم‌زمان می‌گیرد و اگر چیزی عوض نشده باشد همان‌جا می‌ایستد. در غیر این صورت اول از دیتابیس بکاپ می‌گیرد (۵ بکاپ آخر نگه داشته می‌شود)، بخش‌های تغییرکرده را از نو می‌سازد (مایگریشن‌ها خودکار اجرا می‌شوند) و بررسی می‌کند مجموعه سالم برگشته باشد. نصب‌های قدیمی‌تر (چک‌اوت گیت در `/opt/rapido-go`) هم به همین شکل به‌روز می‌شوند.

### بکاپ و بازیابی

```bash
rapido-go backup                          # /var/lib/rapido-go/backup-<date>.sql.gz
rapido-go backup /root/rapido.sql.gz      # یا فایل دلخواه
rapido-go restore /root/rapido.sql.gz     # دیتابیس را جایگزین می‌کند، اول می‌پرسد
```

هر `update` اول بکاپ می‌گیرد. بکاپ‌ها را از سرور بیرون ببرید؛ روی همان دیسک دیتابیس هستند.

### حذف

`rapido-go uninstall` کانتینرها، ولوم‌هایشان (**از جمله دیتابیس**) و `/opt/rapido-go` را پاک می‌کند. بکاپ‌های `/var/lib/rapido-go` می‌مانند؛ اگر ممکن است داده را بخواهید، اول `rapido-go backup` بزنید.

### افزودن نود

در داشبورد به صفحه‌ی **Nodes** بروید؛ یک دستور نصب واحد برای نود نشان می‌دهد، به این شکل:

```bash
curl -fsSL https://<panel>/install/node.sh | sudo bash -s -- '<setup_blob>'
```

همان را همان‌طور که نشان داده شده روی سرور نود اجرا کنید. setup blob گواهی، کلید، CA، رمز گزارش و آدرس پنل را با خودش دارد، پس چیز دیگری برای تنظیم نمی‌ماند و نود فقط به سمت پنل اتصال خروجی می‌زند.

### مهاجرت از پنل پایتونی

اگر از رپیدوی قدیمی (پایتون/MySQL/Xray) یا مرزبان مهاجرت می‌کنید، بعد از نصب رپیدو-گو روی سرور جدید، این را **روی سرور پنل قدیمی** اجرا کنید:

```bash
bash migrate-from-rapido.sh --target https://newpanel.example.com --user admin --pass '...'
```

هر سه چیزی را که یک مهاجرت لازم دارد منتقل می‌کند: دیتابیس، اینکه هر اینباند واقعاً چیست (فقط `xray_config.json` زنده این را می‌داند)، و آدرس و ریمارک واقعی هر هاست (فقط جدول `hosts` قدیمی این را می‌داند). روی پنل قدیمی چیزی نمی‌نویسد، پس هر چند بار که خواستید می‌توانید اجرایش کنید. با `--dry-run` فقط استخراج و گزارش می‌کند. درست قبل از جابه‌جایی دامنه یک بار دیگر اجرایش کنید، چون پنل قدیمی تا آن لحظه همچنان کاربر جدید می‌سازد.

### پیکربندی

همه‌چیز با متغیر محیطی تنظیم می‌شود و اسکریپت نصب، فایل `.env` آماده برایتان می‌نویسد. تنها متغیر الزامی `DATABASE_URL` است؛ بقیه مقدار پیش‌فرض کارا دارند. تنظیمات تلگرام، دیسکورد و وب‌هوک را می‌توانید زنده از صفحه‌ی **Integrations** داشبورد هم عوض کنید که بر متغیرهای محیطی اولویت دارد.

جدول کامل متغیرها در بخش انگلیسی بالاست.

### محدودیت‌های شناخته‌شده

- رمزهای Shadowsocks 2022 (`2022-blake3-*`) پشتیبانی نمی‌شوند؛ از رمزهای AEAD کلاسیک استفاده کنید (پیش‌فرض `chacha20-ietf-poly1305` است).
- تاریخچه‌ی مصرف هر کاربر که از `USAGE_RETENTION_DAYS` (پیش‌فرض ۹۰ روز؛ `0` یعنی نگه‌داشتن همه) قدیمی‌تر باشد پاک می‌شود؛ جمع مصرف و محدودیت‌ها در شمارنده‌ها می‌مانند و تأثیری نمی‌پذیرند.
