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

One command per server - the script fetches everything else itself (Docker, the source, a pre-built image), so there's no manual `git clone` or `docker compose` invocation to get right.

#### 1. Install the panel

**The repository and its images are private**, so the very first fetch needs a GitHub token too - `curl raw.githubusercontent.com` on a private repo just 404s, before the script ever gets a chance to run. Use a token with `repo` + `read:packages` scope (a fine-grained token limited to this repo, with Contents: Read-only and Packages: Read-only, works too):

```bash
export RAPIDO_REPO_TOKEN=<your token>
bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
  -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go.sh?ref=master") install
```

The token is saved into `.env` (chmod 600) so later `rapido-go update` calls keep working unattended - it's never written into the script itself or into `.git/config`. (If this repository is ever made public, the plain, tokenless form also works: `bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install`.)

Installs Docker if it isn't already, asks for the domain you'll run the dashboard on (must already point at this server - [Caddy](https://caddyserver.com/) uses it to request a real Let's Encrypt certificate automatically, no manual certbot dance), generates `.env`, pulls the pre-built image from GHCR, brings up Postgres + Redis + migrations + both the `api` and `backend` roles, and installs itself system-wide as the `rapido-go` command for later management.

At the end it prints a one-time **bootstrap login** - see below.

<details>
<summary><code>rapido-go.sh</code> (click to expand)</summary>

```bash
#!/usr/bin/env bash
#
#  ██████   █████  ██████  ██ ██████   ██████       ██████   ██████
#  ██   ██ ██   ██ ██   ██ ██ ██   ██ ██    ██      ██       ██    ██
#  ██████  ███████ ██████  ██ ██   ██ ██    ██ ████ ██   ███ ██    ██
#  ██   ██ ██   ██ ██      ██ ██   ██ ██    ██      ██    ██ ██    ██
#  ██   ██ ██   ██ ██      ██ ██████   ██████        ██████   ██████
#
#  Rapido-Go - installer and management CLI for the panel.
#
#  Install (this repository is private, so the very first fetch needs a
#  token too - a plain `curl raw.githubusercontent.com` 404s on a private
#  repo before this script ever gets a chance to run):
#
#    export RAPIDO_REPO_TOKEN=<a token with repo + read:packages scope>
#    bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
#      -H "Accept: application/vnd.github.raw" \
#      "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go.sh?ref=master") install
#
#  If this repository is ever made public, the plain form below also works
#  and no token is needed for this first fetch:
#    bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install
#
#  After installing, the command is available system-wide as `rapido-go`.
#
#  This script only ever touches Rapido-Go itself - its source, its .env,
#  its Caddyfile and its containers. It never contains, prompts for or
#  generates any third-party integration secret; those belong in .env on
#  the operator's own machine.
#
set -euo pipefail

RAPIDO_GO_VERSION="1.0.0"

# ─────────────────────────────────────────────────────────────────────────────
# Private repository / private registry access
#
# LEAVE THIS EMPTY in any copy of this script you publish, share, or commit.
# A token written here is readable by everyone who can read the file and by
# everyone who runs it - publishing it is the same as publishing the token.
#
# If Rapido-Go's repository is private, pass the token in for the duration of
# a single command instead, so it never lands on disk:
#
#     RAPIDO_REPO_TOKEN=github_pat_xxx bash rapido-go.sh install
#
# The same token also authenticates the `ghcr.io` image pull (a fine-grained
# token with read-only "Contents" on this repo already carries read:packages
# on its own images) - no separate registry credential to manage.
#
# If you do choose to paste one into your own private copy, use a
# fine-grained token limited to this one repository, and rotate it the
# moment the file leaves your machine.
# ─────────────────────────────────────────────────────────────────────────────
RAPIDO_REPO_TOKEN="${RAPIDO_REPO_TOKEN:-}"

# If the token was not passed in, take the one the install saved. Without
# this every `rapido-go update` on a private repository stops at a git
# username prompt, and an update that cannot run unattended is an update
# that does not happen.
load_saved_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] && return
    [ -f "$APP_DIR/.env" ] || return
    RAPIDO_REPO_TOKEN="$(grep -E '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" 2>/dev/null \
        | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'")"
}

save_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] || return
    [ -f "$APP_DIR/.env" ] || return
    grep -qE '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" && return
    printf '\n# Used to fetch updates from the private repository and pull\n# private images from ghcr.io.\nRAPIDO_REPO_TOKEN="%s"\n' \
        "$RAPIDO_REPO_TOKEN" >> "$APP_DIR/.env"
    chmod 600 "$APP_DIR/.env"
}

REPO_OWNER="${RAPIDO_REPO_OWNER:-legendary1205}"
REPO_NAME="${RAPIDO_REPO_NAME:-rapido-go}"
REPO_BRANCH="${RAPIDO_REPO_BRANCH:-master}"

APP_DIR="${RAPIDO_GO_APP_DIR:-/opt/rapido-go}"
DATA_DIR="${RAPIDO_GO_DATA_DIR:-/var/lib/rapido-go}"
COMPOSE_PROJECT="${RAPIDO_GO_COMPOSE_PROJECT:-rapido-go}"
BIN_PATH="/usr/local/bin/rapido-go"

PANEL_IMAGE="ghcr.io/${REPO_OWNER}/rapido-go-panel"

# ── output ───────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
    C_RESET='\033[0m'; C_DIM='\033[2m'; C_BOLD='\033[1m'
    C_RED='\033[0;31m'; C_GREEN='\033[0;32m'; C_YELLOW='\033[0;33m'; C_CYAN='\033[0;36m'
else
    C_RESET=''; C_DIM=''; C_BOLD=''; C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''
fi

log()  { printf "${C_CYAN}▶${C_RESET} %s\n" "$*"; }
ok()   { printf "${C_GREEN}✔${C_RESET} %s\n" "$*"; }
warn() { printf "${C_YELLOW}!${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}✘ %s${C_RESET}\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

banner() {
    printf "${C_CYAN}${C_BOLD}"
    cat <<'ART'
   ___             _     _         ___
  / _ \__ _ _ __  (_) __| | ___   / __|___
 / /_)/ _` | '_ \ | |/ _` |/ _ \ | (_ / _ \
/ ___/ (_| | |_) || | (_| | (_) | \___\___/
\/    \__,_| .__/ |_|\__,_|\___/
           |_|
ART
    printf "${C_RESET}${C_DIM}  v%s${C_RESET}\n\n" "$RAPIDO_GO_VERSION"
}

require_root() { [ "$(id -u)" = "0" ] || die "This command must be run as root."; }
require_installed() { [ -d "$APP_DIR" ] || die "Rapido-Go is not installed at $APP_DIR. Run: rapido-go install"; }

# ── prerequisites ────────────────────────────────────────────────────────────
pkg_install() {
    if   command -v apt-get >/dev/null 2>&1; then apt-get update -y >/dev/null && apt-get install -y "$@" >/dev/null
    elif command -v dnf     >/dev/null 2>&1; then dnf install -y "$@" >/dev/null
    elif command -v yum     >/dev/null 2>&1; then yum install -y "$@" >/dev/null
    else die "No supported package manager found (need apt-get, dnf or yum)."
    fi
}

ensure_prereqs() {
    local missing=()
    for c in curl git openssl; do command -v "$c" >/dev/null 2>&1 || missing+=("$c"); done
    if [ ${#missing[@]} -gt 0 ]; then
        log "Installing prerequisites: ${missing[*]}"
        pkg_install "${missing[@]}"
    fi
    ok "Prerequisites present."
}

# `docker compose version` answers from the client alone, so it reports
# success even when the daemon is dead - `docker info` is the one that
# actually talks to it.
docker_ready() {
    command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 \
        && docker compose version >/dev/null 2>&1
}

ensure_docker() {
    if docker_ready; then
        ok "Docker is installed and running."
        return
    fi

    if command -v docker >/dev/null 2>&1; then
        log "Docker is installed but not responding - trying to start it..."
        systemctl start docker >/dev/null 2>&1 || true
        sleep 3
        if docker_ready; then
            ok "Docker started."
            return
        fi
        err "The Docker daemon is not running, and would not start."
        err ""
        err "  systemctl status docker --no-pager -l"
        err "  journalctl -u docker -n 30 --no-pager"
        exit 1
    fi

    log "Installing Docker..."
    curl -fsSL https://get.docker.com | sh >/dev/null
    systemctl enable --now docker >/dev/null 2>&1 || true
    sleep 3
    docker_ready || die "Docker installed but the daemon is not responding. Check: journalctl -u docker -n 30"
    ok "Docker installed and running."
}

# GHCR images stay private by default even though the pull itself is a
# plain `docker pull` - the same token that clones the source also has
# read:packages on it, so log in once here rather than making the operator
# manage a second credential.
ensure_registry_login() {
    load_saved_token
    [ -n "$RAPIDO_REPO_TOKEN" ] || return 0
    echo "$RAPIDO_REPO_TOKEN" | docker login ghcr.io -u "$REPO_OWNER" --password-stdin >/dev/null 2>&1 \
        && ok "Logged in to ghcr.io." \
        || warn "Could not log in to ghcr.io with the given token - continuing (the image may still be reachable)."
}

# ── source ───────────────────────────────────────────────────────────────────
repo_url() {
    if [ -n "$RAPIDO_REPO_TOKEN" ]; then
        printf 'https://%s@github.com/%s/%s.git' "$RAPIDO_REPO_TOKEN" "$REPO_OWNER" "$REPO_NAME"
    else
        printf 'https://github.com/%s/%s.git' "$REPO_OWNER" "$REPO_NAME"
    fi
}

# Keeps the token out of .git/config, out of `git remote -v`, and out of any
# later `git fetch` a different operator on this box might run.
scrub_remote() {
    git -C "$APP_DIR" remote set-url origin \
        "https://github.com/${REPO_OWNER}/${REPO_NAME}.git" 2>/dev/null || true
}

fetch_source() {
    load_saved_token
    if [ -d "$APP_DIR/.git" ]; then
        log "Updating source in $APP_DIR..."
        git -C "$APP_DIR" remote set-url origin "$(repo_url)"
        git -C "$APP_DIR" -c credential.helper= fetch --depth 1 origin "$REPO_BRANCH" \
            || { scrub_remote; die "Could not fetch the repository. If it is private, run: RAPIDO_REPO_TOKEN=<token> rapido-go update"; }
        git -C "$APP_DIR" reset --hard "origin/$REPO_BRANCH" >/dev/null
        scrub_remote
    else
        log "Downloading Rapido-Go into $APP_DIR..."
        mkdir -p "$(dirname "$APP_DIR")"
        git -c credential.helper= clone --depth 1 --branch "$REPO_BRANCH" \
            "$(repo_url)" "$APP_DIR" >/dev/null 2>&1 \
            || die "Could not clone the repository. If it is private, set RAPIDO_REPO_TOKEN."
        scrub_remote
    fi
    ok "Source ready ($(git -C "$APP_DIR" rev-parse --short HEAD))."
}

# ── configuration ────────────────────────────────────────────────────────────
random_secret() { openssl rand -hex 16; }

# The panel domain matters more than it looks: it is what goes into every
# subscriber's subscription link. Without it the panel has no TLS in front
# of it at all - the Go binary itself has no built-in HTTPS listener - and
# Caddy (below) has nothing to request a certificate for.
_clean_host() { printf '%s' "$1" | tr -d ' ' | sed -e 's#^https\?://##' -e 's#/.*$##' -e 's#:.*$##'; }
_valid_host() { printf '%s' "$1" | grep -qE '^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$'; }

prompt_domain() {
    if [ -f "$APP_DIR/.env" ] && [ -z "${RAPIDO_DOMAIN:-}" ]; then
        local from_env
        from_env="$(grep -E '^RAPIDO_DOMAIN=' "$APP_DIR/.env" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"')"
        if [ -n "$from_env" ]; then
            RAPIDO_DOMAIN="$from_env"
            ok "Using the domain already in $APP_DIR/.env: $RAPIDO_DOMAIN"
            return
        fi
    fi

    printf "\n${C_BOLD}Panel domain${C_RESET}\n"
    printf "${C_DIM}  The name you will open the dashboard on, e.g. panel.example.com\n"
    printf "  It must already point at this server - Caddy requests a real\n"
    printf "  certificate for it and that check is done by connecting to the\n"
    printf "  name over the internet.${C_RESET}\n\n"
    while [ -z "${RAPIDO_DOMAIN:-}" ]; do
        printf "  Panel domain: "
        read -r RAPIDO_DOMAIN || RAPIDO_DOMAIN=""
        RAPIDO_DOMAIN="$(_clean_host "$RAPIDO_DOMAIN")"
        if [ -z "$RAPIDO_DOMAIN" ]; then
            warn "  A domain is required - Rapido-Go does not install on a bare IP."
        elif ! _valid_host "$RAPIDO_DOMAIN"; then
            warn "  '$RAPIDO_DOMAIN' does not look like a hostname."
            RAPIDO_DOMAIN=""
        fi
    done
    ok "Panel domain: $RAPIDO_DOMAIN"
}

# Caddy handles the certificate itself (request + renewal, zero ongoing
# maintenance) - this just writes the one file that tells it what to do,
# same idea as generate_env below but for Caddy's config instead of the
# panel's.
write_caddyfile() {
    cat > "$APP_DIR/Caddyfile" <<EOF
${RAPIDO_DOMAIN} {
	reverse_proxy panel:8000
}
EOF
    ok "Caddyfile written for $RAPIDO_DOMAIN."
}

# Sizes to nothing machine-specific today (Postgres/Redis use their own
# image defaults), but kept as its own step - same shape as the Python
# installer's sizing pass - so a future tuning knob has one obvious place
# to land instead of being bolted onto generate_env directly.
size_for_machine() {
    local cores ram_mb
    cores="$(nproc 2>/dev/null || echo 2)"
    ram_mb="$(free -m 2>/dev/null | awk '/Mem:/{print $2}')"
    [ -n "$ram_mb" ] || ram_mb=2048
    log "This machine: ${cores} cores, ${ram_mb}MB RAM"
}

generate_env() {
    local env_file="$APP_DIR/.env"
    if [ -f "$env_file" ]; then
        warn "$env_file already exists - leaving it untouched."
        return
    fi
    size_for_machine
    local db_pass sudo_pass ip
    db_pass="$(random_secret)"
    sudo_pass="$(random_secret)"
    ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')"
    mkdir -p "$DATA_DIR"

    cat > "$env_file" <<EOF
# Generated by the Rapido-Go installer on $(date -u +%Y-%m-%dT%H:%M:%SZ)
# Edit with: rapido-go edit-env      (restart afterwards: rapido-go restart)

# ── domain ──────────────────────────────────────────────────────────────────
RAPIDO_DOMAIN="${RAPIDO_DOMAIN}"

# ── database ────────────────────────────────────────────────────────────────
POSTGRES_PASSWORD="${db_pass}"

# ── first admin ─────────────────────────────────────────────────────────────
# This is the one-time bootstrap login (checked in-memory before the admins
# table is ever queried, so there is always a way in from an empty
# database). Log in once, create a real sudo admin from the Admins page,
# then rotate this value if you want the break-glass login to stop working.
SUDO_USERNAME="admin"
SUDO_PASSWORD="${sudo_pass}"

# ── what customers get ──────────────────────────────────────────────────────
# The prefix every subscription link is built from. Change it here if the
# domain changes - existing links are rebuilt from it on the next fetch.
XRAY_SUBSCRIPTION_URL_PREFIX="https://${RAPIDO_DOMAIN}"
# Feeds the {SERVER_IP} placeholder in subscription remarks, if you use it.
PUBLIC_IP="${ip}"

# ── CORS ────────────────────────────────────────────────────────────────────
ALLOWED_ORIGINS="*"
EOF
    chmod 600 "$env_file"
    ADMIN_PASSWORD="$sudo_pass"
    ok "Configuration written to $env_file"
}

# The image is published by CI, so a normal install pulls it instead of
# compiling one. Building locally means Go + Node + sqlc/goose install:
# several minutes of CPU on a small VPS, paid on every install and every
# update to produce a byte-identical result - so it stays the fallback,
# not the default. See docker/Dockerfile.panel.
obtain_image() {
    if [ "${RAPIDO_BUILD_LOCALLY:-}" = "1" ]; then
        log "RAPIDO_BUILD_LOCALLY=1, building from source..."
    else
        log "Fetching the Rapido-Go panel image..."
        if docker pull "${PANEL_IMAGE}:latest" 2>&1 | tail -2; then
            ok "Image ready."
            return 0
        fi
        warn "Could not pull the prebuilt image; building it here instead."
    fi

    log "Building the image (this takes a few minutes)..."
    ( cd "$APP_DIR" && docker build -f docker/Dockerfile.panel -t "${PANEL_IMAGE}:latest" . )
    docker image inspect "${PANEL_IMAGE}:latest" >/dev/null 2>&1 \
        || die "The image was not built - see the output above."
    ok "Image built."
}

install_command() {
    local src="$APP_DIR/rapido-go.sh"
    [ -f "$src" ] || src="$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || true)"
    [ -n "$src" ] && [ -f "$src" ] || return 0
    # `install` errors with "are the same file" when the CLI is run as
    # /usr/local/bin/rapido-go and that is also the source - under `set -e`
    # that would abort an update before it rebuilt anything.
    if [ "$(readlink -f "$src")" = "$(readlink -f "$BIN_PATH" 2>/dev/null || echo /nonexistent)" ]; then
        return 0
    fi
    if install -m 755 "$src" "$BIN_PATH" 2>/dev/null; then
        ok "Installed the 'rapido-go' command at $BIN_PATH"
    else
        warn "Could not update $BIN_PATH - continuing."
    fi
}

# ── compose ──────────────────────────────────────────────────────────────────
compose_file() { echo "docker-compose.prod.yml"; }

compose() {
    require_installed
    (cd "$APP_DIR" && docker compose -p "$COMPOSE_PROJECT" -f "$(compose_file)" --env-file .env "$@")
}

wait_healthy() {
    # Caddy needs a moment to get its certificate on a fresh domain before
    # https:// answers - poll plain HTTP on the panel's own compose network
    # first (proves the app itself is up), then the public https:// URL.
    log "Waiting for the panel to come up..."
    local i
    for i in $(seq 1 60); do
        compose exec -T panel wget -q -O /dev/null http://127.0.0.1:8000/dashboard/ 2>/dev/null \
            && { ok "Panel is answering internally (${i}s)."; break; }
        sleep 1
        [ "$i" -eq 60 ] && { warn "The panel did not answer internally within 60s. Check: rapido-go logs panel"; return 0; }
    done

    log "Waiting for https://${RAPIDO_DOMAIN}/dashboard/ (Caddy obtaining a certificate)..."
    for i in $(seq 1 90); do
        curl -fsS --max-time 3 -o /dev/null "https://${RAPIDO_DOMAIN}/dashboard/" 2>/dev/null \
            && { ok "Panel is up at https://${RAPIDO_DOMAIN}/dashboard/ (${i}s)."; return 0; }
        sleep 1
    done
    warn "https://${RAPIDO_DOMAIN} did not answer within 90s."
    printf "${C_DIM}  Usually the domain does not point here yet, or inbound port 80/443\n"
    printf "  is blocked - Caddy needs both to obtain a certificate.\n"
    printf "  Check: rapido-go logs caddy${C_RESET}\n"
}

# ── commands ─────────────────────────────────────────────────────────────────
verify_stack() {
    local want got missing=""
    want="$(cd "$APP_DIR" && docker compose -p "$COMPOSE_PROJECT" -f "$(compose_file)" config --services 2>/dev/null)"
    got="$(cd "$APP_DIR" && docker compose -p "$COMPOSE_PROJECT" -f "$(compose_file)" --env-file .env ps --services --filter status=running 2>/dev/null)"
    for svc in $want; do
        [ "$svc" = "migrate" ] && continue # exits 0 on purpose once done
        printf '%s\n' "$got" | grep -qx "$svc" || missing="$missing $svc"
    done
    if [ -n "$missing" ]; then
        err "These services are not running:$missing"
        compose ps
        return 1
    fi
    ok "All services running."
}

follow_logs() {
    [ -t 1 ] || return 0
    [ "${RAPIDO_NO_FOLLOW:-0}" = "1" ] && return 0
    printf "\n${C_DIM}  Following the log - Ctrl-C leaves it running.${C_RESET}\n\n"
    compose logs -f --tail "${RAPIDO_LOG_TAIL:-60}"
}

cmd_install() {
    require_root
    banner
    ensure_prereqs
    ensure_docker
    fetch_source
    prompt_domain
    generate_env
    write_caddyfile
    save_token
    install_command
    ensure_registry_login
    obtain_image
    log "Starting Rapido-Go..."
    compose up -d
    wait_healthy
    verify_stack || die "The stack did not come up cleanly - see the output above."

    printf "\n${C_GREEN}${C_BOLD}Rapido-Go is installed.${C_RESET}\n\n"
    printf "  Panel    ${C_BOLD}https://%s/dashboard/${C_RESET}\n" "$RAPIDO_DOMAIN"
    if [ -n "${ADMIN_PASSWORD:-}" ]; then
        printf "  Username ${C_BOLD}admin${C_RESET}\n"
        printf "  Password ${C_BOLD}%s${C_RESET}\n" "$ADMIN_PASSWORD"
        printf "\n${C_YELLOW}  Save that password now - it is not shown again. Log in once,\n"
        printf "  create a real sudo admin, then rotate this one if you want.${C_RESET}\n"
    fi
    printf "\n  ${C_DIM}Manage it with: rapido-go status | logs | restart | update | backup${C_RESET}\n"
    follow_logs
}

cmd_up()      { compose up -d && ok "Started."; }
cmd_down()    { compose down && ok "Stopped."; }
# `compose restart` does NOT re-read env_file - it restarts the container
# with the configuration it was created with, so editing .env and running
# `restart` would silently change nothing. `panel_services` deliberately
# excludes postgres/redis/caddy: recreating those on every restart/update
# is a needless minute of downtime and a needless risk to the state they
# hold, for a step whose real purpose is picking up an edited .env, which
# only panel/backend read.
panel_services() { echo "panel backend"; }

restart_panel() {
    compose up -d
    # shellcheck disable=SC2046
    compose up -d --force-recreate $(panel_services)
}

cmd_restart() { restart_panel && verify_stack && ok "Restarted."; follow_logs; }
cmd_status()  { compose ps; }
cmd_logs()    { compose logs -f --tail "${RAPIDO_LOG_TAIL:-200}" ${1:+"$1"}; }

cmd_update() {
    require_root
    require_installed
    log "Backing up the database first..."
    cmd_backup >/dev/null || warn "Backup failed - continuing anyway."
    fetch_source
    install_command
    ensure_registry_login
    obtain_image
    compose up -d  # re-applies migrations via the migrate service, then recreates whatever image tag changed
    restart_panel
    wait_healthy
    docker image prune -f >/dev/null 2>&1 || true
    ls -1t "$DATA_DIR"/backup-*.sql.gz 2>/dev/null | tail -n +6 | xargs -r rm -f
    verify_stack || die "The stack did not come back cleanly after the update."
    ok "Updated."
    follow_logs
}

cmd_backup() {
    require_installed
    local dest="${1:-$DATA_DIR/backup-$(date +%Y%m%d-%H%M%S).sql.gz}"
    mkdir -p "$(dirname "$dest")"
    log "Dumping the database to $dest..."
    compose exec -T postgres sh -c 'exec pg_dump -U rapido rapido' | gzip > "$dest"
    [ -s "$dest" ] || { rm -f "$dest"; die "Backup produced an empty file - nothing was written."; }
    ok "Backup written: $dest ($(du -h "$dest" | cut -f1))"
}

cmd_restore() {
    require_root
    require_installed
    local src="${1:-}"
    [ -n "$src" ] || die "Usage: rapido-go restore <backup.sql.gz>"
    [ -f "$src" ] || die "No such file: $src"
    warn "This REPLACES the current database with the contents of $src."
    printf "Type 'yes' to continue: "
    local answer; read -r answer
    [ "$answer" = "yes" ] || die "Aborted."
    # Panel/backend hold open connections that would fight a restore.
    compose stop panel backend
    gunzip -c "$src" | compose exec -T postgres sh -c \
        'exec psql -U rapido -d rapido -v ON_ERROR_STOP=1'
    compose up -d panel backend
    ok "Restored from $src"
}

cmd_edit_env() {
    require_installed
    local before after
    before="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1)"
    "${EDITOR:-nano}" "$APP_DIR/.env"
    after="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1)"
    if [ "$before" != "$after" ]; then
        printf "\n"
        warn "Configuration changed. Apply it now? [Y/n] "
        local answer; read -r answer
        case "${answer:-y}" in
            [Nn]*) warn "Not applied. Run 'rapido-go restart' when ready." ;;
            *) cmd_restart ;;
        esac
    fi
}

cmd_uninstall() {
    require_root
    require_installed
    warn "This stops and removes Rapido-Go's containers and its source at $APP_DIR."
    warn "Your data in $DATA_DIR (database backups) is KEPT."
    printf "Type 'yes' to continue: "
    local answer; read -r answer
    [ "$answer" = "yes" ] || die "Aborted."
    compose down --remove-orphans --volumes || true
    rm -rf "$APP_DIR"
    rm -f "$BIN_PATH"
    ok "Rapido-Go removed. Backups left in $DATA_DIR"
}

cmd_version() {
    printf "rapido-go %s\n" "$RAPIDO_GO_VERSION"
    [ -d "$APP_DIR/.git" ] && printf "source    %s\n" "$(git -C "$APP_DIR" rev-parse --short HEAD)"
    return 0
}

usage() {
    banner
    cat <<EOF
${C_BOLD}Rapido-Go${C_RESET} - a self-hosted proxy panel: users, resellers, nodes and
subscriptions, with per-user traffic accounting.

${C_BOLD}USAGE${C_RESET}
  rapido-go <command> [arguments]

${C_BOLD}SETUP${C_RESET}
  install                  Install Docker if needed, fetch, configure and start
  update                   Back up, fetch the latest source, pull/rebuild and restart
  uninstall                Remove the containers and source (backups are kept)

${C_BOLD}RUNNING${C_RESET}
  up | down | restart      Start, stop or restart the stack
  status                   Show what is running
  logs [service]           Follow the logs of everything, or one service

${C_BOLD}DATA${C_RESET}
  backup [file]            Dump the database to a .sql.gz file
  restore <file>           Replace the database from a dump (asks first)

${C_BOLD}ADMIN${C_RESET}
  edit-env                 Open .env in \$EDITOR
  version                  Show the installed version

${C_BOLD}ENVIRONMENT${C_RESET}
  RAPIDO_GO_APP_DIR         Where the source lives          (default: /opt/rapido-go)
  RAPIDO_GO_DATA_DIR        Where backups live               (default: /var/lib/rapido-go)
  RAPIDO_DOMAIN             Panel domain, to skip the prompt
  RAPIDO_NO_FOLLOW=1        Do not tail the log after install/restart
  RAPIDO_REPO_TOKEN         GitHub token, if the repo is private
  RAPIDO_REPO_BRANCH        Branch to install from           (default: master)
  RAPIDO_BUILD_LOCALLY=1    Build the image here instead of pulling from ghcr.io

${C_BOLD}EXAMPLES${C_RESET}
  rapido-go install
  RAPIDO_DOMAIN=panel.example.com rapido-go install
  rapido-go logs panel
  rapido-go backup /root/rapido-go-\$(date +%F).sql.gz

EOF
}

main() {
    local cmd="${1:-}"
    [ $# -gt 0 ] && shift || true
    case "$cmd" in
        install)
            while [ $# -gt 0 ]; do
                case "$1" in
                    --domain) RAPIDO_DOMAIN="${2:-}"; shift 2 ;;
                    *) die "Unknown option for install: $1" ;;
                esac
            done
            cmd_install
            ;;
        up)        cmd_up ;;
        down)      cmd_down ;;
        restart)   cmd_restart ;;
        status)    cmd_status ;;
        logs)      cmd_logs "${1:-}" ;;
        backup)    cmd_backup "${1:-}" ;;
        restore)   cmd_restore "${1:-}" ;;
        update)    cmd_update ;;
        edit-env)  cmd_edit_env ;;
        uninstall) cmd_uninstall ;;
        version|-v|--version) cmd_version ;;
        ""|help|-h|--help) usage ;;
        *) err "Unknown command: $cmd"; echo; usage; exit 1 ;;
    esac
}

