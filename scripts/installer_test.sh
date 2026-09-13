#!/usr/bin/env bash
#
# Guards the failure mode that silently broke every token-less install: a
# helper whose last command legitimately returns non-zero (a test that is
# false, or a `grep` that simply finds nothing under `pipefail`) is called
# as a plain statement, so `set -euo pipefail` exits the whole installer
# mid-run with status 1 and NO message at all.
#
# Both cases below were real, and the second survived the first fix because
# it only triggers once .env EXISTS but carries no token line - which is
# exactly the state the installer itself creates a few steps earlier.
# Invisible while the repo was private: everyone set RAPIDO_REPO_TOKEN and
# took the early return-0 path.
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
run_case() {
    local script="$1" label="$2" envsetup="$3"
    sed '/^main "\$@"$/d' "$script" > /tmp/_installer_src.sh
    local out
    out=$(bash -c '
        set -euo pipefail
        RAPIDO_REPO_TOKEN=""
        APP_DIR=$(mktemp -d)
        '"$envsetup"'
        . /tmp/_installer_src.sh >/dev/null 2>&1
        load_saved_token
        save_token
        load_saved_token
        echo REACHED_END
        rm -rf "$APP_DIR"
    ' 2>&1) || true
    if [ "$out" = "REACHED_END" ]; then
        echo "  ok   $script - $label"
    else
        echo "  FAIL $script - $label (got: ${out:-<silence>})"
        fail=1
    fi
}

for f in rapido-go.sh rapido-go-node.sh; do
    # Fresh server: no .env at all.
    run_case "$f" "no .env yet" ':'
    # After generate_env: .env exists, but has no token line in it - this is
    # the state a real public install reaches, and the one that used to die.
    run_case "$f" ".env exists without a token line" 'printf "POSTGRES_PASSWORD=x\nSUDO_PASSWORD=y\n" > "$APP_DIR/.env"'
done

rm -f /tmp/_installer_src.sh
exit $fail
