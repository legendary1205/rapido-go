# Rapido-Go

A self-hosted proxy panel. It manages users and reseller admins, runs proxy inbounds across any number of relay nodes, meters per-user traffic, and hands every subscriber one subscription link that works in their client of choice.

Go + PostgreSQL + Redis on the backend, [sing-box](https://github.com/SagerNet/sing-box) embedded in the node agent, React on the dashboard.

**[English](#english)** | **[فارسی](#فارسی)**

---

## English

### What you get

- **Users and resellers** - per-user data limits, expiry, on-hold accounts, and reseller admins who only see their own customers.
- **Multiple nodes** - each node carries real traffic and reports its own usage and health. Nodes only ever dial *out*, so they work behind NAT.
- **Live user updates** - adding or removing a VLESS user takes effect on a running inbound without dropping anyone's connection.
- **Six subscription formats** - v2ray share links, sing-box, Clash, Clash Meta, Outline (SIP008), v2ray-json. `GET /sub/:token` picks one from the client's User-Agent; `GET /sub/:token/<format>` forces it.
- **Dashboard** - React/Vite/Tailwind, served by the panel itself; no extra web server.
- **Notifications** - Telegram, Discord and generic webhooks on user and login events.
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

### Install

On a fresh server with a domain already pointing at it:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install
```

It installs Docker if needed, fetches the source, asks for your domain, gets a Let's Encrypt certificate through Caddy, and starts everything. When it finishes it prints the dashboard URL and a generated admin password - save it, it is not shown again.

Then `rapido-go` on its own lists every management command (`update`, `logs`, `backup`, `restore`, `status`, ...).

### Add a node

Add the node in the dashboard (**Nodes → Add node**), copy the setup blob it gives you, then on the node server:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go-node.sh) install
```

Paste the blob when it asks. That one value carries the certificate, key, CA, report secret and panel URL, so there is nothing else to configure.

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

Telegram, Discord, webhook and KirBot settings exist as env vars too, but are also editable live from the dashboard's **Integrations** page, which overrides them.

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
rapido-go.sh, rapido-go-node.sh   installer / management CLIs
```

### Known limitations

- Every node in a fleet runs the same config; per-node configuration isn't supported yet.
- Telegram integration is notifications only - there is no interactive bot console. The dashboard is the replacement.
- Byte-accurate traffic counting is VLESS-only; other protocols route through unmodified sing-box.

---

## فارسی

### این پروژه چیست

یک پنل پروکسی self-hosted. کاربران و ادمین‌های فروشنده را مدیریت می‌کند، اینباندها را روی هر تعداد نود اجرا می‌کند، ترافیک هر کاربر را می‌شمارد و به هر مشترک یک لینک اشتراک می‌دهد که در کلاینت دلخواهش کار می‌کند.

Go و PostgreSQL و Redis در بک‌اند، [sing-box](https://github.com/SagerNet/sing-box) داخل ایجنت نود، و React برای داشبورد.

### امکانات

- **کاربر و فروشنده** — محدودیت حجم، تاریخ انقضا، اکانت on-hold، و ادمین فروشنده‌ای که فقط کاربران خودش را می‌بیند.
- **چند نود** — هر نود ترافیک واقعی را حمل و مصرف و سلامت خودش را گزارش می‌کند. نودها فقط اتصال خروجی می‌زنند، پس پشت NAT هم کار می‌کنند.
- **به‌روزرسانی زنده** — افزودن یا حذف کاربر VLESS روی اینباند در حال اجرا اعمال می‌شود، بدون قطع‌شدن اتصال کسی.
- **شش فرمت اشتراک** — v2ray، sing-box، Clash، Clash Meta، Outline و v2ray-json.
- **داشبورد** — با React، که خود پنل سرو می‌کند؛ وب‌سرور جدا لازم نیست.
- **اعلان‌ها** — تلگرام، دیسکورد و وب‌هوک.
- **Gateway (اختیاری)** — چند نصب مستقل را طوری ترکیب می‌کند که یک لینک اشتراک، هاست‌های همه‌شان را داشته باشد.

### نصب

روی سروری تازه که دامنه‌اش از قبل به آن اشاره می‌کند:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install
```

اگر داکر نصب نباشد نصبش می‌کند، سورس را می‌گیرد، دامنه را می‌پرسد، با Caddy گواهی Let's Encrypt می‌گیرد و همه‌چیز را بالا می‌آورد. در پایان آدرس داشبورد و یک رمز ادمین چاپ می‌کند — همان‌جا ذخیره‌اش کنید، دوباره نشان داده نمی‌شود.

بعد از آن، دستور `rapido-go` به‌تنهایی همه‌ی دستورهای مدیریتی را نشان می‌دهد (`update`، `logs`، `backup`، `restore`، `status` و …).

### افزودن نود

نود را در داشبورد اضافه کنید (**Nodes ← Add node**)، مقدار setup blob را کپی کنید و روی سرور نود:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go-node.sh) install
```

وقتی پرسید، همان blob را بچسبانید. آن یک مقدار، گواهی و کلید و CA و رمز گزارش و آدرس پنل را با خودش دارد، پس چیز دیگری برای تنظیم نمی‌ماند.

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

- همه‌ی نودهای یک مجموعه کانفیگ یکسان می‌گیرند؛ کانفیگ جدا برای هر نود هنوز پشتیبانی نمی‌شود.
- تلگرام فقط اعلان می‌فرستد و کنسول تعاملی ندارد؛ جایگزینش داشبورد است.
- شمارش دقیق بایت‌به‌بایت ترافیک فقط برای VLESS است.