main "$@"
```

</details>

#### 2. First login

The install prints a **bootstrap login** (`SUDO_USERNAME`/`SUDO_PASSWORD`, saved into `.env`). This is not a database row - it's checked in-memory before the `admins` table is ever queried, specifically so there's always a way in even from a completely empty database. Use it once to:

1. Log in at `https://<your-domain>/dashboard/`.
2. Create a real sudo admin from the **Admins** page.
3. Optionally rotate `SUDO_PASSWORD` with `rapido-go edit-env` if you don't want the break-glass login to keep working with its original value.

#### 3. Add and install a node

1. In the dashboard, go to **Nodes → Add Node**, give it a name and address, save.
2. The one-time reveal panel shows a single **setup_blob** value (base64) - this bundles the node's certificate, private key, the panel's CA, and its report secret together. Copy it now; it is never shown again.
3. On the node server (same private-repo token as the panel - see above):

```bash
export RAPIDO_REPO_TOKEN=<your token>
bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
  -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go-node.sh?ref=master") install
# paste the setup_blob when prompted, then pick a listen port (default 62051)
```

or fully non-interactively:

```bash
export RAPIDO_REPO_TOKEN=<your token>
NODE_SETUP_BLOB='<paste the blob here>' \
  bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
    -H "Accept: application/vnd.github.raw" \
    "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go-node.sh?ref=master") install
```

