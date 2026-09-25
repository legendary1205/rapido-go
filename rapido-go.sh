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
#  Install (fresh server, as root):
#    bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go.sh) install --domain panel.example.com
#
#  Afterwards the command is available system-wide as `rapido-go`.
#
#  A new install downloads only what it runs (the compose file, this script
#  and two example files) - it does NOT clone the repository. An install made
#  by an older version of this script is a git clone and keeps updating with
#  git; `rapido-go update` handles both layouts.
#
#  This script only ever touches Rapido-Go itself - its files, its .env,
#  its Caddyfile and its containers. It never contains, prompts for or
#  generates any third-party integration secret.
#
set -euo pipefail

RAPIDO_GO_VERSION="1.1.0"

# Only needed when the repository or its ghcr.io images are PRIVATE. When both
# are public no token is asked for, sent or stored. Never hard-code one here;
# pass it for a single command so it never lands on disk unless it is needed
# for updates:
#     RAPIDO_REPO_TOKEN=github_pat_xxx bash rapido-go.sh install
RAPIDO_REPO_TOKEN="${RAPIDO_REPO_TOKEN:-}"

# Install inputs. Each can come from a flag (see parse_install_args) or from
# the environment variable of the same name; whatever is still empty after
# that is asked for - but only when a terminal is attached.
RAPIDO_DOMAIN="${RAPIDO_DOMAIN:-}"
RAPIDO_EXTRA_DOMAINS="${RAPIDO_EXTRA_DOMAINS:-}"
RAPIDO_SUB_DOMAIN="${RAPIDO_SUB_DOMAIN:-}"
RAPIDO_ADMIN_USER="${RAPIDO_ADMIN_USER:-}"
RAPIDO_ADMIN_PASS="${RAPIDO_ADMIN_PASS:-}"
RAPIDO_YES="${RAPIDO_YES:-0}"
RAPIDO_SKIP_PREFLIGHT="${RAPIDO_SKIP_PREFLIGHT:-0}"

# Set by resolve_repo_access: 0 when the repository AND the images can be read
# without a token (so none is used or stored), 1 otherwise.
TOKEN_NEEDED=1

# Reads one KEY from an env file (default: the install's .env), quotes and CRs
# stripped, last definition wins (the same rule compose applies). Prints the
# default when the key or the file is absent.
env_get() {
    local key="$1" default="${2:-}" file="${3:-$APP_DIR/.env}" val=""
    if [ -f "$file" ]; then
        # `|| true` is load-bearing (see load_saved_token below): a key that
        # is simply not there must give the default, not kill the script.
        val="$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- | tr -d "\"'\r" || true)"
    fi
    printf '%s' "${val:-$default}"
}

# If the token was not passed in, take the one the install saved. Without
# this every `rapido-go update` on a private repository stops at a git
# username prompt, and an update that cannot run unattended is an update
# that does not happen.
load_saved_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] && return 0
    # resolve_repo_access found everything public and dropped the token on
    # purpose - re-reading a stale saved one here would undo that.
    [ "${TOKEN_NEEDED:-1}" = "0" ] && return 0
    [ -f "$APP_DIR/.env" ] || return 0
    # `|| true` is load-bearing, not defensive noise: with `pipefail`, a
    # grep that simply finds nothing (the normal case - no token on a
    # public install) fails the whole pipeline, the assignment inherits
    # that status, and `set -e` kills the installer mid-run, silently.
    RAPIDO_REPO_TOKEN="$(grep -E '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" 2>/dev/null \
        | head -1 | cut -d= -f2- | tr -d '"' | tr -d "'" || true)"
}

save_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] || return 0
    [ -f "$APP_DIR/.env" ] || return 0
    # Public repository and public images: nothing to update with, so no
    # secret is left on disk.
    if [ "${TOKEN_NEEDED:-1}" = "0" ]; then return 0; fi
    if grep -qE '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env"; then
        # A newer token replaces an expired one - otherwise passing a fresh
        # token to `rapido-go update` would silently keep the dead one.
        if [ "$(env_get RAPIDO_REPO_TOKEN)" = "$RAPIDO_REPO_TOKEN" ]; then return 0; fi
        local tmp
        tmp="$(mktemp "$APP_DIR/.env.XXXXXX")"
        grep -vE '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" > "$tmp" || true
        printf 'RAPIDO_REPO_TOKEN="%s"\n' "$RAPIDO_REPO_TOKEN" >> "$tmp"
        chmod 600 "$tmp"
        mv -f "$tmp" "$APP_DIR/.env"
        return 0
    fi
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

# The files a lightweight (non-git) install keeps in $APP_DIR.
LITE_FILES="docker-compose.prod.yml rapido-go.sh Caddyfile.example .env.prod.example"

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
note() { printf "${C_DIM}  %s${C_RESET}\n" "$*"; }
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

# A terminal to ask on. With no terminal `read` returns instantly on EOF and a
# prompt loop would spin forever - every prompt below checks this first and
# otherwise says which flag to pass instead.
have_tty() { [ -t 0 ]; }
is_yes()   { [ "${RAPIDO_YES:-0}" = "1" ]; }

# confirm <question> [y|n]: asks only with a terminal and without --yes;
# otherwise answers with the default. Returns 0 for yes.
confirm() {
    local q="$1" def="${2:-y}" ans="" hint="Y/n"
    if [ "$def" != "y" ]; then hint="y/N"; fi
    if is_yes || ! have_tty; then
        if [ "$def" = "y" ]; then return 0; else return 1; fi
    fi
    printf "  %s [%s] " "$q" "$hint"
    read -r ans || ans=""
    case "${ans:-$def}" in
        [Yy]*) return 0 ;;
        *)     return 1 ;;
    esac
}

# ── prerequisites ────────────────────────────────────────────────────────────
detect_pkg_manager() {
    local pm
    for pm in apt-get dnf yum; do
        if command -v "$pm" >/dev/null 2>&1; then printf '%s' "$pm"; return 0; fi
    done
    return 0
}

pkg_install() {
    if   command -v apt-get >/dev/null 2>&1; then
        # Try the install as it stands: `apt-get update` costs 10-60 seconds
        # and is only needed when the package lists are stale or missing,
        # which is exactly when this first attempt fails. A failing update
        # (one stale third-party list is enough) must not abort the install -
        # only the install step itself matters.
        if ! DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" >/dev/null 2>&1; then
            apt-get update -y >/dev/null 2>&1 || true
            DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" >/dev/null || die "Could not install: $*"
        fi
    elif command -v dnf     >/dev/null 2>&1; then dnf install -y "$@" >/dev/null || die "Could not install: $*"
    elif command -v yum     >/dev/null 2>&1; then yum install -y "$@" >/dev/null || die "Could not install: $*"
    else die "No supported package manager found (need apt-get, dnf or yum)."
    fi
}

# git only when this install is (or will be) a git checkout - a normal new
# install needs curl and nothing else.
ensure_prereqs() {
    local missing=() c
    for c in curl; do command -v "$c" >/dev/null 2>&1 || missing+=("$c"); done
    if [ "$(detect_install_mode)" = "git" ]; then
        command -v git >/dev/null 2>&1 || missing+=("git")
    fi
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
    command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1
}

# Kept out of docker_ready deliberately: a healthy daemon with no compose
# plugin used to fail that same check, so the installer sent the operator
# off to debug a daemon that every suggested command reports as running.
# The plugin is its own package and its own fix.
ensure_compose() {
    docker compose version >/dev/null 2>&1 && return 0
    log "Installing the Docker Compose plugin..."
    pkg_install docker-compose-plugin >/dev/null 2>&1 || true
    docker compose version >/dev/null 2>&1 \
        || die "Docker is running but the 'docker compose' plugin is missing and could not be installed (try: apt-get install docker-compose-plugin)."
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
    curl -fsSL https://get.docker.com -o /tmp/get-docker.sh \
        || die "Could not download the Docker installer - check this server's outbound network."
    sh /tmp/get-docker.sh >/dev/null || die "The Docker installer failed - see the output above."
    rm -f /tmp/get-docker.sh
    systemctl enable --now docker >/dev/null 2>&1 || true
    sleep 3
    docker_ready || die "Docker installed but the daemon is not responding. Check: journalctl -u docker -n 30"
    ok "Docker installed and running."
}

# GHCR images can stay private even though the pull itself is a plain
# `docker pull` - the same token that reads the source also has
# read:packages on it, so log in once here rather than making the operator
# manage a second credential. Skipped when everything is public.
ensure_registry_login() {
    load_saved_token
    [ "${TOKEN_NEEDED:-1}" = "0" ] && return 0
    [ -n "$RAPIDO_REPO_TOKEN" ] || return 0
    echo "$RAPIDO_REPO_TOKEN" | docker login ghcr.io -u "$REPO_OWNER" --password-stdin >/dev/null 2>&1 \
        && ok "Logged in to ghcr.io." \
        || warn "Could not log in to ghcr.io with the given token - continuing (the image may still be reachable)."
}

# ── GitHub access ────────────────────────────────────────────────────────────
# Runs curl with an optional GitHub token, handed over as a curl config on a
# pipe (`-K -`) - never as an argument, which any local user could read from
# the process list. Anything that is not a token character is dropped first,
# so a stray quote in a hand-edited .env cannot alter the config.
# _curl_gh <token-or-empty> <curl args...>
_curl_gh() {
    local tok
    tok="$(printf '%s' "$1" | tr -cd 'A-Za-z0-9_')"; shift
    if [ -n "$tok" ]; then
        printf 'header = "Authorization: Bearer %s"\n' "$tok" | curl -K - "$@"
    else
        curl "$@"
    fi
}

# HTTP status of one api.github.com request (000 when it cannot be reached).
# github_status <token-or-empty> <api path>
github_status() {
    local code
    code="$(_curl_gh "$1" -s -o /dev/null -w '%{http_code}' --max-time 10 "https://api.github.com/$2" || true)"
    printf '%s' "${code:-000}"
}

