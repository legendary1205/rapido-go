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
# These hold REAL escape bytes, not the literal text \033[... - printf would
# expand either form, but usage() draws its box with plain string
# arguments, and anything that is not printf (cat, echo without -e)
# prints a backslash-033 literally instead of colouring it. Producing
# the byte once, here, means every caller gets colour however it prints.
if [ -t 1 ]; then
    C_RESET=$(printf '\033[0m');   C_DIM=$(printf '\033[2m');    C_BOLD=$(printf '\033[1m')
    C_RED=$(printf '\033[0;31m');  C_GREEN=$(printf '\033[0;32m'); C_YELLOW=$(printf '\033[0;33m')
    C_BLUE=$(printf '\033[1;34m'); C_CYAN=$(printf '\033[0;36m')
else
    C_RESET=''; C_DIM=''; C_BOLD=''; C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''; C_CYAN=''
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
        GIT_TERMINAL_PROMPT=0 git -C "$APP_DIR" -c credential.helper= fetch --depth 1 origin "$REPO_BRANCH" \
            || { scrub_remote; die "Could not fetch the repository. If it is private, run: RAPIDO_REPO_TOKEN=<token> rapido-go update"; }
        git -C "$APP_DIR" reset --hard "origin/$REPO_BRANCH" >/dev/null
        scrub_remote
    else
        log "Downloading Rapido-Go into $APP_DIR..."
        mkdir -p "$(dirname "$APP_DIR")"
        # GIT_TERMINAL_PROMPT=0: a missing/wrong token on a private repo must
        # fail with the die() message below, not hang forever on a credential
        # prompt with no terminal attached to answer it (this is exactly what
        # happens with no token at all - found by actually running an install
        # non-interactively over SSH, not assumed).
        GIT_TERMINAL_PROMPT=0 git -c credential.helper= clone --depth 1 --branch "$REPO_BRANCH" \
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
    # cmd_install's own call arrives with RAPIDO_DOMAIN already set by
    # prompt_domain earlier in the same run, but cmd_update never prompts
    # for it at all - load it from .env here too (same idiom as
    # load_saved_token) so `rapido-go update` doesn't crash under set -u
    # on a plain unset-variable reference the moment it reaches the
    # https:// check below. Found by actually running `rapido-go update`
    # non-interactively, not assumed.
    if [ -z "${RAPIDO_DOMAIN:-}" ] && [ -f "$APP_DIR/.env" ]; then
        RAPIDO_DOMAIN="$(grep -E '^RAPIDO_DOMAIN=' "$APP_DIR/.env" 2>/dev/null \
            | head -1 | cut -d= -f2- | tr -d '"')"
    fi
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

# ── help screen ──────────────────────────────────────────────────────────────
# The box is drawn at a fixed 78 columns so the vertical rule stays straight
# down the whole command table. Three numbers hold it together and must move
# together if any of them changes: the left field is 25 wide, the right one
# 48, and the borders below carry 27 and 50 dashes either side of their
# junction. They are generated from those numbers, not counted by hand -
# an earlier version had 80-wide borders around 78-wide rows because the
# dashes were typed out and miscounted.
#
# Every cell is passed to printf as an ARGUMENT, never as part of the format
# string - one description contains a literal % (date +%F) that printf would
# otherwise try to expand.
_u_top()  { printf "${C_DIM}╭──────────────────────────────────────────────────────────────────────────────╮${C_RESET}\n"; }
_u_mid()  { printf "${C_DIM}├───────────────────────────┬──────────────────────────────────────────────────┤${C_RESET}\n"; }
_u_join() { printf "${C_DIM}├───────────────────────────┴──────────────────────────────────────────────────┤${C_RESET}\n"; }
_u_end()  { printf "${C_DIM}╰──────────────────────────────────────────────────────────────────────────────╯${C_RESET}\n"; }

# A full-width line (header and examples, where there is no second column).
_u_wide() { printf "${C_DIM}│${C_RESET}  %-74s  ${C_DIM}│${C_RESET}\n" "$1"; }
_u_blue() { printf "${C_DIM}│${C_RESET}  ${C_BLUE}%-74s${C_RESET}  ${C_DIM}│${C_RESET}\n" "$1"; }

# A section heading: bold, left of the rule, nothing on the right.
_u_head() { printf "${C_DIM}│${C_RESET}  ${C_BOLD}%-25s${C_RESET}${C_DIM}│${C_RESET} %-48s ${C_DIM}│${C_RESET}\n" "$1" ""; }

# A command row: the command itself in blue, what it does in yellow.
_u_row()  { printf "${C_DIM}│${C_RESET}  ${C_BLUE}%-25s${C_RESET}${C_DIM}│${C_RESET} ${C_YELLOW}%-48s${C_RESET} ${C_DIM}│${C_RESET}\n" "  $1" "$2"; }

# An empty row - keeps the vertical rule unbroken between sections.
_u_gap()  { printf "${C_DIM}│${C_RESET}  %-25s${C_DIM}│${C_RESET} %-48s ${C_DIM}│${C_RESET}\n" "" ""; }

usage() {
    banner
    _u_top
    _u_wide "Rapido-Go $RAPIDO_GO_VERSION"
    _u_wide "A self-hosted proxy panel: users, resellers, nodes and"
    _u_wide "subscriptions, with per-user traffic accounting."
    _u_wide ""
    _u_wide "Usage:  rapido-go <command> [arguments]"
    _u_mid
    _u_head "SETUP"
    _u_row  "install"   "Install Docker if needed, fetch and start"
    _u_row  "update"    "Back up, fetch latest source, rebuild, restart"
    _u_row  "uninstall" "Remove containers and source (backups kept)"
    _u_gap

    _u_head "RUNNING"
    _u_row  "up | down | restart" "Start, stop or restart the stack"
    _u_row  "status"              "Show what is running"
    _u_row  "logs [service]"      "Follow all logs, or one service"
    _u_gap

    _u_head "DATA"
    _u_row  "backup [file]"  "Dump the database to a .sql.gz file"
    _u_row  "restore <file>" "Replace the database from a dump (asks first)"
    _u_gap

    _u_head "ADMIN"
    _u_row  "edit-env" "Open .env in \$EDITOR"
    _u_row  "version"  "Show the installed version"
    _u_gap

    _u_head "ENVIRONMENT"
    _u_row  "RAPIDO_GO_APP_DIR"      "Where the source lives (/opt/rapido-go)"
    _u_row  "RAPIDO_GO_DATA_DIR"     "Where backups live (/var/lib/rapido-go)"
    _u_row  "RAPIDO_DOMAIN"          "Panel domain, to skip the prompt"
    _u_row  "RAPIDO_NO_FOLLOW=1"     "Do not tail the log after install/restart"
    _u_row  "RAPIDO_REPO_TOKEN"      "GitHub token, if the repo is private"
    _u_row  "RAPIDO_REPO_BRANCH"     "Branch to install from (default: master)"
    _u_row  "RAPIDO_BUILD_LOCALLY=1" "Build here instead of pulling from ghcr.io"
    _u_join

    _u_wide "EXAMPLES"
    _u_blue "  rapido-go install"
    _u_blue "  RAPIDO_DOMAIN=panel.example.com rapido-go install"
    _u_blue "  rapido-go logs panel"
    _u_blue "  rapido-go backup /root/rapido-go-\$(date +%F).sql.gz"
    _u_end
    printf "\n"
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
