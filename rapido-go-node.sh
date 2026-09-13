#!/usr/bin/env bash
#
#  Rapido-Go Node - installer and management CLI.
#
#  Install:
#    bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapido-go/master/rapido-go-node.sh) install
#
#  Afterwards the command is available system-wide as `rapido-go-node`.
#
set -euo pipefail

RAPIDO_GO_NODE_VERSION="1.0.0"

# Only needed for a PRIVATE fork, or private ghcr.io images - this
# repository is public, so a normal install needs no token.
RAPIDO_REPO_TOKEN="${RAPIDO_REPO_TOKEN:-}"

load_saved_token() {
    [ -n "$RAPIDO_REPO_TOKEN" ] && return 0
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
    grep -qE '^RAPIDO_REPO_TOKEN=' "$APP_DIR/.env" && return 0
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

# Real escape bytes, not the literal text - printf expands either, but
# usage() prints through `cat`, which does not, and would show [1m
# verbatim. Producing the byte once here fixes every caller at once.
if [ -t 1 ]; then
    C_RESET=$(printf '\033[0m');  C_DIM=$(printf '\033[2m');    C_BOLD=$(printf '\033[1m')
    C_RED=$(printf '\033[0;31m'); C_GREEN=$(printf '\033[0;32m'); C_YELLOW=$(printf '\033[0;33m')
    C_CYAN=$(printf '\033[0;36m')
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
        GIT_TERMINAL_PROMPT=0 git -C "$APP_DIR" -c credential.helper= fetch --depth 1 origin "$REPO_BRANCH" \
            || { scrub_remote; die "Could not fetch. If the repo is private, set RAPIDO_REPO_TOKEN."; }
        git -C "$APP_DIR" reset --hard "origin/$REPO_BRANCH" >/dev/null
        scrub_remote
    else
        log "Downloading Rapido-Go into $APP_DIR..."
        mkdir -p "$(dirname "$APP_DIR")"
        # GIT_TERMINAL_PROMPT=0: fail with the die() message below instead of
        # hanging on a credential prompt with no terminal to answer it - see
        # rapido-go.sh's copy of this comment for how that was actually found.
        GIT_TERMINAL_PROMPT=0 git -c credential.helper= clone --depth 1 --branch "$REPO_BRANCH" "$(repo_url)" "$APP_DIR" >/dev/null 2>&1 \
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
        from_env="$(grep -E '^LISTEN_PORT=' "$APP_DIR/.env" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"' || true)"
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
    ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}' || true)"
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
        out=$(curl -fsS --max-time 20 --interface "$n" https://api.ipify.org 2>&1 || true)
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
    before="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1 || true)"
    "${EDITOR:-nano}" "$APP_DIR/.env"
    after="$(md5sum "$APP_DIR/.env" 2>/dev/null | cut -d' ' -f1 || true)"
    if [ "$before" != "$after" ]; then
        printf "\n"
        warn "Configuration changed. Apply it now? [Y/n] "
        local answer; read -r answer || answer=""
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
    local answer; read -r answer || answer=""
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
  RAPIDO_REPO_TOKEN         Only for a private fork or images
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