# public | private | unknown: can the panel image be pulled with no login?
# Asks ghcr.io for an anonymous pull token and then for the manifest - the
# same two requests `docker pull` would make.
ghcr_image_visibility() {
    local ref="${RAPIDO_IMAGE:-$PANEL_IMAGE}" repo tok code
    case "$ref" in
        ghcr.io/*) repo="${ref#ghcr.io/}" ;;
        *) printf 'unknown'; return 0 ;;
    esac
    tok="$(curl -fsS --max-time 8 "https://ghcr.io/token?service=ghcr.io&scope=repository:${repo}:pull" 2>/dev/null \
        | sed -n 's/.*"token":"\([^"]*\)".*/\1/p' || true)"
    if [ -z "$tok" ]; then printf 'unknown'; return 0; fi
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 \
        -H "Authorization: Bearer $tok" \
        -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
        "https://ghcr.io/v2/${repo}/manifests/${RAPIDO_TAG:-latest}" || true)"
    case "$code" in
        200) printf 'public' ;;
        401|403|404) printf 'private' ;;
        *) printf 'unknown' ;;
    esac
}

_valid_token() { case "${1:-}" in ''|*[!A-Za-z0-9_]*) return 1 ;; *) return 0 ;; esac; }

# Asks once for a token, hidden. Only with a terminal.
prompt_token() {
    have_tty || die "The repository or its images are private and no terminal is attached to ask on - pass a token: RAPIDO_REPO_TOKEN=<token> rapido-go install (or --token)."
    local t="" tries=0
    printf "\n${C_BOLD}GitHub access token${C_RESET}\n"
    printf "${C_DIM}  The repository or its images are private. Paste a token that can read\n"
    printf "  them (classic: repo + read:packages). Input is hidden and the token is\n"
    printf "  stored only in %s/.env (mode 600), for updates.${C_RESET}\n\n" "$APP_DIR"
    while [ "$tries" -lt 3 ]; do
        printf "  Token: "
        read -rs t || t=""
        printf "\n"
        t="$(printf '%s' "$t" | tr -d '[:space:]')"
        if _valid_token "$t"; then RAPIDO_REPO_TOKEN="$t"; return 0; fi
        warn "  That does not look like a GitHub token."
        tries=$((tries + 1))
    done
    die "No valid token given."
}

# Decides whether a token is needed at all, asks for one only if so, and
# checks it before anything is installed. mode: strict (install: prompt/die)
# or lenient (update: never prompt, never die - the later steps report).
resolve_repo_access() {
    local mode="${1:-strict}" repo_code img no_token=0
    TOKEN_NEEDED=1
    load_saved_token
    RAPIDO_REPO_TOKEN="$(printf '%s' "$RAPIDO_REPO_TOKEN" | tr -d '[:space:]')"

    # Repository: 200 = public, 401/404 = private (GitHub answers 404 for a
    # private repository it will not admit exists), anything else (rate
    # limit, no route to api.github.com) = cannot tell.
    repo_code="$(github_status "" "repos/${REPO_OWNER}/${REPO_NAME}")"
    img="unknown"
    case "$repo_code" in 401|404) : ;; *) img="$(ghcr_image_visibility)" ;; esac

    # No token is needed when the repository is public AND the image is not
    # private. Then no credential is used at all - a stale token would even do
    # harm, GitHub answers 401 to a bad token on a public resource. Where a
    # check could not be made (rate limit, ghcr.io unreachable) a token that
    # is already in hand is kept rather than dropped on a guess.
    if [ "$img" != "private" ]; then
        if [ "$repo_code" = "200" ]; then
            if [ "$img" = "public" ] || [ -z "$RAPIDO_REPO_TOKEN" ]; then no_token=1; fi
        elif [ "$repo_code" != "401" ] && [ "$repo_code" != "404" ] && [ -z "$RAPIDO_REPO_TOKEN" ]; then
            no_token=1
        fi
    fi
    if [ "$no_token" = "1" ]; then
        TOKEN_NEEDED=0
        if [ -n "$RAPIDO_REPO_TOKEN" ]; then
            note "The repository and images are public - the token is not needed and will not be stored."
        fi
        RAPIDO_REPO_TOKEN=""
        if [ "$repo_code" = "200" ]; then
            ok "Repository and images are publicly readable - no token needed."
        else
            warn "Could not check repository access (GitHub answered $repo_code) - assuming it is public."
        fi
        return 0
    fi

    if [ -z "$RAPIDO_REPO_TOKEN" ]; then
        if [ "$mode" = "strict" ]; then prompt_token; else return 0; fi
    fi

    # A token is in hand: prove it can read the repository before going on.
    repo_code="$(github_status "$RAPIDO_REPO_TOKEN" "repos/${REPO_OWNER}/${REPO_NAME}")"
    case "$repo_code" in
        200) ok "GitHub token accepted." ;;
        401) if [ "$mode" = "strict" ]; then die "GitHub rejected the token (expired, revoked or mistyped)."; fi
             warn "GitHub rejected the saved token - pass a new one: RAPIDO_REPO_TOKEN=<token> rapido-go update" ;;
        000) warn "Could not reach api.github.com to check the token - continuing." ;;
        *)   if [ "$mode" = "strict" ]; then die "That token cannot read ${REPO_OWNER}/${REPO_NAME} (GitHub answered $repo_code). It needs read access to the repository."; fi
             warn "The saved token cannot read ${REPO_OWNER}/${REPO_NAME} (GitHub answered $repo_code)." ;;
    esac
}

# ── source ───────────────────────────────────────────────────────────────────
# git: a clone (every install made by earlier versions of this script, and any
# install that builds the image locally). lite: just the files in LITE_FILES.
detect_install_mode() {
    if [ -d "$APP_DIR/.git" ]; then printf 'git'
    elif [ "${RAPIDO_BUILD_LOCALLY:-}" = "1" ] || [ "${RAPIDO_INSTALL_MODE:-}" = "git" ]; then printf 'git'
    else printf 'lite'
    fi
}

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

file_sum() { sha256sum "$1" 2>/dev/null | cut -d' ' -f1 || true; }

# One file from the repository at $REPO_BRANCH. Public: the raw host (no
# per-IP API rate limit). With a token: the contents API with the raw media
# type - the documented route that works for every kind of token, and the
# same one the README uses for a private repository.
fetch_repo_file() {
    local path="$1" dest="$2" api raw
    api="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/contents/${path}?ref=${REPO_BRANCH}"
    raw="https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/${REPO_BRANCH}/${path}"
    if [ -z "$RAPIDO_REPO_TOKEN" ]; then
        if curl -fsSL --retry 2 --retry-delay 1 --max-time 60 "$raw" -o "$dest" 2>/dev/null; then return 0; fi
    fi
    _curl_gh "$RAPIDO_REPO_TOKEN" -fsSL --retry 2 --retry-delay 1 --max-time 60 \
        -H 'Accept: application/vnd.github.raw' "$api" -o "$dest"
}

fetch_source_lite() {
    local tmp f pid pids="" failed=0 sha=""
    log "Downloading Rapido-Go ($REPO_BRANCH) into $APP_DIR..."
    mkdir -p "$APP_DIR"
    tmp="$(mktemp -d "$APP_DIR/.fetch.XXXXXX")"
    # All four files at once - they are small, so this is one round trip.
    for f in $LITE_FILES; do
        fetch_repo_file "$f" "$tmp/$f" &
        pids="$pids $!"
    done
    for pid in $pids; do
        wait "$pid" || failed=1
    done
    if [ "$failed" -ne 0 ]; then
        rm -rf "$tmp"
        die "Could not download the Rapido-Go files from GitHub (the reason is above). If the repository is private, pass a token: RAPIDO_REPO_TOKEN=<token>"
    fi
    # Never move a truncated or error-page download over a working install.
    if ! grep -q '^services:' "$tmp/docker-compose.prod.yml" || ! bash -n "$tmp/rapido-go.sh" 2>/dev/null; then
        rm -rf "$tmp"
        die "The downloaded files are not valid (a proxy or captive portal answering instead of GitHub?). Nothing was changed."
    fi
    for f in $LITE_FILES; do
        mv -f "$tmp/$f" "$APP_DIR/$f"
    done
    chmod +x "$APP_DIR/rapido-go.sh"
    rm -rf "$tmp"
    mkdir -p "$APP_DIR/templates"
    # Best effort: remember which commit these files are, for `rapido-go version`.
    sha="$(_curl_gh "$RAPIDO_REPO_TOKEN" -fsS --max-time 6 -H 'Accept: application/vnd.github.sha' \
        "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/commits/${REPO_BRANCH}" 2>/dev/null | cut -c1-7 || true)"
    case "$sha" in
        ''|*[!0-9a-f]*) rm -f "$APP_DIR/.source-version" ;;
        *) printf '%s\n' "$sha" > "$APP_DIR/.source-version" ;;
    esac
    ok "Files ready${sha:+ ($sha)}."
}

fetch_source_git() {
    if [ -d "$APP_DIR/.git" ]; then
        log "Updating source in $APP_DIR..."
        git -C "$APP_DIR" remote set-url origin "$(repo_url)"
        GIT_TERMINAL_PROMPT=0 git -C "$APP_DIR" -c credential.helper= fetch --depth 1 origin "$REPO_BRANCH" \
            || { scrub_remote; die "Could not fetch the repository. If it is private, run: RAPIDO_REPO_TOKEN=<token> rapido-go update"; }
        git -C "$APP_DIR" reset --hard "origin/$REPO_BRANCH" >/dev/null
        scrub_remote
    elif [ -d "$APP_DIR" ] && [ -n "$(ls -A "$APP_DIR" 2>/dev/null || true)" ]; then
        # The directory already holds files (a lightweight install being
        # switched to a full checkout to build locally, or an interrupted
        # install) - `git clone` refuses a non-empty directory, so attach the
        # repository to it instead. .env and Caddyfile are not tracked and stay.
        log "Fetching the full source into $APP_DIR..."
        git -C "$APP_DIR" init -q
        git -C "$APP_DIR" remote add origin "$(repo_url)" 2>/dev/null \
            || git -C "$APP_DIR" remote set-url origin "$(repo_url)"
        GIT_TERMINAL_PROMPT=0 git -C "$APP_DIR" -c credential.helper= fetch --depth 1 origin "$REPO_BRANCH" \
            || { scrub_remote; die "Could not fetch the repository. If it is private, pass a token: RAPIDO_REPO_TOKEN=<token>"; }
        git -C "$APP_DIR" checkout -q -f -B "$REPO_BRANCH" FETCH_HEAD
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
            "$(repo_url)" "$APP_DIR" >/dev/null \
            || die "Could not clone the repository (git's own reason is above). If $APP_DIR already exists from an interrupted install, remove it and retry."
        scrub_remote
    fi
    ok "Source ready ($(git -C "$APP_DIR" rev-parse --short HEAD))."
}

