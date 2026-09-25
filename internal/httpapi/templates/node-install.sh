#!/usr/bin/env bash
# Rapido node installer, served by the panel at {{.Origin}}/install/node.sh
# It contains no secret: the setup blob you pass is the only credential.
#
#   curl -fsSL {{.Origin}}/install/node.sh | sudo bash -s -- '<setup_blob>' [--no-tune]
#
[ -n "${BASH_VERSION:-}" ] || { echo "This installer needs bash. Run: curl -fsSL {{.Origin}}/install/node.sh | sudo bash -s -- '<setup_blob>'" >&2; exit 1; }
set -euo pipefail

PANEL_ORIGIN='{{.Origin}}'

if [ -t 1 ]; then
    BOLD=$'\033[1m'; RED=$'\033[31m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
else
    BOLD=''; RED=''; YELLOW=''; RESET=''
fi

TMP=''
ARCH=''
SECRET=''

step() { printf '%s==>%s %s\n' "$BOLD" "$RESET" "$*"; }
warn() { printf '%sWARNING:%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
fail() { printf '%sERROR:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

cleanup() {
    if [ -n "$TMP" ]; then
        rm -rf "$TMP"
    fi
}

# The secret goes to curl on stdin, not argv, so it never shows up in `ps`.
# No -L: an authenticated request must not follow a redirect to another host.
download() {
    local url="$1" dest="$2" code
    code="$(printf 'header = "Authorization: Bearer %s"\n' "$SECRET" |
        curl -sS --connect-timeout 15 --max-time 900 --retry 3 --retry-delay 2 \
            -o "$dest" -w '%{http_code}' -K - "$url")" || code='000'
    case "$code" in
        200) return 0 ;;
        401) fail "The panel rejected this node's secret. Was the node deleted or re-created? Copy a fresh install command from the panel's Nodes page." ;;
        404) fail "This panel has no node build for $ARCH (HTTP 404). Ask its administrator to publish the node binaries." ;;
        000) fail "Could not reach $PANEL_ORIGIN. Check this server's DNS and outbound HTTPS access." ;;
        *) fail "The panel answered HTTP $code for $url." ;;
    esac
}

ensure_curl() {
    command -v curl >/dev/null 2>&1 && return 0
    step "Installing curl"
    if command -v apt-get >/dev/null 2>&1; then
        DEBIAN_FRONTEND=noninteractive apt-get update -y </dev/null >/dev/null &&
            DEBIAN_FRONTEND=noninteractive apt-get install -y curl </dev/null >/dev/null || true
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y curl </dev/null >/dev/null || true
    elif command -v yum >/dev/null 2>&1; then
        yum install -y curl </dev/null >/dev/null || true
    fi
    command -v curl >/dev/null 2>&1 || fail "curl is missing and could not be installed. Install it and run this again."
}

# Everything runs from main, called on the last line: a download cut short
# executes nothing, and no command can swallow the rest of the script from
# stdin while it is being piped in.
main() {
    local blob="${1:-}" json expected actual bin rc=0

    printf '%sRapido node installer%s  (panel: %s)\n\n' "$BOLD" "$RESET" "$PANEL_ORIGIN"

    [ "$(id -u)" -eq 0 ] || fail "This installer must run as root. Use: curl -fsSL $PANEL_ORIGIN/install/node.sh | sudo bash -s -- '<setup_blob>'"
    [ "$(uname -s)" = Linux ] || fail "Nodes run on Linux only (this is $(uname -s))."
    command -v systemctl >/dev/null 2>&1 || fail "systemd was not found; the node agent installs as a systemd service."

    case "$blob" in
        ''|-*) fail "The setup blob is missing. Copy the full install command from the panel's Nodes page." ;;
    esac
    blob="${blob//[[:space:]]/}"
    [[ "$blob" =~ ^[A-Za-z0-9+/=]+$ ]] || fail "The setup blob is not valid base64. Copy the whole command again."

    case "$(uname -m)" in
        x86_64|amd64) ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) fail "Unsupported CPU '$(uname -m)'. Nodes run on x86_64 (amd64) and aarch64 (arm64)." ;;
    esac

    json="$(printf '%s' "$blob" | base64 -d 2>/dev/null)" || fail "The setup blob is not valid base64. Copy the whole command again."
    SECRET="$(printf '%s' "$json" | sed -n 's/.*"secret"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
    [[ "$SECRET" =~ ^[A-Za-z0-9._~+/=-]{8,200}$ ]] || fail "The setup blob does not carry a usable node secret. Copy the whole command again."

    ensure_curl
    command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is missing (part of coreutils); install it and run this again."

    TMP="$(mktemp -d /var/tmp/rapido-node-install.XXXXXX 2>/dev/null || mktemp -d)" || fail "Could not create a temporary directory."
    trap cleanup EXIT
    bin="$TMP/rapido-go-node"

    step "Downloading the node ($ARCH) from $PANEL_ORIGIN"
    download "$PANEL_ORIGIN/install/node/$ARCH.sha256" "$TMP/sum"
    download "$PANEL_ORIGIN/install/node/$ARCH" "$bin"

    read -r expected _ <"$TMP/sum" || true
    expected="${expected,,}"
    [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || fail "The panel sent an invalid checksum file."
    actual="$(sha256sum "$bin" | cut -d' ' -f1)"
    [ "$actual" = "$expected" ] || fail "Checksum mismatch: the download is damaged or was tampered with. Nothing was installed."

    chmod 755 "$bin"
    "$bin" version >/dev/null 2>&1 || fail "The downloaded node will not run on this server (wrong CPU, or the temp directory is mounted noexec)."

    step "Installing"
    "$bin" install --setup-blob "$blob" --panel-url "$PANEL_ORIGIN" "${@:2}" || rc=$?

    echo
    if [ "$rc" -eq 0 ]; then
        printf '%sNode ready.%s Manage it with:\n' "$BOLD" "$RESET"
        echo "  rapido-go-node status     service, panel link, tunnels, ports"
        echo "  rapido-go-node update     fetch the panel's current node build"
        echo "  rapido-go-node tunnels    bring up WireGuard exits, also at boot"
    else
        warn "The install did not finish (exit code $rc); see the messages above. Running the same command again is safe."
    fi
    exit "$rc"
}

main "$@"
