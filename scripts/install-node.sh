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