fetch_source() {
    load_saved_token
    if [ "$(detect_install_mode)" = "git" ]; then fetch_source_git; else fetch_source_lite; fi
}

# What is installed, as a short string: a git sha, a lite install's recorded
# sha, or "dev".
source_version() {
    if [ -d "$APP_DIR/.git" ]; then
        git -C "$APP_DIR" rev-parse --short HEAD 2>/dev/null || printf 'dev'
    elif [ -f "$APP_DIR/.source-version" ]; then
        head -n 1 "$APP_DIR/.source-version" 2>/dev/null || printf 'dev'
    else
        printf 'dev'
    fi
}

# ── inputs ───────────────────────────────────────────────────────────────────
random_secret() {
    if command -v openssl >/dev/null 2>&1; then openssl rand -hex 16
    else head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'
    fi
}

# 22 letters and digits (about 130 bits). tr/cut read their whole input, so no
# early-exiting reader can hand the pipeline a SIGPIPE under pipefail.
gen_password() {
    local p=""
    if command -v openssl >/dev/null 2>&1; then
        p="$(openssl rand -base64 96 | tr -dc 'A-Za-z0-9' | cut -c1-22)"
    else
        p="$(head -c 96 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-22)"
    fi
    [ "${#p}" -ge 16 ] || die "Could not generate a password (no source of randomness?)."
    printf '%s' "$p"
}

# The panel domain matters more than it looks: it is what goes into every
# subscriber's subscription link. Without it the panel has no TLS in front
# of it at all - the Go binary itself has no built-in HTTPS listener - and
# Caddy (below) has nothing to request a certificate for.
_clean_host() { printf '%s' "$1" | tr -d ' ' | tr 'A-Z' 'a-z' | sed -e 's#^https\?://##' -e 's#/.*$##' -e 's#:.*$##'; }
_valid_host() {
    # A bare IP passes the label pattern (digits are alphanumeric) but can
    # never get a certificate - a numeric last label is never a real TLD.
    printf '%s' "$1" | grep -qE '^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$' \
        && ! printf '%s' "$1" | grep -qE '\.[0-9]+$'
}

# "A.example.com, b.example.com ,a.example.com" -> "a.example.com,b.example.com"
# (cleaned, validated, lowercase, de-duplicated). Returns 1 on a bad name.
normalize_host_list() {
    local raw="${1:-}" out="" h
    local IFS=,
    for h in $raw; do
        h="$(_clean_host "$h")"
        [ -n "$h" ] || continue
        if ! _valid_host "$h"; then
            err "'$h' does not look like a hostname."
            return 1
        fi
        case ",$out," in *",$h,"*) continue ;; esac
        out="${out:+$out,}$h"
    done
    printf '%s' "$out"
}

# Items of list A that are not in list B (both comma-separated).
list_minus() {
    local a="${1:-}" b="${2:-}" out="" h
    local IFS=,
    for h in $a; do
        case ",$b," in *",$h,"*) continue ;; esac
        out="${out:+$out,}$h"
    done
    printf '%s' "$out"
}

split_list() { printf '%s' "${1:-}" | tr ',' ' '; }

# Every name this install serves, panel names first.
all_domains() {
    local out="$RAPIDO_DOMAIN"
    [ -z "$RAPIDO_EXTRA_DOMAINS" ] || out="$out,$RAPIDO_EXTRA_DOMAINS"
    [ -z "$RAPIDO_SUB_DOMAIN" ] || out="$out,$RAPIDO_SUB_DOMAIN"
    printf '%s' "$out"
}

_valid_admin_user() { printf '%s' "${1:-}" | grep -qE '^[A-Za-z0-9_.-]{1,64}$'; }
# The password ends up single-quoted in .env (so `$` is not interpolated by
# compose), which is why a single quote is the one character refused.
_valid_admin_pass() {
    local p="${1:-}"
    [ "${#p}" -ge 8 ] && [ "${#p}" -le 128 ] || return 1
    case "$p" in *"'"*|*[[:cntrl:]]*) return 1 ;; esac
    return 0
}

# After flags/env and .env are merged: clean, validate and de-duplicate.
finalize_domains() {
    RAPIDO_DOMAIN="$(_clean_host "$RAPIDO_DOMAIN")"
    if ! _valid_host "$RAPIDO_DOMAIN"; then
        die "'$RAPIDO_DOMAIN' does not look like a hostname (a domain is required - Rapido-Go does not install on a bare IP)."
    fi
    RAPIDO_EXTRA_DOMAINS="$(normalize_host_list "$RAPIDO_EXTRA_DOMAINS")"
    RAPIDO_SUB_DOMAIN="$(normalize_host_list "$RAPIDO_SUB_DOMAIN")"
    # A name can only have one site block; the panel block already serves /sub.
    RAPIDO_EXTRA_DOMAINS="$(list_minus "$RAPIDO_EXTRA_DOMAINS" "$RAPIDO_DOMAIN")"
    RAPIDO_SUB_DOMAIN="$(list_minus "$RAPIDO_SUB_DOMAIN" "$RAPIDO_DOMAIN${RAPIDO_EXTRA_DOMAINS:+,$RAPIDO_EXTRA_DOMAINS}")"
}

prompt_domain() {
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
}

prompt_sub_domain() {
    local ans=""
    printf "\n${C_BOLD}Subscription domain (optional)${C_RESET}\n"
    printf "${C_DIM}  A separate name for subscription links only - the dashboard is not\n"
    printf "  reachable through it. Press Enter to skip.${C_RESET}\n\n"
    printf "  Subscription domain: "
    read -r ans || ans=""
    RAPIDO_SUB_DOMAIN="$ans"
}

# Merges flags/env with what an earlier install left in .env, asks for what
# is still missing (terminal only) and validates it all. Sets the RAPIDO_*
# variables above; ADMIN_PASSWORD only when this run generates or is given
# one (it is shown once, at the end).
resolve_inputs() {
    local env_file="$APP_DIR/.env" have_env=0 asked=0
    if [ -f "$env_file" ]; then have_env=1; fi

    if [ "$have_env" = "1" ]; then
        if [ -z "$RAPIDO_DOMAIN" ]; then
            RAPIDO_DOMAIN="$(env_get RAPIDO_DOMAIN)"
            if [ -n "$RAPIDO_DOMAIN" ]; then ok "Using the domain already in $env_file: $RAPIDO_DOMAIN"; fi
        fi
        if [ -z "$RAPIDO_EXTRA_DOMAINS" ]; then RAPIDO_EXTRA_DOMAINS="$(env_get RAPIDO_EXTRA_DOMAINS)"; fi
        if [ -z "$RAPIDO_SUB_DOMAIN" ]; then RAPIDO_SUB_DOMAIN="$(env_get RAPIDO_SUB_DOMAIN)"; fi
    fi

    if [ -z "$RAPIDO_DOMAIN" ]; then
        # With no terminal, `read` returns instantly on EOF and the variable
        # stays empty - a prompt loop would spin forever printing the prompt.
        # A non-interactive install must say what to do, not hang.
        have_tty || die "No terminal to ask on - pass the domain instead: rapido-go install --domain panel.example.com (or RAPIDO_DOMAIN=...)"
        prompt_domain
        asked=1
    fi
    if [ "$have_env" = "0" ] && [ "$asked" = "1" ] && [ -z "$RAPIDO_SUB_DOMAIN" ] && ! is_yes && have_tty; then
        prompt_sub_domain
    fi
    finalize_domains
    ok "Panel domain: $RAPIDO_DOMAIN${RAPIDO_EXTRA_DOMAINS:+ (also $RAPIDO_EXTRA_DOMAINS)}"
    if [ -n "$RAPIDO_SUB_DOMAIN" ]; then ok "Subscription domain: $RAPIDO_SUB_DOMAIN"; fi

    # An existing .env is never rewritten - say so when a flag asks for
    # something it does not hold, instead of silently ignoring the flag.
    if [ "$have_env" = "1" ]; then
        if [ "$(env_get RAPIDO_DOMAIN)" != "$RAPIDO_DOMAIN" ]; then
            warn "$env_file already exists and names a different domain ($(env_get RAPIDO_DOMAIN)) - it is left as is (rapido-go edit-env to change it). Only the Caddyfile follows this run."
        fi
        if [ -n "$RAPIDO_ADMIN_USER$RAPIDO_ADMIN_PASS" ]; then
            warn "$env_file already exists - its admin login is kept; --admin-user/--admin-pass are ignored."
        fi
    fi

    # First admin: only for a brand-new .env. An existing one is never rewritten.
    ADMIN_PASSWORD=""
    if [ "$have_env" = "0" ]; then
        if [ -z "$RAPIDO_ADMIN_USER" ]; then RAPIDO_ADMIN_USER="admin"; fi
        _valid_admin_user "$RAPIDO_ADMIN_USER" || die "--admin-user must be 1-64 letters, digits, dot, dash or underscore."
        if [ -z "$RAPIDO_ADMIN_PASS" ]; then
            RAPIDO_ADMIN_PASS="$(gen_password)"
        else
            _valid_admin_pass "$RAPIDO_ADMIN_PASS" || die "--admin-pass must be 8-128 characters, with no single quote and no control characters."
        fi
        ADMIN_PASSWORD="$RAPIDO_ADMIN_PASS"
    fi
}