<details>
<summary><code>rapido-go-node.sh</code> (click to expand)</summary>

```bash
#!/usr/bin/env bash
#
#  Rapido-Go Node - installer and management CLI.
#
#  Install (this repository is private, so the very first fetch needs a
#  token too - a plain `curl raw.githubusercontent.com` 404s on a private
#  repo before this script ever gets a chance to run):
#
#    export RAPIDO_REPO_TOKEN=<a token with repo + read:packages scope>
#    bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
#      -H "Accept: application/vnd.github.raw" \
#      "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go-node.sh?ref=master") install
#
#  If this repository is ever made public, the plain form below also works
#  and no token is needed for this first fetch:
#    bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go-node.sh) install
#
#  Afterwards the command is available system-wide as `rapido-go-node`.
#
set -euo pipefail

RAPIDO_GO_NODE_VERSION="1.0.0"

# See the note in rapido-go.sh: leave empty in any copy you publish, and
# pass the token for one command instead -
# RAPIDO_REPO_TOKEN=xxx bash rapido-go-node.sh install
RAPIDO_REPO_TOKEN="${RAPIDO_REPO_TOKEN:-}"

load_saved_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] && return
    [ -f "$APP_DIR/.env" ] || return
    RAPIDO_REPO_TOKEN="$(grep -E '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" 2>/dev/null \
        | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'")"
}

save_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] || return
    [ -f "$APP_DIR/.env" ] || return
    grep -qE '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" && return
    printf '\n# Used to fetch updates from the private repository and pull\n# private images from ghcr.io.\nRAPIDO_REPO_TOKEN="%s"\n' \
        "$RAPIDO_REPO_TOKEN" >> "$APP_DIR/.env"
    chmod 600 "$APP_DIR/.env"
}

REPO_OWNER="${RAPIDO_REPO_OWNER:-legendary1205}"
REPO_NAME="${RAPIDO_REPO_NAME:-rapido-go}"
REPO_BRANCH="${RAPIDO_REPO_BRANCH:-master}"

APP_DIR="${RAPIDO_GO_NODE_APP_DIR:-/opt/rapido-go-node}"
DATA_DIR="${RAPIDO_GO_NODE_DATA_DIR:-/var/lib/rapido-go-node}"
COMPOSE_PROJECT="${RAPIDO_GO_NODE_COMPOSE_PROJECT:-rapido-go-node}"
BIN_PATH="/usr/local/bin/rapido-go-node"

NODE_IMAGE="ghcr.io/${REPO_OWNER}/rapido-go-node"
LISTEN_PORT="${LISTEN_PORT:-62051}"

if [ -t 1 ]; then
    C_RESET='\033[0m'; C_DIM='\033[2m'; C_BOLD='\033[1m'
    C_RED='\033[0;31m'; C_GREEN='\033[0;32m'; C_YELLOW='\033[0;33m'; C_CYAN='\033[0;36m'
else
    C_RESET=''; C_DIM=''; C_BOLD=''; C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''
fi

log()  { printf "${C_CYAN}▶${C_RESET} %s\n" "$*"; }
ok()   { printf "${C_GREEN}✔${C_RESET} %s\n" "$*"; }
warn() { printf "${C_YELLOW}!${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}✘ %s${C_RESET}\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

banner() {
    printf "${C_CYAN}${C_BOLD}"
    cat <<'ART'
   ___             _     _         ___       _  _         _
  / _ \__ _ _ __  (_) __| | ___   / __|___  | \| |___  __| |___
 / /_)/ _` | '_ \ | |/ _` |/ _ \ | (_ / _ \ | .` / _ \/ _` / -_)
