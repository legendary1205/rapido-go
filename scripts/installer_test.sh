#!/usr/bin/env bash
# Guards the exact failure that silently broke every token-less install:
# a helper whose last test is false returns that false status, and because
# the helper is called as a plain statement under `set -euo pipefail`, the
# whole installer exits mid-run with status 1 and NO error message.
# It was invisible while the repo was private, because everyone set
# RAPIDO_REPO_TOKEN and took the early return-0 path instead.
set -uo pipefail
fail=0
cd "$(dirname "$0")/.."
for f in rapido-go.sh rapido-go-node.sh; do
    # Source the script far enough to define its functions, without running
    # main: every one of these scripts ends in `main "$@"`, so strip it.
    sed '/^main "\$@"$/d' "$f" > /tmp/_src.sh
    out=$(bash -c '
        set -euo pipefail
        RAPIDO_REPO_TOKEN=""
        APP_DIR=/nonexistent-'"$$"'
        . /tmp/_src.sh >/dev/null 2>&1
        load_saved_token
        save_token
        echo REACHED_END
    ' 2>&1) || true
    if [ "$out" = "REACHED_END" ]; then
        echo "  ok   $f - token helpers survive a fresh, token-less install"
    else
        echo "  FAIL $f - installer would exit early (got: ${out:-<silence>})"
        fail=1
    fi
done
exit $fail