# ── install flags ────────────────────────────────────────────────────────────
set_install_opt() {
    case "$1" in
        --domain)        RAPIDO_DOMAIN="$2" ;;
        --extra-domains) RAPIDO_EXTRA_DOMAINS="$2" ;;
        --sub-domain)    RAPIDO_SUB_DOMAIN="$2" ;;
        --admin-user)    RAPIDO_ADMIN_USER="$2" ;;
        --admin-pass)    RAPIDO_ADMIN_PASS="$2" ;;
        --token)         RAPIDO_REPO_TOKEN="$2" ;;
    esac
}

parse_install_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --domain|--extra-domains|--sub-domain|--admin-user|--admin-pass|--token)
                [ $# -ge 2 ] || die "$1 needs a value, e.g. $1 <value>"
                set_install_opt "$1" "$2"; shift 2 ;;
            --domain=*|--extra-domains=*|--sub-domain=*|--admin-user=*|--admin-pass=*|--token=*)
                set_install_opt "${1%%=*}" "${1#*=}"; shift ;;
            -y|--yes)         RAPIDO_YES=1; shift ;;
            --skip-preflight) RAPIDO_SKIP_PREFLIGHT=1; shift ;;
            -h|--help)        usage; exit 0 ;;
            *) die "Unknown option for install: $1 (see: rapido-go help)" ;;
        esac
    done
}

# ── sizing ───────────────────────────────────────────────────────────────────
total_ram_mb() {
    local kb=""
    if [ -r /proc/meminfo ]; then
        kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || true)"
    fi
    if [ -n "$kb" ]; then printf '%s' "$((kb / 1024))"; fi
}

_clamp() {
    local v="$1" lo="$2" hi="$3"
    if [ "$v" -lt "$lo" ]; then v="$lo"; fi
    if [ "$v" -gt "$hi" ]; then v="$hi"; fi
    printf '%s' "$v"
}

# Postgres memory from this machine's RAM (in MB): shared_buffers about 25%
# clamped to 128MB..2GB, effective_cache_size about 60% clamped to
# 512MB..8GB. Prints "<shared_buffers> <effective_cache_size>".
pg_tuning_for_ram_mb() {
    local ram="${1:-0}" sb ecs
    case "$ram" in ''|*[!0-9]*) ram=2048 ;; esac
    if [ "$ram" -eq 0 ]; then ram=2048; fi
    sb="$(_clamp $((ram / 4)) 128 2048)"
    ecs="$(_clamp $((ram * 60 / 100)) 512 8192)"
    printf '%sMB %sMB' "$sb" "$ecs"
}

# ── preflight ────────────────────────────────────────────────────────────────
# Everything here only READS the machine; nothing is installed or changed
# until every hard requirement has passed (or --skip-preflight was given).
MIN_RAM_MB=900      # a "1 GB" VPS reports a little under 1000
WARN_RAM_MB=1800
MIN_DISK_MB=2048
WARN_DISK_MB=5120

# level_for_value <value> <fail-below> <warn-below> -> ok | warn | fail | unknown
level_for_value() {
    local v="${1:-}" f="$2" w="$3"
    case "$v" in ''|*[!0-9]*) printf 'unknown'; return 0 ;; esac
    if [ "$v" -lt "$f" ]; then printf 'fail'
    elif [ "$v" -lt "$w" ]; then printf 'warn'
    else printf 'ok'
    fi
}

normalize_arch() {
    case "${1:-}" in
        x86_64|amd64)  printf 'amd64' ;;
        aarch64|arm64) printf 'arm64' ;;
        *) : ;;
    esac
}

preflight_fail() {
    if [ "${RAPIDO_SKIP_PREFLIGHT:-0}" = "1" ]; then
        warn "$* (ignored: --skip-preflight)"
        return 0
    fi
    die "$* (re-run with --skip-preflight to override this check)"
}

os_pretty_name() {
    local n=""
    if [ -r /etc/os-release ]; then
        n="$(grep -E '^PRETTY_NAME=' /etc/os-release | head -n 1 | cut -d= -f2- | tr -d '"' || true)"
    fi
    printf '%s' "${n:-$(uname -s)}"
}

# Free MB on the filesystem that holds <path> (walks up to a path that exists).
free_disk_mb() {
    local p="${1:-/}"
    while [ ! -e "$p" ] && [ "$p" != "/" ] && [ "$p" != "." ]; do p="$(dirname "$p")"; done
    df -Pm "$p" 2>/dev/null | awk 'NR==2 {print $4}' || true
}

# 0 when something is listening on the TCP port.
port_in_use() {
    local port="$1" out=""
    if command -v ss >/dev/null 2>&1; then
        out="$(ss -ltn "sport = :$port" 2>/dev/null | tail -n +2 || true)"
        if [ -n "$out" ]; then return 0; else return 1; fi
    fi
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then return 0; fi
    return 1
}

port_owner() {
    command -v ss >/dev/null 2>&1 || return 0
    ss -ltnp "sport = :$1" 2>/dev/null | sed -n 's/.*users:(("\([^"]*\)".*/\1/p' | head -n 1 || true
}

# A re-run over a working install: its own Caddy holds 80/443, which is fine.
our_stack_running() {
    local ids=""
    command -v docker >/dev/null 2>&1 || return 1
    ids="$(docker ps -q --filter "label=com.docker.compose.project=${COMPOSE_PROJECT}" 2>/dev/null || true)"
    [ -n "$ids" ]
}

preflight_ports() {
    local p owner busy=0
    if our_stack_running; then
        ok "Ports 80 and 443 are held by the running Rapido-Go stack."
        return 0
    fi
    for p in 80 443; do
        if port_in_use "$p"; then
            busy=1
            owner="$(port_owner "$p")"
            preflight_fail "Port $p is already in use${owner:+ by $owner}. Caddy needs 80 and 443 free (Let's Encrypt validates on 80). Stop that service (for example: systemctl disable --now nginx apache2) and re-run."
        fi
    done
    if [ "$busy" = "0" ]; then ok "Ports 80 and 443 are free."; fi
}

preflight_system() {
    local pm arch ram disk docker_root lvl
    log "Checking this server..."
    if [ "$(uname -s)" != "Linux" ]; then preflight_fail "Rapido-Go installs on Linux only (this is $(uname -s))."; fi

    pm="$(detect_pkg_manager)"
    arch="$(normalize_arch "$(uname -m)")"
    if [ -z "$arch" ]; then preflight_fail "Unsupported CPU architecture: $(uname -m). Rapido-Go supports x86_64 (amd64) and aarch64 (arm64)."; fi
    if [ -z "$pm" ] && ! command -v docker >/dev/null 2>&1; then
        preflight_fail "No supported package manager (apt-get, dnf or yum) and Docker is not installed. Install Docker yourself, then re-run."
    fi
    ok "OS: $(os_pretty_name), ${arch:-$(uname -m)}, package manager: ${pm:-none}"
    if command -v docker >/dev/null 2>&1; then
        ok "Docker is already installed."
    else
        note "Docker is not installed - it will be installed for you."
    fi

    ram="$(total_ram_mb)"
    lvl="$(level_for_value "$ram" "$MIN_RAM_MB" "$WARN_RAM_MB")"
    case "$lvl" in
        fail) preflight_fail "This server has ${ram} MB of RAM; Rapido-Go needs at least ${MIN_RAM_MB} MB (a 1 GB plan). Use a bigger server, or add swap." ;;
        warn) warn "RAM: ${ram} MB - it will run, but 2 GB or more is recommended." ;;
        ok)   ok "RAM: ${ram} MB" ;;
        *)    warn "Could not read this server's RAM size - skipping that check." ;;
    esac

    docker_root="/var/lib/docker"
    disk="$(free_disk_mb "$APP_DIR")"
    lvl="$(level_for_value "$disk" "$MIN_DISK_MB" "$WARN_DISK_MB")"
    case "$lvl" in
        fail) preflight_fail "Only ${disk} MB of disk is free at $APP_DIR; at least ${MIN_DISK_MB} MB is needed (images, database, backups)." ;;
        warn) warn "Disk: ${disk} MB free at $APP_DIR - enough to install, but keep 5 GB or more free for the database and backups." ;;
        ok)   ok "Disk: ${disk} MB free" ;;
        *)    : ;;
    esac
    disk="$(free_disk_mb "$docker_root")"
    lvl="$(level_for_value "$disk" "$MIN_DISK_MB" "$WARN_DISK_MB")"
    if [ "$lvl" = "fail" ]; then
        preflight_fail "Only ${disk} MB of disk is free where Docker keeps images ($docker_root); at least ${MIN_DISK_MB} MB is needed."
    fi

    preflight_ports
}

# Best-effort public IPv4 of this server, empty when it cannot be found.
_valid_ipv4() {
    local ip="${1:-}" o
    case "$ip" in ''|*[!0-9.]*) return 1 ;; esac
    local IFS=.
    set -- $ip
    [ $# -eq 4 ] || return 1
    for o in "$@"; do
        if [ -z "$o" ] || [ "${#o}" -gt 3 ] || [ "$o" -gt 255 ]; then return 1; fi
    done
    return 0
}

detect_public_ip() {
    local u ip
    for u in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
        ip="$(curl -fsS --max-time 5 "$u" 2>/dev/null | tr -d '[:space:]' || true)"
        if _valid_ipv4 "$ip"; then printf '%s' "$ip"; return 0; fi
    done
    return 0
}

# Space-separated IPv4 addresses a name resolves to (empty when none).
resolve_a() {
    local host="$1" out=""
    if command -v getent >/dev/null 2>&1; then
        out="$(getent ahostsv4 "$host" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ' || true)"
    fi
    if [ -z "${out// /}" ] && command -v dig >/dev/null 2>&1; then
        out="$(dig +short +time=3 +tries=1 A "$host" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | tr '\n' ' ' || true)"
    fi
    if [ -z "${out// /}" ] && command -v host >/dev/null 2>&1; then
        out="$(host -W 3 -t A "$host" 2>/dev/null | awk '/has address/ {print $NF}' | tr '\n' ' ' || true)"
    fi
    printf '%s' "${out% }"
}