/ ___/ (_| | |_) || | (_| | (_) | \___\___/ |_|\_\___/\__,_\___|
\/    \__,_| .__/ |_|\__,_|\___/
           |_|
ART
    printf "${C_RESET}${C_DIM}  v%s${C_RESET}\n\n" "$RAPIDO_GO_NODE_VERSION"
}

require_root() { [ "$(id -u)" = "0" ] || die "This command must be run as root."; }
require_installed() { [ -d "$APP_DIR" ] || die "Not installed at $APP_DIR. Run: rapido-go-node install"; }

pkg_install() {
    if   command -v apt-get >/dev/null 2>&1; then apt-get update -y >/dev/null && apt-get install -y "$@" >/dev/null
    elif command -v dnf     >/dev/null 2>&1; then dnf install -y "$@" >/dev/null
    elif command -v yum     >/dev/null 2>&1; then yum install -y "$@" >/dev/null
    else die "No supported package manager found (need apt-get, dnf or yum)."
    fi
}

ensure_prereqs() {
    local missing=()
    for c in curl git nano; do command -v "$c" >/dev/null 2>&1 || missing+=("$c"); done
    [ ${#missing[@]} -gt 0 ] && { log "Installing: ${missing[*]}"; pkg_install "${missing[@]}"; }
    ok "Prerequisites present."
}

docker_ready() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }

ensure_compose() {
    if docker compose version >/dev/null 2>&1; then
        ok "docker compose present."
        return
    fi
    log "Installing the docker compose plugin..."
    pkg_install docker-compose-plugin 2>/dev/null || true
    docker compose version >/dev/null 2>&1 \
        || die "Could not install the docker compose plugin. Install it and run this again."
    ok "docker compose installed."
}

install_docker_engine() {
    log "Installing Docker..."
    curl -fsSL https://get.docker.com -o /tmp/get-docker.sh \
        || die "Could not download the Docker installer. Check this server's internet access."
    sh /tmp/get-docker.sh >/dev/null 2>&1 || warn "The Docker installer reported a problem - checking anyway."
    rm -f /tmp/get-docker.sh
    systemctl enable --now containerd >/dev/null 2>&1 || true
    systemctl enable --now docker >/dev/null 2>&1 || true
    sleep 4
}

docker_diagnosis() {
    err "Docker is still not responding. What the system says:"
    echo
    if systemctl list-unit-files 2>/dev/null | grep -q '^docker\.service'; then
        systemctl status docker --no-pager -l 2>&1 | head -12 | sed 's/^/    /'
        echo
        journalctl -u docker -n 15 --no-pager 2>&1 | tail -12 | sed 's/^/    /'
    else
        echo "    There is no docker.service unit on this system."
        echo "    docker binary : $(command -v docker || echo none)"
    fi
    echo
    err "Fix that and run the installer again."
    exit 1
}

ensure_docker() {
    if docker_ready; then
        ok "Docker is installed and running."
        return
    fi
    if ! systemctl list-unit-files 2>/dev/null | grep -q '^docker\.service'; then
        command -v docker >/dev/null 2>&1 && warn "The docker command is present but the engine is not installed."
        install_docker_engine
        docker_ready && { ok "Docker installed and running."; return; }
        docker_diagnosis
    fi
    log "Docker is installed but not responding - trying to start it..."
    systemctl start containerd >/dev/null 2>&1 || true
    systemctl start docker.socket >/dev/null 2>&1 || true
    systemctl start docker >/dev/null 2>&1 || true
    sleep 4
    docker_ready && { ok "Docker started."; return; }
    warn "It would not start - repairing the installation..."
    install_docker_engine
    docker_ready && { ok "Docker repaired and running."; return; }
    docker_diagnosis
}

ensure_registry_login() {
    load_saved_token
    [ -n "$RAPIDO_REPO_TOKEN" ] || return 0
    echo "$RAPIDO_REPO_TOKEN" | docker login ghcr.io -u "$REPO_OWNER" --password-stdin >/dev/null 2>&1 \
        && ok "Logged in to ghcr.io." \
        || warn "Could not log in to ghcr.io with the given token - continuing."
}

repo_url() {
    if [ -n "$RAPIDO_REPO_TOKEN" ]; then
        printf 'https://%s@github.com/%s/%s.git' "$RAPIDO_REPO_TOKEN" "$REPO_OWNER" "$REPO_NAME"
    else
        printf 'https://github.com/%s/%s.git' "$REPO_OWNER" "$REPO_NAME"
    fi
}

scrub_remote() {
    git -C "$APP_DIR" remote set-url origin \
        "https://github.com/${REPO_OWNER}/${REPO_NAME}.git" 2>/dev/null || true
}

fetch_source() {
    load_saved_token
    if [ -d "$APP_DIR/.git" ]; then
        log "Updating source..."
        git -C "$APP_DIR" remote set-url origin "$(repo_url)"
        git -C "$APP_DIR" -c credential.helper= fetch --depth 1 origin "$REPO_BRANCH" \
            || { scrub_remote; die "Could not fetch. If the repo is private, set RAPIDO_REPO_TOKEN."; }
        git -C "$APP_DIR" reset --hard "origin/$REPO_BRANCH" >/dev/null
        scrub_remote
    else
        log "Downloading Rapido-Go into $APP_DIR..."
        mkdir -p "$(dirname "$APP_DIR")"
        git -c credential.helper= clone --depth 1 --branch "$REPO_BRANCH" "$(repo_url)" "$APP_DIR" >/dev/null 2>&1 \
            || die "Could not clone. If the repo is private, set RAPIDO_REPO_TOKEN."
        scrub_remote
    fi
    ok "Source ready ($(git -C "$APP_DIR" rev-parse --short HEAD))."
}

# The node's own control API is its only listening port - unlike the
# Python-era node (a separate panel-facing port plus a second Xray gRPC
# port), rapido-go embeds sing-box directly, so there is just the one.
port_in_use() { ss -lnt 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$1\$"; }

prompt_port() {
    if [ -f "$APP_DIR/.env" ] && [ -z "${RAPIDO_GO_NODE_PORT_SET:-}" ]; then
        local from_env
        from_env="$(grep -E '^LISTEN_PORT=' "$APP_DIR/.env" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"')"
        [ -n "$from_env" ] && { LISTEN_PORT="$from_env"; ok "Using the port already in .env: $LISTEN_PORT"; return; }
    fi
    printf "\n${C_BOLD}Port${C_RESET}\n"
    printf "${C_DIM}  Must be reachable from the panel server. Press Enter to accept\n"
    printf "  the default.${C_RESET}\n\n"
    while true; do
        printf "  Listen port [%s]: " "$LISTEN_PORT"
        local value; read -r value || value=""
        value="${value:-$LISTEN_PORT}"
        if ! printf '%s' "$value" | grep -qE '^[0-9]+$' || [ "$value" -lt 1 ] || [ "$value" -gt 65535 ]; then
            warn "  Ports are numbers between 1 and 65535."; continue
        fi
        if port_in_use "$value"; then
            warn "  Something is already listening on $value."; continue
        fi
        LISTEN_PORT="$value"
        break
    done
    ok "Listen port: $LISTEN_PORT"
}

# The one value from the panel's Nodes -> Add Node -> one-time reveal panel
# - bundles this node's certificate, private key, the panel's CA and its
# report secret together (see internal/httpapi/node.go's buildNodeSetupBlob
# on the panel side, cmd/node/main.go's applyNodeSetupBlob on this side).
# Only needed once: the node writes it to disk on first boot and never
# reads the env var again after that, so re-running this against a volume
# that already has cert.pem in it is a no-op here, same idea as
# docker-compose.node.yml's own version of this check.
prompt_setup_blob() {
    if [ -f "$DATA_DIR/certs/cert.pem" ] && [ -z "${NODE_SETUP_BLOB:-}" ]; then
        ok "This node is already provisioned (certs present in $DATA_DIR/certs) - skipping."
        return
    fi
    if [ -n "${NODE_SETUP_BLOB:-}" ]; then
        return
    fi
    printf "\n${C_BOLD}Paste the setup_blob${C_RESET}\n"
    printf "${C_DIM}  Panel -> Nodes -> Add Node -> the one-time reveal panel. It is\n"
    printf "  never shown again after this.${C_RESET}\n\n"
    printf "  setup_blob: "
    read -r NODE_SETUP_BLOB || NODE_SETUP_BLOB=""
    [ -n "$NODE_SETUP_BLOB" ] || die "No setup_blob given - nothing to provision this node with."
}

generate_env() {
    local env_file="$APP_DIR/.env"
    if [ -f "$env_file" ]; then
        warn ".env already exists - leaving it untouched (port/blob values above still apply to this run)."
        return
    fi
    mkdir -p "$DATA_DIR/certs"
    cat > "$env_file" <<EOF
# Generated by the Rapido-Go Node installer on $(date -u +%Y-%m-%dT%H:%M:%SZ)
LISTEN_PORT=${LISTEN_PORT}
NODE_SETUP_BLOB="${NODE_SETUP_BLOB:-}"
# Bind-mounts the real host directory instead of an opaque Docker volume,
# so this script's own "already provisioned?" check (prompt_setup_blob)
# can see cert.pem directly - see docker-compose.node.yml's own comment.
RAPIDO_GO_NODE_CERTS_DIR="${DATA_DIR}/certs"
EOF
    chmod 600 "$env_file"
    ok "Configuration written to $env_file"
}

install_command() {
    local src="$APP_DIR/rapido-go-node.sh"
    [ -f "$src" ] || src="$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || true)"
    [ -n "$src" ] && [ -f "$src" ] || return 0
    if [ "$(readlink -f "$src")" = "$(readlink -f "$BIN_PATH" 2>/dev/null || echo /nonexistent)" ]; then
        return 0
    fi
    if install -m 755 "$src" "$BIN_PATH" 2>/dev/null; then
        ok "Installed the 'rapido-go-node' command at $BIN_PATH"
    else
        warn "Could not update $BIN_PATH - continuing."
    fi
}

compose() {
    require_installed
    (cd "$APP_DIR" && docker compose -p "$COMPOSE_PROJECT" -f docker-compose.node.yml --env-file .env "$@")
}

open_firewall() {
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "${LISTEN_PORT}/tcp" >/dev/null 2>&1 || true
        ok "Opened port ${LISTEN_PORT} in ufw."
    fi
}

obtain_image() {
    if [ "${RAPIDO_BUILD_LOCALLY:-}" = "1" ]; then
        log "RAPIDO_BUILD_LOCALLY=1, building from source..."
    else
        log "Fetching the Rapido-Go node image..."
        if docker pull "${NODE_IMAGE}:latest" 2>&1 | tail -2; then
            ok "Image ready."
            return 0
        fi
        warn "Could not pull the prebuilt image; building it here instead."
    fi
    log "Building the image (this takes a few minutes)..."
    ( cd "$APP_DIR" && docker build -f docker/Dockerfile.node -t "${NODE_IMAGE}:latest" . )
    docker image inspect "${NODE_IMAGE}:latest" >/dev/null 2>&1 \
        || die "The image was not built - see the output above."
    ok "Image built."
}

follow_logs() {
    [ -t 1 ] || return 0
    [ "${RAPIDO_NO_FOLLOW:-0}" = "1" ] && return 0
    printf "\n${C_DIM}  Following the log - Ctrl-C leaves the node running.${C_RESET}\n\n"
    compose logs -f --tail "${RAPIDO_LOG_TAIL:-40}"
}

cmd_install() {
    require_root
    banner
    ensure_prereqs
    ensure_docker
    ensure_compose
    fetch_source
    prompt_port
    prompt_setup_blob
    generate_env
    save_token
    install_command
    open_firewall
    ensure_registry_login
    obtain_image
    log "Starting..."
    compose up -d
    sleep 5

    if ! compose ps --services --filter status=running 2>/dev/null | grep -q .; then
        err "The container is not running."
        compose logs --tail 30
        exit 1
    fi

    local ip
    ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')"
    printf "\n${C_GREEN}${C_BOLD}Rapido-Go Node is running.${C_RESET}\n\n"
    printf "  If you have not already added it in the panel:\n"
    printf "    Nodes -> Add Node -> Address ${C_BOLD}%s${C_RESET}, Port ${C_BOLD}%s${C_RESET}\n\n" "$ip" "$LISTEN_PORT"
    printf "  ${C_DIM}This port must be reachable from the panel server. Check the\n"
    printf "  panel's Nodes page - status flips to \"Connected\" once this\n"
    printf "  node's first report arrives (a few seconds).${C_RESET}\n"
    follow_logs
}

cmd_up()      { compose up -d && ok "Started."; }
cmd_down()    { compose down && ok "Stopped."; }
cmd_restart() { compose up -d --force-recreate && ok "Restarted."; }
cmd_status()  { compose ps; }

# WireGuard exits are not part of the node itself - the node just binds
# outbounds to whatever interfaces exist via bind_interface. Bringing them
# up by hand fails the same way on every fresh server, so this does it
# correctly:
#   * `DNS =` in a Mullvad-style config makes wg-quick shell out to
#     resolvconf, which fails hard (and deletes the interface it just
#     created) on a host where that is the systemd-resolved shim and the
#     service is not running - and the line does nothing for these tunnels
#     anyway, since the node picks an exit by binding to the interface, not
#     through the system resolver.
#   * without PersistentKeepalive a tunnel only re-handshakes when traffic
#     happens to flow, so a relay that goes away stays "up" and silent.
#   * wg-quick@ is not enabled by default, so a reboot leaves every exit
#     down.
cmd_tunnels() {
    require_root
    local confs
    confs=$(ls /etc/wireguard/*.conf 2>/dev/null) || true
    [ -n "$confs" ] || die "No tunnel configs in /etc/wireguard."

    command -v wg >/dev/null 2>&1 || { log "Installing wireguard-tools..."; pkg_install wireguard-tools; }

    local n
    for f in $confs; do
        n=$(basename "$f" .conf)
        if grep -qE '^DNS' "$f" && ! systemctl is-active --quiet systemd-resolved 2>/dev/null; then
            cp "$f" "${f}.bak-$(date +%Y%m%d_%H%M%S)"
            sed -i 's/^DNS/#DNS/' "$f"
            warn "$n: commented out DNS (systemd-resolved is not running here)"
        fi
        grep -q PersistentKeepalive "$f" || printf 'PersistentKeepalive = 25\n' >> "$f"
        if ip link show "$n" >/dev/null 2>&1; then
            ok "$n: already up"
        elif wg-quick up "$n" >/tmp/wg_$n.log 2>&1; then
            ok "$n: up"
        else
            err "$n: failed"
            tail -4 "/tmp/wg_$n.log" | sed 's/^/      /'
            continue
        fi
        systemctl enable "wg-quick@$n" >/dev/null 2>&1 || true
    done

    printf "\n${C_BOLD}Handshakes${C_RESET}\n"
    local now; now=$(date +%s)
    wg show all dump 2>/dev/null | awk -v now="$now" 'NF>=9 {
        age = ($6 > 0) ? now - $6 : -1
        printf "  %-14s %-24s %s\n", $1, $4, (age < 0 ? "NEVER" : age "s ago")
    }'

    printf "\n${C_BOLD}Where each one comes out${C_RESET}\n"
    for f in $confs; do
        n=$(basename "$f" .conf)
        printf "  %-14s " "$n"
        out=$(curl -fsS --max-time 20 --interface "$n" https://api.ipify.org 2>&1)
        case "$out" in
            *[0-9].[0-9]*) printf "%s\n" "$out" ;;
            *) printf "${C_YELLOW}no answer${C_RESET}\n" ;;
        esac
    done
    printf "\n${C_DIM}  A tunnel that never handshakes usually means the relay is gone.${C_RESET}\n\n"
}

cmd_logs() { compose logs -f --tail "${RAPIDO_LOG_TAIL:-200}"; }

cmd_update() {
    require_root
    require_installed
    fetch_source
    install_command
    ensure_registry_login
    obtain_image
    compose up -d --force-recreate
    ok "Updated."
}

cmd_edit_env() {
    require_installed
    local before after
    before="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1)"
    "${EDITOR:-nano}" "$APP_DIR/.env"
    after="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1)"
    if [ "$before" != "$after" ]; then
        printf "\n"
        warn "Configuration changed. Apply it now? [Y/n] "
        local answer; read -r answer
        case "${answer:-y}" in
            [Nn]*) warn "Not applied. Run 'rapido-go-node restart' when ready." ;;
            *) cmd_restart ;;
        esac
    fi
}

cmd_uninstall() {
    require_root
    require_installed
    warn "This removes the container and the source at $APP_DIR."
    warn "Certificates in $DATA_DIR are KEPT - deleting them would force you to"
    warn "re-add this node in the panel."
    printf "Type 'yes' to continue: "
    local answer; read -r answer
    [ "$answer" = "yes" ] || die "Aborted."
    compose down --remove-orphans || true
    rm -rf "$APP_DIR"
    rm -f "$BIN_PATH"
    ok "Removed. Certificates left in $DATA_DIR"
}

usage() {
    banner
    cat <<EOF
${C_BOLD}Rapido-Go Node${C_RESET} - runs sing-box on a remote server under a Rapido-Go
panel's control.

${C_BOLD}USAGE${C_RESET}
  rapido-go-node <command>

${C_BOLD}SETUP${C_RESET}
  install                  Install Docker if needed, fetch, configure and start
  update                   Fetch the latest source, pull/rebuild and restart
  uninstall                Remove the container and source (certificates kept)

${C_BOLD}RUNNING${C_RESET}
  up | down | restart      Start, stop or restart
  status                   Show what is running
  tunnels                  Bring up /etc/wireguard tunnels, correctly and at boot
  logs                     Follow the logs

${C_BOLD}OTHER${C_RESET}
  edit-env                 Open .env in \$EDITOR

${C_BOLD}ENVIRONMENT${C_RESET}
  LISTEN_PORT               Node control port          (default: 62051)
  NODE_SETUP_BLOB           The one-time blob from the panel's Add Node screen
  RAPIDO_REPO_TOKEN         GitHub token, if the repo is private
  RAPIDO_BUILD_LOCALLY=1    Build the image here instead of pulling from ghcr.io

EOF
}

main() {
    local cmd="${1:-}"
    [ $# -gt 0 ] && shift || true
    case "$cmd" in
        install)   cmd_install ;;
        up)        cmd_up ;;
        down)      cmd_down ;;
        restart)   cmd_restart ;;
        status)    cmd_status ;;
        tunnels)   cmd_tunnels ;;
        logs)      cmd_logs ;;
        update)    cmd_update ;;
        edit-env)  cmd_edit_env ;;
        uninstall) cmd_uninstall ;;
        version|-v|--version) printf "rapido-go-node %s\n" "$RAPIDO_GO_NODE_VERSION" ;;
        ""|help|-h|--help) usage ;;
        *) err "Unknown command: $cmd"; echo; usage; exit 1 ;;
    esac
}

main "$@"
```

</details>

4. Back in the dashboard, the node's status flips to **Connected** once its first push arrives (a few seconds).
5. Create an inbound (e.g. VLESS) and a host under **Hosts** - the node picks up the new config on its next poll (also a few seconds), no restart needed.

#### Updating

```bash
rapido-go update        # panel: backs up the DB, fetches, pulls the newer image, restarts
rapido-go-node update    # node
```

#### Manual install (advanced)

Both CLIs are thin wrappers around plain Docker Compose files - if you'd rather not run a curl-piped script, `git clone` (or download) the repo yourself and drive [`docker-compose.prod.yml`](docker-compose.prod.yml) / [`docker-compose.node.yml`](docker-compose.node.yml) directly, using [`.env.prod.example`](.env.prod.example) / [`.env.node.example`](.env.node.example) and [`Caddyfile.example`](Caddyfile.example) as templates. Everything the CLI scripts do is visible in the two files above - there's nothing they do that isn't also just `docker compose up -d` under the hood.

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
docker-compose.prod.yml, docker-compose.node.yml   production images, driven by the scripts below
rapido-go.sh, rapido-go-node.sh   curl-piped installer/management CLIs, see Quick install
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

یک دستور برای هر سرور - بقیه‌ش (داکر، سورس، ایمیج آماده) رو خود اسکریپت می‌گیره؛ نه `git clone` دستی لازمه نه دستور `docker compose` زدن.

#### ۱. نصب پنل

**ریپو و ایمیج‌هاش خصوصی‌ان**، پس همون اولین fetch هم به یک توکن گیت‌هاب نیاز داره - یک `curl raw.githubusercontent.com` ساده روی ریپوی خصوصی همون اول با 404 مواجه می‌شه، قبل از اینکه اصلاً اسکریپت فرصت اجرا شدن پیدا کنه. از توکنی با اسکوپ `repo` + `read:packages` استفاده کنید (یک fine-grained token محدود به همین ریپو با دسترسی Contents: Read-only و Packages: Read-only هم کار می‌کنه):

```bash
export RAPIDO_REPO_TOKEN=<توکن‌تون>
bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
  -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go.sh?ref=master") install
```

توکن توی `.env` (با دسترسی chmod 600) ذخیره می‌شه تا `rapido-go update` بعدی هم بدون نیاز به دخالت دستی کار کنه - نه توی خود اسکریپت نوشته می‌شه نه توی `.git/config`. (اگه یه روز این ریپو عمومی بشه، شکل ساده و بدون توکن هم کار می‌کنه: `bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install`.)

اگه داکر نصب نباشه نصبش می‌کنه، دامنه‌ای که قراره داشبورد روش بالا بیاد رو می‌پرسه (باید از قبل به همین سرور اشاره کنه - [Caddy](https://caddyserver.com/) با همون دامنه یک گواهی واقعی Let's Encrypt خودکار می‌گیره، بدون نیاز به کار دستی با certbot)، `.env` رو می‌سازه، ایمیج آماده رو از GHCR می‌گیره، Postgres + Redis + migration + هر دو نقش `api` و `backend` رو بالا میاره، و خودش رو هم به‌صورت سراسری با اسم `rapido-go` نصب می‌کنه تا بعداً برای مدیریت ازش استفاده کنید.

در آخر یک **لاگین بوت‌استرپ** یک‌باره چاپ می‌کنه - در ادامه توضیح داده شده.

محتوای کامل `rapido-go.sh` توی بخش انگلیسی بالا (Quick install) قابل مشاهده‌ست.

#### ۲. اولین ورود

نصب یک **لاگین بوت‌استرپ** (`SUDO_USERNAME`/`SUDO_PASSWORD`، ذخیره‌شده توی `.env`) چاپ می‌کنه. این یک ردیف واقعی توی دیتابیس نیست - قبل از اینکه اصلاً جدول `admins` کوئری بشه در حافظه چک می‌شه، دقیقاً به همین دلیل که همیشه یک راه ورود وجود داشته باشه حتی از یک دیتابیس کاملاً خالی. یک‌بار ازش استفاده کنید تا:

1. توی `https://<دامنه‌تون>/dashboard/` لاگین کنید.
2. از صفحه‌ی **Admins** یک ادمین سودوی واقعی بسازید.
3. اختیاری: با `rapido-go edit-env` مقدار `SUDO_PASSWORD` رو عوض کنید، اگه نمی‌خواید این لاگین اضطراری با مقدار اولیه‌ش همچنان فعال بمونه.

#### ۳. افزودن و نصب نود

۱. توی داشبورد، به **Nodes → Add Node** برید، یک اسم و آدرس بدید و ذخیره کنید.
۲. پنل نمایش یک‌باره یک مقدار **setup_blob** (به‌صورت base64) رو نشون می‌ده - این مقدار گواهی نود، کلید خصوصی، CA پنل، و سکرت گزارش‌دهی رو همه با هم بسته‌بندی می‌کنه. همین الان کپی‌ش کنید؛ دیگه هیچ‌وقت نشون داده نمی‌شه.
۳. روی سرور نود (همون توکن ریپوی خصوصیِ پنل - بالا رو ببینید):

```bash
export RAPIDO_REPO_TOKEN=<توکن‌تون>
bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
  -H "Accept: application/vnd.github.raw" \
  "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go-node.sh?ref=master") install
# موقع پرسیدن setup_blob پیستش کنید، بعد یک پورت گوش‌دادن انتخاب کنید (پیش‌فرض ۶۲۰۵۱)
```

یا کاملاً غیرتعاملی:

```bash
export RAPIDO_REPO_TOKEN=<توکن‌تون>
NODE_SETUP_BLOB='<اینجا blob رو پیست کنید>' \
  bash <(curl -fsSL -H "Authorization: token $RAPIDO_REPO_TOKEN" \
    -H "Accept: application/vnd.github.raw" \
    "https://api.github.com/repos/legendary1205/rapido-go/contents/rapido-go-node.sh?ref=master") install
```

محتوای کامل `rapido-go-node.sh` هم توی بخش انگلیسی بالا قابل مشاهده‌ست.

۴. توی داشبورد، وضعیت نود بعد از اولین push (چند ثانیه) به **Connected** تغییر می‌کنه.
۵. یک inbound بسازید (مثلاً VLESS) و یک host زیر **Hosts** - نود توی pull بعدیش (چند ثانیه‌ی دیگه) پیکربندی جدید رو می‌گیره، بدون نیاز به ری‌استارت.

#### آپدیت

```bash
rapido-go update        # پنل: اول از دیتابیس بکاپ می‌گیره، سورس رو می‌گیره، ایمیج جدید رو pull می‌کنه، ری‌استارت می‌کنه
rapido-go-node update    # نود
```

#### نصب دستی (پیشرفته)

هر دو اسکریپت فقط یک لایه‌ی نازک روی فایل‌های Docker Compose معمولی‌ان - اگه ترجیح می‌دید اسکریپت curl-pipe اجرا نکنید، خودتون ریپو رو `git clone` (یا دانلود) کنید و مستقیم [`docker-compose.prod.yml`](docker-compose.prod.yml) / [`docker-compose.node.yml`](docker-compose.node.yml) رو با کمک [`.env.prod.example`](.env.prod.example) / [`.env.node.example`](.env.node.example) و [`Caddyfile.example`](Caddyfile.example) به‌عنوان قالب اجرا کنید. هر کاری که این دو اسکریپت می‌کنن توی همون دو فایل بالا قابل دیدنه - کاری نمی‌کنن که در نهایت همون `docker compose up -d` نباشه.

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
docker-compose.prod.yml, docker-compose.node.yml   ایمیج‌های پروداکشن، به‌وسیله‌ی اسکریپت‌های زیر اجرا می‌شن
rapido-go.sh, rapido-go-node.sh   اسکریپت‌های نصب/مدیریت با curl-pipe، ببینید نصب سریع
.github/workflows/     CI - هر دو ایمیج رو می‌سازه و توی GHCR منتشر می‌کنه
```

### فرمت‌های سابسکریپشن

`GET /sub/:token` فرمت کلاینت رو از روی User-Agent خودکار تشخیص می‌ده؛ `GET /sub/:token/<format>` صریح انتخاب می‌کنه. فرمت‌های پشتیبانی‌شده: **لینک‌های اشتراکی v2ray**، **sing-box**، **Clash**، **Clash Meta**، **Outline** (SIP008 واقعی، شامل همه‌ی هاست‌ها)، **v2ray-json**.

### محدودیت‌های شناخته‌شده

- هنوز پیکربندی مستقل به‌ازای هر نود وجود نداره - همه‌ی نودهای یک فلیت دقیقاً یک پیکربندی یکسان اجرا می‌کنن (با معماری فعلی پنل هم‌خوانه، نه یک عقب‌گرد).
- کنسول تعاملی بات تلگرام سیستم قدیمی (ساخت/تعلیق/مدیریت گروهی کاربر از طریق چت) پورت نشده - فقط نوتیفیکیشن خروجی وجود داره. داشبورد جایگزین در نظر گرفته‌شده است.
- شمارش دقیق بایت ترافیک فعلاً فقط برای VLESS هست (پروتکلی که فورک آپدیت زنده دورش پیچیده شده)؛ بقیه‌ی پروتکل‌ها از طریق sing-box دست‌نخورده مسیر می‌شن.