# Warns - never blocks - when a domain does not point at this server, and says
# the exact record to add. Caddy keeps retrying its certificate request, so a
# record that is still propagating is fine: the install can continue.
preflight_dns() {
    local host ips bad=0
    SERVER_IP="$(detect_public_ip)"
    if [ -z "$SERVER_IP" ]; then
        warn "Could not determine this server's public IP - skipping the DNS check."
        return 0
    fi
    for host in $(split_list "$(all_domains)"); do
        ips="$(resolve_a "$host")"
        case " $ips " in
            *" $SERVER_IP "*) ok "DNS: $host -> $SERVER_IP" ;;
            *)
                bad=1
                if [ -z "$ips" ]; then
                    warn "DNS: $host has no A record yet."
                else
                    warn "DNS: $host points at ${ips% }, not at this server ($SERVER_IP)."
                fi
                note "Add this record:   $host.   A   $SERVER_IP"
                ;;
        esac
    done
    if [ "$bad" = "1" ]; then
        DNS_MISMATCH=1
        note "(If the name is behind a CDN proxy this is expected. Otherwise Caddy cannot get a certificate until the record exists - it keeps retrying on its own.)"
        confirm "Continue anyway?" y || die "Stopped - add the DNS record(s) above and re-run."
    fi
}

# ── configuration ────────────────────────────────────────────────────────────
# The URL(s) subscription links are built on: the subscription domain(s) when
# there are any, otherwise every panel name. Sets SUB_PREFIX (the first, what
# XRAY_SUBSCRIPTION_URL_PREFIX carries) and SUB_PREFIXES (the whole ordered
# list, only when there is more than one).
compute_sub_urls() {
    local hosts h list=""
    if [ -n "$RAPIDO_SUB_DOMAIN" ]; then
        hosts="$RAPIDO_SUB_DOMAIN"
    else
        hosts="$RAPIDO_DOMAIN${RAPIDO_EXTRA_DOMAINS:+,$RAPIDO_EXTRA_DOMAINS}"
    fi
    for h in $(split_list "$hosts"); do
        list="${list:+$list,}https://$h"
    done
    SUB_PREFIX="${list%%,*}"
    case "$list" in
        *,*) SUB_PREFIXES="$list" ;;
        *)   SUB_PREFIXES="" ;;
    esac
}

# Caddy handles the certificates itself (request + renewal, zero ongoing
# maintenance) - this just writes the one file that tells it what to do,
# same idea as generate_env below but for Caddy's config instead of the
# panel's.
#
# Panel block: the primary domain plus any extra domains, ONE site block, so
# they share a single reverse proxy. Subscription block (optional): its own
# name(s), and it proxies only what a subscriber needs - /sub/*, the fonts
# the subscription page loads, the health probe and the empty root - so the
# dashboard and the admin API are not reachable through it. `handle` blocks
# rather than a bare `respond`, because Caddy orders `respond` before
# `reverse_proxy` and a bare one would answer everything itself.
render_caddyfile() {
    local panel_hosts="$RAPIDO_DOMAIN${RAPIDO_EXTRA_DOMAINS:+, ${RAPIDO_EXTRA_DOMAINS//,/, }}"
    cat <<EOF
${panel_hosts} {
	encode zstd gzip
	reverse_proxy panel:8000
}
EOF
    if [ -n "$RAPIDO_SUB_DOMAIN" ]; then
        cat <<EOF

${RAPIDO_SUB_DOMAIN//,/, } {
	encode zstd gzip
	@subscriber path / /sub/* /statics/* /health
	handle @subscriber {
		reverse_proxy panel:8000
	}
	handle {
		respond "Not found" 404
	}
}
EOF
    fi
}

write_caddyfile() {
    local target="$APP_DIR/Caddyfile" new
    new="$(mktemp)"
    render_caddyfile > "$new"
    # Never silently replace a Caddyfile that was edited by hand (extra
    # domains, custom blocks): keep the old one next to it.
    if [ -f "$target" ] && ! cmp -s "$new" "$target"; then
        cp -p "$target" "$target.bak-$(date +%Y%m%d-%H%M%S)"
        warn "The existing Caddyfile differed - the old one is kept as $target.bak-*"
    fi
    cat "$new" > "$target"
    rm -f "$new"
    ok "Caddyfile written for $RAPIDO_DOMAIN${RAPIDO_EXTRA_DOMAINS:+, $RAPIDO_EXTRA_DOMAINS}${RAPIDO_SUB_DOMAIN:+ and subscription domain $RAPIDO_SUB_DOMAIN}."
}

# render_env <db_password> <admin_user> <admin_password> <shared_buffers>
#            <effective_cache_size> <public_ip>
render_env() {
    local db_pass="$1" admin_user="$2" admin_pass="$3" sb="$4" ecs="$5" ip="$6"
    compute_sub_urls
    cat <<EOF
# Generated by the Rapido-Go installer on $(date -u +%Y-%m-%dT%H:%M:%SZ)
# Edit with: rapido-go edit-env      (restart afterwards: rapido-go restart)

# ── domains ─────────────────────────────────────────────────────────────────
# Panel name, extra names serving the same dashboard, and an optional separate
# subscription domain. The installer wrote ${APP_DIR}/Caddyfile from these;
# after changing them edit the Caddyfile too (see Caddyfile.example), then
# recreate Caddy so it re-reads the file (certificates are kept):
#   cd ${APP_DIR} && docker compose -p ${COMPOSE_PROJECT} up -d --force-recreate caddy
RAPIDO_DOMAIN="${RAPIDO_DOMAIN}"
RAPIDO_EXTRA_DOMAINS="${RAPIDO_EXTRA_DOMAINS}"
RAPIDO_SUB_DOMAIN="${RAPIDO_SUB_DOMAIN}"

# ── database ────────────────────────────────────────────────────────────────
POSTGRES_PASSWORD="${db_pass}"
# Sized from this machine's RAM at install time (about 25% and 60%).
POSTGRES_SHARED_BUFFERS="${sb}"
POSTGRES_EFFECTIVE_CACHE_SIZE="${ecs}"

# ── first admin ─────────────────────────────────────────────────────────────
# This is the one-time bootstrap login (checked in-memory before the admins
# table is ever queried, so there is always a way in from an empty
# database). Log in once, create a real sudo admin from the Admins page,
# then rotate this value if you want the break-glass login to stop working.
SUDO_USERNAME="${admin_user}"
SUDO_PASSWORD='${admin_pass}'

# ── what customers get ──────────────────────────────────────────────────────
# The prefix every subscription link is built from. Change it here if the
# domain changes - existing links are rebuilt from it on the next fetch.
XRAY_SUBSCRIPTION_URL_PREFIX="${SUB_PREFIX}"
# Every address a subscription answers on, in dashboard order (blank = just the
# one above).
XRAY_SUBSCRIPTION_URL_PREFIXES="${SUB_PREFIXES}"
# Feeds the {SERVER_IP} placeholder in subscription remarks, if you use it.
PUBLIC_IP="${ip}"

# ── CORS ────────────────────────────────────────────────────────────────────
ALLOWED_ORIGINS="*"

# ── optional ────────────────────────────────────────────────────────────────
# Days of usage history to keep (the compose default is 90).
# USAGE_RETENTION_DAYS=90
EOF
}

generate_env() {
    local env_file="$APP_DIR/.env"
    if [ -f "$env_file" ]; then
        warn "$env_file already exists - leaving it untouched."
        return
    fi
    local db_pass ram tuning sb ecs ip
    db_pass="$(random_secret)"
    ram="$(total_ram_mb)"
    tuning="$(pg_tuning_for_ram_mb "${ram:-0}")"
    sb="${tuning%% *}"
    ecs="${tuning##* }"
    ip="${SERVER_IP:-}"
    if [ -z "$ip" ]; then ip="$(detect_public_ip)"; fi
    if [ -z "$ip" ]; then ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"; fi
    mkdir -p "$DATA_DIR"
    chmod 700 "$DATA_DIR"
    log "This machine: $(nproc 2>/dev/null || echo '?') cores, ${ram:-?} MB RAM - Postgres shared_buffers $sb, effective_cache_size $ecs"

    ( umask 077; render_env "$db_pass" "$RAPIDO_ADMIN_USER" "$RAPIDO_ADMIN_PASS" "$sb" "$ecs" "$ip" > "$env_file" )
    chmod 600 "$env_file"
    ok "Configuration written to $env_file"
}

# ── images ───────────────────────────────────────────────────────────────────
# Every image the stack runs, one per line, resolved through .env exactly as
# `docker compose up` would.
image_list() { compose config --images 2>/dev/null | sort -u || true; }

is_panel_image() { case "$1" in *rapido-go-panel*) return 0 ;; *) return 1 ;; esac; }

# Image IDs, to tell whether a pull changed anything.
images_fingerprint() {
    local img out=""
    for img in $(image_list); do
        out="${out}${img}=$(docker image inspect --format '{{.Id}}' "$img" 2>/dev/null || true);"
    done
    printf '%s' "$out"
}

# Pulls every image of the stack at the same time (one line each, failures
# with the registry's own words). Sets PULL_FAILED to the images that did not
# come down. With RAPIDO_BUILD_LOCALLY=1 the panel image is left to the build.
PULL_FAILED=""
pull_images() {
    local imgs img n=0 i tmp
    PULL_FAILED=""
    imgs="$(image_list)"
    if [ -z "$imgs" ]; then
        warn "Could not list the stack's images - docker compose will pull them as it starts."
        return 0
    fi
    tmp="$(mktemp -d)"
    log "Pulling images in parallel..."
    for img in $imgs; do
        if [ "${RAPIDO_BUILD_LOCALLY:-}" = "1" ] && is_panel_image "$img"; then continue; fi
        n=$((n + 1))
        printf '%s\n' "$img" > "$tmp/$n.img"
        ( if docker pull -q "$img" > "$tmp/$n.out" 2>&1; then echo 0; else echo 1; fi > "$tmp/$n.rc" ) &
    done
    wait
    i=1
    while [ "$i" -le "$n" ]; do
        img="$(cat "$tmp/$i.img")"
        if [ "$(cat "$tmp/$i.rc" 2>/dev/null || echo 1)" = "0" ]; then
            ok "  $img"
        else
            PULL_FAILED="${PULL_FAILED:+$PULL_FAILED }$img"
            err "  $img could not be pulled:"
            tail -n 3 "$tmp/$i.out" 2>/dev/null | sed 's/^/      /' >&2 || true
            if grep -qiE 'denied|unauthorized|authentication required' "$tmp/$i.out" 2>/dev/null; then
                err "      The registry refused the login. For a private image the token needs the read:packages scope (a fine-grained token cannot read packages)."
            fi
        fi
        i=$((i + 1))
    done
    rm -rf "$tmp"
}

build_local_image() {
    [ -f "$APP_DIR/docker/Dockerfile.panel" ] \
        || die "The source tree is not here, so the image cannot be built. Re-run with RAPIDO_BUILD_LOCALLY=1 to fetch it."
    log "Building the image (this takes a few minutes)..."
    ( cd "$APP_DIR" && DOCKER_BUILDKIT=1 docker build -f docker/Dockerfile.panel \
        --build-arg "VERSION=$(source_version)" -t "${PANEL_IMAGE}:latest" . )
    docker image inspect "${PANEL_IMAGE}:latest" >/dev/null 2>&1 \
        || die "The image was not built - see the output above."
    ok "Image built."
}

# The image is published by CI, so a normal install pulls it instead of
# compiling one. Building locally means Go + Node + sqlc/goose install:
# several minutes of CPU on a small VPS, paid on every install and every
# update to produce a byte-identical result - so it stays the fallback,
# not the default. See docker/Dockerfile.panel.
obtain_images() {
    local other f panel_failed
    pull_images
    if [ "${RAPIDO_BUILD_LOCALLY:-}" = "1" ]; then
        log "RAPIDO_BUILD_LOCALLY=1, building from source..."
        build_local_image
    fi
    if [ -n "$PULL_FAILED" ]; then
        other=""
        panel_failed=0
        for f in $PULL_FAILED; do
            if is_panel_image "$f"; then panel_failed=1; continue; fi
            # A registry hiccup must not stop an update of the panel when the
            # image it could not refresh is already here (compose would have
            # used the local copy before, without asking the registry).
            if docker image inspect "$f" >/dev/null 2>&1; then
                warn "Could not refresh $f - keeping the copy already on this server."
            else
                other="${other:+$other }$f"
            fi
        done
        [ -z "$other" ] || die "Could not pull: $other - check this server's network and Docker Hub access."
        if [ "$panel_failed" = "1" ]; then
            if [ -d "$APP_DIR/.git" ]; then
                log "No prebuilt image available - building from source instead."
                build_local_image
            else
                die "Could not pull the panel image. If it is private, pass a token with read:packages (RAPIDO_REPO_TOKEN=<token>); to build it on this server instead: RAPIDO_BUILD_LOCALLY=1 rapido-go install"
            fi
        fi
    fi
    ok "Images ready."
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

# Public https:// wait, in seconds. Shortened when the DNS check already said
# the name does not point here - waiting the full time would be pointless.
PUBLIC_WAIT=90

wait_healthy() {
    # cmd_install's own call arrives with RAPIDO_DOMAIN already set by
    # resolve_inputs earlier in the same run, but cmd_update never asks for
    # it at all - load it from .env here too (same idiom as
    # load_saved_token) so `rapido-go update` doesn't crash under set -u
    # on a plain unset-variable reference the moment it reaches the
    # https:// check below. Found by actually running `rapido-go update`
    # non-interactively, not assumed.
    if [ -z "${RAPIDO_DOMAIN:-}" ]; then
        RAPIDO_DOMAIN="$(env_get RAPIDO_DOMAIN)"
    fi
    if [ "${DNS_MISMATCH:-0}" = "1" ]; then PUBLIC_WAIT=20; fi
    # Caddy needs a moment to get its certificate on a fresh domain before
    # https:// answers - poll plain HTTP on the panel's own compose network
    # first (proves the app itself is up), then the public https:// URL.
    log "Waiting for the panel to come up..."
    local i
    for i in $(seq 1 60); do
        compose exec -T panel wget -q -O /dev/null http://127.0.0.1:8000/dashboard/ 2>/dev/null </dev/null \
            && { ok "Panel is answering internally (${i}s)."; break; }
        sleep 1
        [ "$i" -eq 60 ] && { warn "The panel did not answer internally within 60s. Check: rapido-go logs panel"; return 0; }
    done

    log "Waiting for https://${RAPIDO_DOMAIN}/dashboard/ (Caddy obtaining a certificate)..."
    for i in $(seq 1 "$PUBLIC_WAIT"); do
        curl -fsS --max-time 3 -o /dev/null "https://${RAPIDO_DOMAIN}/dashboard/" 2>/dev/null \
            && { ok "Panel is up at https://${RAPIDO_DOMAIN}/dashboard/ (${i}s)."; return 0; }
        sleep 1
    done
    warn "https://${RAPIDO_DOMAIN} did not answer within ${PUBLIC_WAIT}s."
    printf "${C_DIM}  Usually the domain does not point here yet, or inbound port 80/443\n"
    printf "  is blocked - Caddy needs both to obtain a certificate.\n"
    printf "  Check: rapido-go logs caddy${C_RESET}\n"
}

# ── commands ─────────────────────────────────────────────────────────────────
# Services that should be running but are not, space-separated (empty = all up).
missing_services() {
    local want got svc missing=""
    want="$(cd "$APP_DIR" && docker compose -p "$COMPOSE_PROJECT" -f "$(compose_file)" config --services 2>/dev/null)" || want=""
    got="$(cd "$APP_DIR" && docker compose -p "$COMPOSE_PROJECT" -f "$(compose_file)" --env-file .env ps --services --filter status=running 2>/dev/null)" || got=""
    [ -n "$want" ] || { printf 'UNKNOWN'; return 0; }
    for svc in $want; do
        [ "$svc" = "migrate" ] && continue # exits 0 on purpose once done
        printf '%s\n' "$got" | grep -qx "$svc" || missing="$missing $svc"
    done
    printf '%s' "${missing# }"
}

verify_stack() {
    local missing
    missing="$(missing_services)"
    if [ "$missing" = "UNKNOWN" ]; then err "Could not read the service list from the compose file."; return 1; fi
    if [ -n "$missing" ]; then
        err "These services are not running: $missing"
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

print_summary() {
    local h
    printf "\n${C_GREEN}${C_BOLD}Rapido-Go is installed.${C_RESET}\n\n"
    printf "  %-15s${C_BOLD}https://%s/dashboard/${C_RESET}\n" "Dashboard" "$RAPIDO_DOMAIN"
    for h in $(split_list "$RAPIDO_EXTRA_DOMAINS"); do
        printf "  %-15shttps://%s/dashboard/\n" "" "$h"
    done
    if [ -n "${ADMIN_PASSWORD:-}" ]; then
        printf "  %-15s${C_BOLD}%s${C_RESET} / ${C_BOLD}%s${C_RESET}\n" "Admin login" "$RAPIDO_ADMIN_USER" "$ADMIN_PASSWORD"
    else
        printf "  %-15s%s ${C_DIM}(password already set - see SUDO_PASSWORD in %s/.env)${C_RESET}\n" \
            "Admin login" "$(env_get SUDO_USERNAME admin)" "$APP_DIR"
    fi
    printf "  %-15s%s/sub/<token>\n" "Subscriptions" "$(env_get XRAY_SUBSCRIPTION_URL_PREFIX "https://$RAPIDO_DOMAIN")"
    printf "  %-15s%s ${C_DIM}(rapido-go backup)${C_RESET}\n" "Backups" "$DATA_DIR"
    printf "  %-15s%s/.env ${C_DIM}(mode 600)${C_RESET}\n" "Settings" "$APP_DIR"
    if [ -n "${ADMIN_PASSWORD:-}" ]; then
        printf "\n${C_YELLOW}  Save that password now - it is not shown again. Log in once,\n"
        printf "  create a real sudo admin, then rotate this one if you want.${C_RESET}\n"
    fi
    printf "\n  ${C_DIM}Commands   rapido-go status | logs | update | backup | restore | doctor${C_RESET}\n"
    printf "  ${C_DIM}Add nodes  open Nodes in the dashboard and copy the one command it shows${C_RESET}\n\n"
}

cmd_install() {
    require_root
    banner
    preflight_system
    ensure_prereqs
    resolve_inputs
    preflight_dns
    resolve_repo_access strict
    ensure_docker
    ensure_compose
    fetch_source
    generate_env
    write_caddyfile
    save_token
    install_command
    ensure_registry_login
    obtain_images
    log "Starting Rapido-Go..."
    compose up -d
    wait_healthy
    verify_stack || die "The stack did not come up cleanly - see the output above."
    print_summary
    # The summary (and the one-time password in it) is the last thing on the
    # screen - streaming logs under it would scroll it away. Opt back in with
    # RAPIDO_FOLLOW_LOGS=1.
    if [ "${RAPIDO_FOLLOW_LOGS:-0}" = "1" ]; then follow_logs; fi
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

# 0 when the running stack differs from what is now on disk / pulled:
# $1 = image fingerprint from before the pull.
update_needed() {
    local before_fp="$1" cid have want ref missing
    if [ "${COMPOSE_CHANGED:-0}" = "1" ]; then return 0; fi
    if [ "$(images_fingerprint)" != "$before_fp" ]; then return 0; fi
    missing="$(missing_services)"
    if [ -n "$missing" ]; then return 0; fi
    # An earlier update may have pulled a newer image and then been
    # interrupted before recreating: the containers still run the old one.
    cid="$(compose ps -q panel 2>/dev/null | head -n 1 || true)"
    ref="$(image_list | grep 'rapido-go-panel' | head -n 1 || true)"
    if [ -z "$cid" ] || [ -z "$ref" ]; then return 0; fi
    have="$(docker inspect --format '{{.Image}}' "$cid" 2>/dev/null || true)"
    want="$(docker image inspect --format '{{.Id}}' "$ref" 2>/dev/null || true)"
    if [ -n "$have" ] && [ "$have" = "$want" ]; then return 1; fi
    return 0
}

cmd_update() {
    require_root
    require_installed
    ensure_docker
    resolve_repo_access lenient
    local before_sum after_sum before_fp
    before_sum="$(file_sum "$APP_DIR/$(compose_file)")"
    fetch_source
    install_command
    after_sum="$(file_sum "$APP_DIR/$(compose_file)")"
    COMPOSE_CHANGED=0
    if [ "$before_sum" != "$after_sum" ]; then COMPOSE_CHANGED=1; fi
    ensure_registry_login
    before_fp="$(images_fingerprint)"
    obtain_images
    if ! update_needed "$before_fp"; then
        ok "Already up to date ($(installed_version_label))."
        return 0
    fi
    # Nothing has been recreated yet: the pull above only downloaded images.
    # The backup still comes before the stack changes, and a failed backup
    # stops the update (cmd_backup exits) - set RAPIDO_SKIP_BACKUP=1 to
    # override on a box whose database is already broken.
    if [ "${RAPIDO_SKIP_BACKUP:-0}" = "1" ]; then
        warn "RAPIDO_SKIP_BACKUP=1 - skipping the pre-update backup."
    else
        log "Backing up the database first..."
        cmd_backup
    fi
    log "Applying the update..."
    compose up -d  # re-applies migrations via the migrate service, then recreates whatever image tag or config changed
    wait_healthy
    docker image prune -f >/dev/null 2>&1 || true
    ls -1t "$DATA_DIR"/backup-*.sql.gz 2>/dev/null | tail -n +6 | xargs -r rm -f || true
    verify_stack || die "The stack did not come back cleanly after the update."
    ok "Updated to $(installed_version_label)."
    follow_logs
}

cmd_backup() {
    require_installed
    local dest="${1:-$DATA_DIR/backup-$(date +%Y%m%d-%H%M%S).sql.gz}"
    mkdir -p "$(dirname "$dest")"
    log "Dumping the database to $dest..."
    # Check the DUMP, not the file. gzip of a failed or empty pg_dump still
    # writes a valid ~20-byte stream, so `[ -s "$dest" ]` is true and the
    # old guard never fired - `rapido-go update` would then report a
    # successful backup and go on to rebuild on top of nothing.
    if ! compose exec -T postgres sh -c 'exec pg_dump -U rapido rapido' | gzip > "$dest"; then
        rm -f "$dest"
        die "pg_dump failed - nothing was written. Is the database up? (rapido-go status)"
    fi
    # Read the first byte into a variable rather than piping into `grep -q`:
    # grep exits at the first match, which under `pipefail` turns the SIGPIPE
    # gunzip then gets into a failed pipeline - a healthy dump of a large
    # database was reported as empty and blocked `rapido-go update`.
    local first_byte
    first_byte="$(gunzip -c "$dest" 2>/dev/null | head -c 1 || true)"
    if [ -z "$first_byte" ]; then
        rm -f "$dest"
        die "The backup came out empty - nothing was written."
    fi
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
    local answer; read -r answer || answer=""
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
    before="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1 || true)"
    "${EDITOR:-nano}" "$APP_DIR/.env"
    after="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1 || true)"
    if [ "$before" != "$after" ]; then
        printf "\n"
        warn "Configuration changed. Apply it now? [Y/n] "
        local answer; read -r answer || answer=""
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
    local answer; read -r answer || answer=""
    [ "$answer" = "yes" ] || die "Aborted."
    compose down --remove-orphans --volumes || true
    rm -rf "$APP_DIR"
    rm -f "$BIN_PATH"
    ok "Rapido-Go removed. Backups left in $DATA_DIR"
}

# ── versions ─────────────────────────────────────────────────────────────────
# The version stamped into the running panel image (build arg VERSION -> the
# org.opencontainers.image.version label): a release tag or a short commit
# sha. Empty when nothing is running or the image predates the label.
installed_image_version() {
    local cid=""
    [ -d "$APP_DIR" ] || return 0
    command -v docker >/dev/null 2>&1 || return 0
    cid="$(compose ps -q panel 2>/dev/null | head -n 1 || true)"
    [ -n "$cid" ] || return 0
    docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$cid" 2>/dev/null || true
}

installed_version_label() {
    local v
    v="$(installed_image_version)"
    if [ -n "$v" ] && [ "$v" != "<no value>" ]; then printf '%s' "$v"; else source_version; fi
}

# Newest published release tag (empty when there is none or GitHub is
# unreachable), and the short sha at the tip of the install branch.
latest_release_tag() {
    _curl_gh "$RAPIDO_REPO_TOKEN" -fsS --max-time 8 \
        "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest" 2>/dev/null \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1 || true
}

latest_branch_sha() {
    _curl_gh "$RAPIDO_REPO_TOKEN" -fsS --max-time 8 -H 'Accept: application/vnd.github.sha' \
        "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/commits/${REPO_BRANCH}" 2>/dev/null \
        | cut -c1-7 || true
}

cmd_version() {
    printf "rapido-go %s\n" "$RAPIDO_GO_VERSION"
    if [ -d "$APP_DIR/.git" ]; then
        printf "source    %s\n" "$(git -C "$APP_DIR" rev-parse --short HEAD)"
    elif [ -f "$APP_DIR/.source-version" ]; then
        printf "source    %s\n" "$(source_version)"
    fi
    local v
    v="$(installed_image_version)"
    if [ -n "$v" ] && [ "$v" != "<no value>" ]; then printf "image     %s\n" "$v"; fi
    return 0
}

# ── doctor ───────────────────────────────────────────────────────────────────
# Read-only. Every check reports and carries on; the exit status is 1 only if
# something FAILED (warnings do not fail it).
DOC_FAILS=0
DOC_WARNS=0
doc_ok()   { ok "$*"; }
doc_warn() { warn "$*"; DOC_WARNS=$((DOC_WARNS + 1)); }
doc_fail() { err "$*"; DOC_FAILS=$((DOC_FAILS + 1)); }
doc_head() { printf "\n${C_BOLD}%s${C_RESET}\n" "$*"; }

# Whole days until a date string `date -d` understands ("Nov 30 12:00:00 2026 GMT");
# negative when past; empty when the date cannot be read.
days_until_date() {
    local end_ts now_ts="${2:-}"
    end_ts="$(date -d "$1" +%s 2>/dev/null || true)"
    [ -n "$end_ts" ] || return 0
    if [ -z "$now_ts" ]; then now_ts="$(date +%s)"; fi
    printf '%s' "$(( (end_ts - now_ts) / 86400 ))"
}

_timeout() {
    if command -v timeout >/dev/null 2>&1; then timeout "$@"; else shift; "$@"; fi
}

# Days left on the certificate Caddy is serving for <host> (asked of the local
# Caddy, so it does not depend on DNS). Empty when there is none yet.
cert_days_left() {
    local end=""
    command -v openssl >/dev/null 2>&1 || return 0
    end="$(_timeout 8 openssl s_client -connect 127.0.0.1:443 -servername "$1" </dev/null 2>/dev/null \
        | openssl x509 -noout -enddate 2>/dev/null || true)"
    end="${end#notAfter=}"
    [ -n "$end" ] || return 0
    days_until_date "$end"
}

doctor_docker() {
    doc_head "Docker"
    if docker_ready; then doc_ok "Docker daemon is running."; else doc_fail "Docker is not running (systemctl start docker)."; return 0; fi
    if docker compose version >/dev/null 2>&1; then doc_ok "docker compose plugin present."; else doc_fail "The docker compose plugin is missing."; fi
}

doctor_containers() {
    doc_head "Containers"
    local missing svc cid health restarts
    missing="$(missing_services)"
    if [ "$missing" = "UNKNOWN" ]; then
        doc_fail "Could not read the service list (is $APP_DIR/.env complete?)."
        return 0
    fi
    if [ -z "$missing" ]; then doc_ok "All services are running."; else doc_fail "Not running: $missing  (rapido-go status, rapido-go logs <service>)"; fi
    for svc in postgres redis panel backend caddy; do
        cid="$(compose ps -q "$svc" 2>/dev/null | head -n 1 || true)"
        [ -n "$cid" ] || continue
        health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$cid" 2>/dev/null || true)"
        restarts="$(docker inspect --format '{{.RestartCount}}' "$cid" 2>/dev/null || echo 0)"
        if [ "$health" = "unhealthy" ]; then doc_fail "$svc is unhealthy."; fi
        if [ "${restarts:-0}" -gt 3 ] 2>/dev/null; then doc_warn "$svc has restarted $restarts times (rapido-go logs $svc)."; fi
    done
}

doctor_database() {
    doc_head "Database and cache"
    local out
    if compose exec -T postgres pg_isready -U rapido -q >/dev/null 2>&1; then
        doc_ok "Postgres accepts connections."
        out="$(compose exec -T postgres psql -U rapido -d rapido -tAc "select pg_size_pretty(pg_database_size('rapido')) || ', shared_buffers ' || current_setting('shared_buffers')" 2>/dev/null </dev/null || true)"
        if [ -n "$out" ]; then doc_ok "Database size: $out"; else doc_warn "Could not query the database."; fi
    else
        doc_fail "Postgres is not answering (rapido-go logs postgres)."
    fi
    out="$(compose exec -T redis redis-cli ping 2>/dev/null </dev/null | tr -d '\r' || true)"
    if [ "$out" = "PONG" ]; then
        doc_ok "Redis answers."
        out="$(compose exec -T redis redis-cli config get save 2>/dev/null </dev/null | tr -d '\r' | sed -n 2p || true)"
        if [ -n "$out" ]; then doc_warn "Redis still writes snapshots to disk (save '$out') - run: rapido-go update"; fi
    else
        doc_fail "Redis is not answering (rapido-go logs redis)."
    fi
}

doctor_disk() {
    doc_head "Disk"
    local p free lvl root
    root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
    for p in "$APP_DIR" "$DATA_DIR" "${root:-/var/lib/docker}"; do
        [ -e "$p" ] || continue
        free="$(free_disk_mb "$p")"
        lvl="$(level_for_value "$free" 1024 3072)"
        case "$lvl" in
            fail) doc_fail "$p: only ${free} MB free." ;;
            warn) doc_warn "$p: ${free} MB free - getting low." ;;
            ok)   doc_ok "$p: ${free} MB free." ;;
            *)    : ;;
        esac
    done
    local latest
    latest="$(ls -1t "$DATA_DIR"/backup-*.sql.gz 2>/dev/null | head -n 1 || true)"
    if [ -z "$latest" ]; then
        doc_warn "No backups in $DATA_DIR yet (rapido-go backup)."
    elif [ -n "$(find "$latest" -mtime +7 2>/dev/null || true)" ]; then
        doc_warn "The newest backup is over 7 days old: $latest"
    else
        doc_ok "Newest backup: $latest"
    fi
}

# Site addresses in the live Caddyfile (one per line): what is actually being
# served, which is the truth when the file was edited by hand - the domains in
# .env only describe what the installer wrote.
caddyfile_hosts() {
    local f="$APP_DIR/Caddyfile"
    [ -f "$f" ] || return 0
    # Address lines start in column 0 and end in `{`; options blocks (`{`),
    # snippets (`(name) {`), comments and port-only addresses are dropped.
    grep -E '^[^[:space:]#{}(]' "$f" 2>/dev/null \
        | grep -E '\{[[:space:]]*$' \
        | sed -e 's/[[:space:]]*{[[:space:]]*$//' -e 's/,/ /g' -e 's#https\?://##g' \
        | tr ' ' '\n' \
        | grep -E '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$' || true
}

# Every name to check, primary first, without duplicates: what .env says plus
# what the Caddyfile serves.
doctor_names() {
    local n out=""
    for n in $(split_list "$(all_domains)") $(caddyfile_hosts); do
        case " $out " in *" $n "*) continue ;; esac
        out="${out:+$out }$n"
    done
    printf '%s' "$out"
}

doctor_http() {
    doc_head "Panel"
    local d prefix
    if compose exec -T panel wget -q -O /dev/null http://127.0.0.1:8000/dashboard/ >/dev/null 2>&1 </dev/null; then
        doc_ok "The panel answers /dashboard/ inside the stack."
    else
        doc_fail "The panel does not answer /dashboard/ (rapido-go logs panel)."
    fi
    prefix="$(env_get XRAY_SUBSCRIPTION_URL_PREFIX)"
    if [ -z "$RAPIDO_DOMAIN" ]; then doc_warn "RAPIDO_DOMAIN is not set in $APP_DIR/.env - skipping the public checks."; return 0; fi
    if curl -fsS --max-time 10 -o /dev/null "https://${RAPIDO_DOMAIN}/dashboard/" 2>/dev/null; then
        doc_ok "https://${RAPIDO_DOMAIN}/dashboard/ answers."
    else
        doc_fail "https://${RAPIDO_DOMAIN}/dashboard/ does not answer (DNS, ports 80/443 or the certificate - rapido-go logs caddy)."
    fi
    # Every other name (extra panel names, subscription names) answers /health,
    # including the ones that only serve subscriptions.
    for d in $(doctor_names); do
        [ "$d" = "$RAPIDO_DOMAIN" ] && continue
        if curl -fsS --max-time 10 -o /dev/null "https://${d}/health" 2>/dev/null; then doc_ok "https://${d}/health answers."; else doc_warn "https://${d}/health does not answer."; fi
    done
    if [ -n "$prefix" ]; then note "Subscription links are built on $prefix"; fi
}

doctor_certs() {
    doc_head "Certificates"
    local d days
    if ! command -v openssl >/dev/null 2>&1; then doc_warn "openssl is not installed - skipping the certificate check."; return 0; fi
    if [ -z "$(doctor_names)" ]; then doc_warn "No domain found in $APP_DIR/.env or the Caddyfile - nothing to check."; return 0; fi
    for d in $(doctor_names); do
        days="$(cert_days_left "$d")"
        if [ -z "$days" ]; then
            doc_warn "$d: no certificate served yet (DNS not pointing here, or Caddy still working - rapido-go logs caddy)."
        elif [ "$days" -lt 0 ]; then
            doc_fail "$d: the certificate EXPIRED ${days#-} days ago (rapido-go logs caddy)."
        elif [ "$days" -lt 14 ]; then
            doc_warn "$d: the certificate expires in $days days - Caddy renews at 30, so renewal is failing (rapido-go logs caddy)."
        else
            doc_ok "$d: certificate valid for $days more days."
        fi
    done
}

doctor_version() {
    doc_head "Version"
    local installed rel sha
    installed="$(installed_image_version)"
    if [ -z "$installed" ] || [ "$installed" = "<no value>" ]; then
        doc_warn "The running image carries no version label (it predates it). Source: $(source_version)"
        return 0
    fi
    doc_ok "Installed: $installed  (rapido-go script $RAPIDO_GO_VERSION)"
    rel="$(latest_release_tag)"
    sha="$(latest_branch_sha)"
    case "$installed" in
        v[0-9]*)
            if [ -z "$rel" ]; then note "Could not read the latest release from GitHub."
            elif [ "$rel" = "$installed" ]; then doc_ok "Up to date (latest release $rel)."
            else doc_warn "A newer release is available: $rel - run: rapido-go update"; fi ;;
        *)
            if [ -z "$sha" ]; then note "Could not read the latest version from GitHub."
            elif [ "${sha#"$installed"}" != "$sha" ] || [ "${installed#"$sha"}" != "$installed" ]; then doc_ok "Up to date (latest commit $sha)."
            else doc_warn "A newer version is available ($sha, installed $installed) - run: rapido-go update"; fi ;;
    esac
}

cmd_doctor() {
    require_root
    require_installed
    RAPIDO_DOMAIN="$(env_get RAPIDO_DOMAIN)"
    RAPIDO_EXTRA_DOMAINS="$(env_get RAPIDO_EXTRA_DOMAINS)"
    RAPIDO_SUB_DOMAIN="$(env_get RAPIDO_SUB_DOMAIN)"
    printf "\n${C_BOLD}Rapido-Go doctor${C_RESET}  ${C_DIM}(read-only)${C_RESET}\n"
    doctor_docker
    if docker_ready; then
        doctor_containers
        doctor_database
        doctor_disk
        doctor_http
        doctor_certs
        doctor_version
    fi
    printf "\n"
    if [ "$DOC_FAILS" -gt 0 ]; then
        err "$DOC_FAILS problem(s) found, $DOC_WARNS warning(s)."
        exit 1
    fi
    if [ "$DOC_WARNS" -gt 0 ]; then
        warn "No failures, $DOC_WARNS warning(s)."
    else
        ok "Everything looks healthy."
    fi
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
    _u_row  "install [options]" "Install Docker if needed, fetch and start"
    _u_row  "update"    "Pull new images and restart if anything changed"
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
    _u_row  "doctor"   "Read-only health checks (exit 1 on a failure)"
    _u_row  "edit-env" "Open .env in \$EDITOR"
    _u_row  "version"  "Show the installed and source versions"
    _u_gap

    _u_head "INSTALL OPTIONS"
    _u_row  "--domain <name>"       "Panel domain (asked for if omitted)"
    _u_row  "--extra-domains <a,b>" "More names serving the same dashboard"
    _u_row  "--sub-domain <name>"   "Separate domain for subscription links"
    _u_row  "--admin-user <name>"   "First admin login (default: admin)"
    _u_row  "--admin-pass <pass>"   "First admin password (default: generated)"
    _u_row  "--token <pat>"         "GitHub token, only for a private repo/images"
    _u_row  "--yes"                 "Never ask - take the defaults"
    _u_row  "--skip-preflight"      "Turn failed server checks into warnings"
    _u_gap

    _u_head "ENVIRONMENT"
    _u_row  "RAPIDO_GO_APP_DIR"      "Where the files live (/opt/rapido-go)"
    _u_row  "RAPIDO_GO_DATA_DIR"     "Where backups live (/var/lib/rapido-go)"
    _u_row  "RAPIDO_DOMAIN"          "Panel domain, to skip the prompt"
    _u_row  "RAPIDO_EXTRA_DOMAINS"   "Extra panel domains, comma-separated"
    _u_row  "RAPIDO_SUB_DOMAIN"      "Subscription domain(s), comma-separated"
    _u_row  "RAPIDO_NO_FOLLOW=1"     "Do not tail the log after restart/update"
    _u_row  "RAPIDO_REPO_TOKEN"      "Only for a private fork or images"
    _u_row  "RAPIDO_REPO_BRANCH"     "Branch or tag to install (default: master)"
    _u_row  "RAPIDO_BUILD_LOCALLY=1" "Build here instead of pulling from ghcr.io"
    _u_join

    _u_wide "EXAMPLES"
    _u_blue "  rapido-go install --domain panel.example.com"
    _u_blue "  rapido-go install --domain a.example.com --sub-domain s.example.com"
    _u_blue "  rapido-go doctor"
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
            parse_install_args "$@"
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
        doctor)    cmd_doctor ;;
        edit-env)  cmd_edit_env ;;
        uninstall) cmd_uninstall ;;
        version|-v|--version) cmd_version ;;
        ""|help|-h|--help) usage ;;
        *) err "Unknown command: $cmd"; echo; usage; exit 1 ;;
    esac
}

main "$@"
