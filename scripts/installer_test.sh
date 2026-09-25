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
#
# The second half exercises the panel installer's pure helpers (flag parsing,
# domain handling, sizing, Caddyfile/.env rendering, preflight arithmetic, the
# parallel pull and lightweight fetch logic). None of it needs root, Docker or
# a network: the script is sourced with its final `main "$@"` removed, and
# anything that would touch the outside world is stubbed per case.
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# A copy of the script without its final `main "$@"`, safe to source.
prepare() {
    local out="$WORK/$(basename "$1")"
    sed '/^main "\$@"$/d' "$1" > "$out"
    printf '%s' "$out"
}

run_case() {
    local script="$1" label="$2" envsetup="$3" src out
    src="$(prepare "$script")"
    out=$(bash -c '
        set -euo pipefail
        RAPIDO_REPO_TOKEN=""
        APP_DIR=$(mktemp -d)
        '"$envsetup"'
        . "'"$src"'" >/dev/null 2>&1
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

for f in rapido-go.sh; do
    # Fresh server: no .env at all.
    run_case "$f" "no .env yet" ':'
    # After generate_env: .env exists, but has no token line in it - this is
    # the state a real public install reaches, and the one that used to die.
    run_case "$f" ".env exists without a token line" 'printf "POSTGRES_PASSWORD=x\nSUDO_PASSWORD=y\n" > "$APP_DIR/.env"'
done

# ── panel installer helpers ──────────────────────────────────────────────────
PANEL_SRC="$(prepare rapido-go.sh)"

# A working Python (a bare `python3` can be a Windows Store stub that exists
# but does not run), and whether this filesystem keeps chmod modes.
PY=""
for p in python3 python; do
    if "$p" -c 'pass' >/dev/null 2>&1; then PY="$p"; break; fi
done
probe="$WORK/chmod-probe"; : > "$probe"; chmod 600 "$probe"
CHMOD_OK=0; [ "$(stat -c %a "$probe" 2>/dev/null)" = "600" ] && CHMOD_OK=1

# run_unit <code>: run bash code with rapido-go.sh sourced, errexit on.
run_unit() {
    RAPIDO_GO_APP_DIR="$WORK/app" bash -c 'set -euo pipefail; . "$1" >/dev/null 2>&1; eval "$2"' _ "$PANEL_SRC" "$1" 2>&1
}

# unit <label> <expected stdout> <code>
unit() {
    local out
    out="$(run_unit "$3")" || true
    if [ "$out" = "$2" ]; then
        echo "  ok   rapido-go.sh - $1"
    else
        echo "  FAIL rapido-go.sh - $1"
        echo "       expected: $2"
        echo "       got:      $out"
        fail=1
    fi
}

# unit_has <label> <needle> <code>: stdout must contain the fixed string.
unit_has() {
    local out
    out="$(run_unit "$3")" || true
    if printf '%s\n' "$out" | grep -qF -- "$2"; then
        echo "  ok   rapido-go.sh - $1"
    else
        echo "  FAIL rapido-go.sh - $1 (missing: $2)"
        printf '%s\n' "$out" | sed 's/^/       | /'
        fail=1
    fi
}

# unit_lacks <label> <needle> <code>: stdout must NOT contain the fixed string.
unit_lacks() {
    local out
    out="$(run_unit "$3")" || true
    if printf '%s\n' "$out" | grep -qF -- "$2"; then
        echo "  FAIL rapido-go.sh - $1 (should not contain: $2)"
        printf '%s\n' "$out" | sed 's/^/       | /'
        fail=1
    else
        echo "  ok   rapido-go.sh - $1"
    fi
}

# unit_fails <label> <code>: the code must exit non-zero.
unit_fails() {
    if run_unit "$2" >/dev/null; then
        echo "  FAIL rapido-go.sh - $1 (expected a non-zero exit)"
        fail=1
    else
        echo "  ok   rapido-go.sh - $1"
    fi
}

# ---- flag parsing ----
unit "install flags are all parsed" \
    'a.example.com|b.example.com,c.example.com|s.example.com|bob|S3cret pass|ghp_abc123|1|1' \
    'parse_install_args --domain a.example.com --extra-domains b.example.com,c.example.com --sub-domain s.example.com --admin-user bob --admin-pass "S3cret pass" --token ghp_abc123 --yes --skip-preflight
     echo "$RAPIDO_DOMAIN|$RAPIDO_EXTRA_DOMAINS|$RAPIDO_SUB_DOMAIN|$RAPIDO_ADMIN_USER|$RAPIDO_ADMIN_PASS|$RAPIDO_REPO_TOKEN|$RAPIDO_YES|$RAPIDO_SKIP_PREFLIGHT"'
unit "--flag=value form is accepted" \
    'x.example.com|s.example.com' \
    'parse_install_args --domain=x.example.com --sub-domain=s.example.com; echo "$RAPIDO_DOMAIN|$RAPIDO_SUB_DOMAIN"'
unit "a flag overrides the environment" \
    'flag.example.com' \
    'RAPIDO_DOMAIN=env.example.com; parse_install_args --domain flag.example.com; echo "$RAPIDO_DOMAIN"'
unit "the environment is used when no flag is given" \
    'env.example.com' \
    'RAPIDO_DOMAIN=env.example.com; parse_install_args --yes; echo "$RAPIDO_DOMAIN"'
unit_fails "an unknown install option is refused" 'parse_install_args --bogus'
unit_has   "the unknown option is named in the error" 'Unknown option for install: --bogus' 'parse_install_args --bogus'
unit_fails "a flag with no value is refused" 'parse_install_args --domain'

# ---- domains ----
unit "host list is lowercased, trimmed and de-duplicated" \
    'a.example.com,b.example.com' \
    'normalize_host_list "A.Example.com, b.example.com ,a.example.com,"'
unit "a URL is reduced to its host" \
    'panel.example.com' \
    'normalize_host_list "https://Panel.example.com:8443/dashboard/"'
unit "an empty list stays empty" '' 'normalize_host_list ""'
unit_fails "a bad host name is refused" 'normalize_host_list "good.example.com,not_a_host"'
unit_fails "a bare IP is not a valid domain" '_valid_host 203.0.113.7'
unit_fails "a single label is not a valid domain" '_valid_host localhost'
unit "a normal domain is valid" 'yes' '_valid_host sub.panel-1.example.com && echo yes'
unit "extra/sub lists drop names that already have a site block" \
    'a.example.com|b.example.com|s.example.com' \
    'RAPIDO_DOMAIN=A.example.com RAPIDO_EXTRA_DOMAINS="a.example.com,b.example.com" RAPIDO_SUB_DOMAIN="b.example.com,s.example.com"
     finalize_domains; echo "$RAPIDO_DOMAIN|$RAPIDO_EXTRA_DOMAINS|$RAPIDO_SUB_DOMAIN"'
unit "all_domains lists panel, extra, then subscription names" \
    'a.example.com,b.example.com,s.example.com' \
    'RAPIDO_DOMAIN=a.example.com RAPIDO_EXTRA_DOMAINS=b.example.com RAPIDO_SUB_DOMAIN=s.example.com; all_domains'
unit "admin password: a single quote is refused" \
    'refused' \
    '_valid_admin_pass "it'"'"'s-bad-1" && echo accepted || echo refused'
unit "admin password: a normal one passes" 'ok' '_valid_admin_pass "Good-pass-123" && echo ok'
unit_fails "admin password: too short is refused" '_valid_admin_pass short'

# ---- generated password ----
unit "generated password is 22 letters/digits" \
    'ok' \
    'p="$(gen_password)"; [ "${#p}" -eq 22 ] && case "$p" in *[!A-Za-z0-9]*) echo bad ;; *) echo ok ;; esac'
unit "two generated passwords differ" \
    'different' \
    '[ "$(gen_password)" != "$(gen_password)" ] && echo different'

# ---- sizing ----
unit "postgres tuning: 512 MB machine is clamped up"    '128MB 512MB'   'pg_tuning_for_ram_mb 512'
unit "postgres tuning: 1 GB machine"                    '256MB 614MB'   'pg_tuning_for_ram_mb 1024'
unit "postgres tuning: 4 GB machine"                    '1024MB 2457MB' 'pg_tuning_for_ram_mb 4096'
unit "postgres tuning: 16 GB machine is clamped down"   '2048MB 8192MB' 'pg_tuning_for_ram_mb 16384'
unit "postgres tuning: unknown RAM falls back to 2 GB"  '512MB 1228MB'  'pg_tuning_for_ram_mb 0'

# ---- preflight arithmetic ----
unit "RAM below the minimum fails"        'fail'    'level_for_value 700 900 1800'
unit "RAM between minimum and comfort warns" 'warn' 'level_for_value 1024 900 1800'
unit "plenty of RAM is ok"                'ok'      'level_for_value 4096 900 1800'
unit "a non-number is unknown, not a crash" 'unknown' 'level_for_value "" 900 1800'
unit "x86_64 is amd64"      'amd64' 'normalize_arch x86_64'
unit "aarch64 is arm64"     'arm64' 'normalize_arch aarch64'
unit "armv7l is unsupported" ''     'normalize_arch armv7l'
unit "IPv4 validation"      'ok|bad|bad|bad' \
    '_valid_ipv4 203.0.113.7 && printf ok; printf "|"; _valid_ipv4 999.1.1.1 || printf bad; printf "|"; _valid_ipv4 1.2.3 || printf bad; printf "|"; _valid_ipv4 a.b.c.d || printf bad'
unit "preflight failures become warnings with --skip-preflight" \
    'continued' \
    'RAPIDO_SKIP_PREFLIGHT=1; preflight_fail "too small" >/dev/null; echo continued'
unit_fails "a preflight failure stops the install" 'RAPIDO_SKIP_PREFLIGHT=0; preflight_fail "too small"'
unit "days until a date" '10' 'days_until_date "Jan 11 00:00:00 2030 GMT" "$(date -d "Jan 1 00:00:00 2030 GMT" +%s)"'
unit "days until a past date is negative" '-5' 'days_until_date "Jan 1 00:00:00 2030 GMT" "$(date -d "Jan 6 00:00:00 2030 GMT" +%s)"'

# Ports: only when python3 can open a listener to test against.
if [ -n "$PY" ]; then
    port="$("$PY" -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
    "$PY" -c "
import socket, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', $port)); s.listen(1); time.sleep(20)" &
    listener=$!
    sleep 1
    unit "port_in_use sees a listener" 'busy' "port_in_use $port && echo busy"
    kill "$listener" 2>/dev/null; wait "$listener" 2>/dev/null
    unit "port_in_use sees a free port" 'free' "port_in_use $port || echo free"
else
    echo "  skip rapido-go.sh - port_in_use (no working python)"
fi

# ---- .env reading ----
unit "env_get strips quotes and takes the last value" \
    'two|fallback' \
    'f="$(mktemp)"; printf "A=\"one\"\nA=\x27two\x27\n" > "$f"; printf "%s|" "$(env_get A "" "$f")"; env_get MISSING fallback "$f"'
unit "env_get on a missing file gives the default" 'dflt' 'env_get X dflt /nonexistent/file'

# ---- Caddyfile ----
CADDY_ONE='RAPIDO_DOMAIN=panel.example.com RAPIDO_EXTRA_DOMAINS="" RAPIDO_SUB_DOMAIN=""'
CADDY_MANY='RAPIDO_DOMAIN=panel.example.com RAPIDO_EXTRA_DOMAINS="panel2.example.com,panel3.example.com" RAPIDO_SUB_DOMAIN="sub.example.com,sub2.example.com"'
unit "caddyfile: single domain, compressed and proxied" \
    'panel.example.com {
	encode zstd gzip
	reverse_proxy panel:8000
}' \
    "$CADDY_ONE"'; render_caddyfile'
unit_has   "caddyfile: extra domains share the panel block" 'panel.example.com, panel2.example.com, panel3.example.com {' "$CADDY_MANY"'; render_caddyfile'
unit_has   "caddyfile: subscription domains get their own block" 'sub.example.com, sub2.example.com {' "$CADDY_MANY"'; render_caddyfile'
unit_has   "caddyfile: subscription block only proxies subscriber paths" '@subscriber path / /sub/* /statics/* /health' "$CADDY_MANY"'; render_caddyfile'
unit_has   "caddyfile: subscription block answers 404 for the rest" 'respond "Not found" 404' "$CADDY_MANY"'; render_caddyfile'
unit_lacks "caddyfile: no subscription block without a subscription domain" '@subscriber' "$CADDY_ONE"'; render_caddyfile'
unit "caddyfile: both blocks compress" '2' "$CADDY_MANY"'; render_caddyfile | grep -c "encode zstd gzip"'
# (grep -c reads everything - `grep -q` would exit early and hand the writer a
# SIGPIPE, which pipefail turns into a failure.)
unit "caddyfile uses tabs for indentation" 'yes' "$CADDY_MANY"'; [ "$(render_caddyfile | grep -cP "^\t")" -gt 0 ] && echo yes'
unit "an existing caddyfile that differs is kept as a backup" \
    '1' \
    'APP_DIR="$(mktemp -d)"; RAPIDO_DOMAIN=new.example.com RAPIDO_EXTRA_DOMAINS="" RAPIDO_SUB_DOMAIN=""
     printf "old.example.com {\n}\n" > "$APP_DIR/Caddyfile"; write_caddyfile >/dev/null 2>&1
     ls "$APP_DIR"/Caddyfile.bak-* | wc -l'

# A hand-written Caddyfile (global options, a snippet, comma lists, a scheme, a
# port-only address) - doctor must see exactly the real site names in it.
HAND_CADDY='APP_DIR="$(mktemp -d)"; cat > "$APP_DIR/Caddyfile" <<CF
{
	email admin@example.com
}
(common) {
	encode gzip
}
# comment {
panel.example.com, panel2.example.com {
	import common
	reverse_proxy panel:8000
}
https://sub.example.com,sub2.example.com {
	reverse_proxy panel:8000
}
:8080 {
	respond "x"
}
CF
'
unit "caddyfile_hosts finds the real site names in a hand-written file" \
    'panel.example.com panel2.example.com sub.example.com sub2.example.com' \
    "$HAND_CADDY"'caddyfile_hosts | tr "\n" " " | sed "s/ \$//"'
unit "doctor_names merges .env names with the Caddyfile, primary first, no duplicates" \
    'a.example.com panel.example.com panel2.example.com sub.example.com sub2.example.com' \
    "$HAND_CADDY"'RAPIDO_DOMAIN=a.example.com RAPIDO_EXTRA_DOMAINS=panel.example.com RAPIDO_SUB_DOMAIN=""; doctor_names'
unit "caddyfile_hosts with no Caddyfile prints nothing" '' 'APP_DIR="$(mktemp -d)"; caddyfile_hosts'

# ---- .env ----
ENV_ONE='RAPIDO_DOMAIN=panel.example.com RAPIDO_EXTRA_DOMAINS="" RAPIDO_SUB_DOMAIN=""'
ENV_SUB='RAPIDO_DOMAIN=panel.example.com RAPIDO_EXTRA_DOMAINS="panel2.example.com" RAPIDO_SUB_DOMAIN="sub.example.com,sub2.example.com"'
unit_has   "env: domain" 'RAPIDO_DOMAIN="panel.example.com"' "$ENV_ONE"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit_has   "env: postgres tuning" 'POSTGRES_SHARED_BUFFERS="256MB"' "$ENV_ONE"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit_has   "env: effective cache size" 'POSTGRES_EFFECTIVE_CACHE_SIZE="614MB"' "$ENV_ONE"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit_has   "env: the password is single-quoted so compose does not expand \$" "SUDO_PASSWORD='pa\$\$word1'" "$ENV_ONE"'; render_env dbpw admin '"'"'pa$$word1'"'"' 256MB 614MB 203.0.113.7'
unit_has   "env: subscription prefix is the panel domain by default" 'XRAY_SUBSCRIPTION_URL_PREFIX="https://panel.example.com"' "$ENV_ONE"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit_has   "env: a single address leaves the prefixes list blank" 'XRAY_SUBSCRIPTION_URL_PREFIXES=""' "$ENV_ONE"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit_has   "env: subscription prefix moves to the subscription domain" 'XRAY_SUBSCRIPTION_URL_PREFIX="https://sub.example.com"' "$ENV_SUB"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit_has   "env: several subscription domains are listed in order" 'XRAY_SUBSCRIPTION_URL_PREFIXES="https://sub.example.com,https://sub2.example.com"' "$ENV_SUB"'; render_env dbpw admin pw12345678 256MB 614MB 203.0.113.7'
unit "env: it parses as a shell-style env file and keeps the password intact" \
    'pa$$word1|panel.example.com' \
    "$ENV_ONE"'; f="$(mktemp)"; render_env dbpw admin '"'"'pa$$word1'"'"' 256MB 614MB 203.0.113.7 > "$f"
     printf "%s|%s" "$(sed -n "s/^SUDO_PASSWORD=\x27\(.*\)\x27\$/\1/p" "$f")" "$(env_get RAPIDO_DOMAIN "" "$f")"'

# ---- the GitHub token never appears in a process argument ----
TOKEN_STUB='curl() { echo "ARGS:$*"; cat; }; _curl_gh "ghp_Secret123" -s https://example.invalid/x'
unit_has   "the token reaches curl as a config header on stdin" 'header = "Authorization: Bearer ghp_Secret123"' "$TOKEN_STUB"
unit "the token is not in curl's arguments" 'ARGS:-K - -s https://example.invalid/x' "$TOKEN_STUB"' | grep "^ARGS:"'
unit "a token with quote/newline characters cannot inject config" 'header = "Authorization: Bearer ghp_xurlhttpevilexample"' \
    'curl() { cat; }; _curl_gh "$(printf "ghp_x\"\nurl = \"http://evil.example")" -s https://example.invalid/x'
unit "no token means no auth header at all" 'ARGS:-s https://example.invalid/x' \
    'curl() { echo "ARGS:$*"; }; _curl_gh "" -s https://example.invalid/x'

# ---- lightweight fetch ----
unit "lite fetch places the files and rejects nothing valid" \
    'ok|1|1' \
    'APP_DIR="$(mktemp -d)"
     fetch_repo_file() { case "$1" in
         docker-compose.prod.yml) printf "services:\n  a: {}\n" > "$2" ;;
         rapido-go.sh) printf "#!/usr/bin/env bash\ntrue\n" > "$2" ;;
         *) : > "$2" ;; esac; }
     _curl_gh() { return 1; }
     fetch_source_lite >/dev/null 2>&1
     printf "ok|%s|%s" "$(grep -c "^services:" "$APP_DIR/docker-compose.prod.yml")" "$([ -x "$APP_DIR/rapido-go.sh" ] && echo 1)"'
unit "lite fetch refuses an error page and leaves the install alone" \
    'kept' \
    'APP_DIR="$(mktemp -d)"; printf "services:\n  keep: {}\n" > "$APP_DIR/docker-compose.prod.yml"
     fetch_repo_file() { printf "<html>captive portal</html>\n" > "$2"; }
     _curl_gh() { return 1; }
     ( fetch_source_lite ) >/dev/null 2>&1 || true
     grep -q "keep:" "$APP_DIR/docker-compose.prod.yml" && echo kept'
unit "lite fetch fails cleanly when a download fails" \
    'failed|no leftovers' \
    'APP_DIR="$(mktemp -d)"
     fetch_repo_file() { return 22; }
     _curl_gh() { return 1; }
     ( fetch_source_lite ) >/dev/null 2>&1 && echo unexpected || printf "failed|"
     [ -z "$(ls -A "$APP_DIR" | grep "^.fetch" || true)" ] && echo "no leftovers"'
unit "a directory without .git is a lite install" 'lite' 'APP_DIR="$(mktemp -d)"; detect_install_mode'
unit "a git checkout keeps using git" 'git' 'APP_DIR="$(mktemp -d)"; mkdir "$APP_DIR/.git"; detect_install_mode'
unit "building locally needs the full checkout" 'git' 'APP_DIR="$(mktemp -d)"; RAPIDO_BUILD_LOCALLY=1 detect_install_mode'

# ---- parallel image pull ----
unit "pull_images reports only the image that failed" \
    'b:2' \
    'image_list() { printf "a:1\nb:2\nc:3\n"; }
     docker() { if [ "$*" = "pull -q b:2" ]; then echo "denied: not allowed" >&2; return 1; fi; return 0; }
     pull_images >/dev/null 2>&1; echo "$PULL_FAILED"'
unit "pull_images pulls concurrently" \
    'fast' \
    'image_list() { printf "a:1\nb:2\nc:3\nd:4\n"; }
     docker() { sleep 1; return 0; }
     s=$SECONDS; pull_images >/dev/null 2>&1; [ $((SECONDS - s)) -lt 3 ] && echo fast'
unit "pull_images leaves the panel image to the local build" \
    'other:1' \
    'image_list() { printf "ghcr.io/x/rapido-go-panel:latest\nother:1\n"; }
     docker() { echo "$*" >> "$WORK_LOG"; return 0; }
     WORK_LOG="$(mktemp)"; RAPIDO_BUILD_LOCALLY=1 pull_images >/dev/null 2>&1; sed "s/pull -q //" "$WORK_LOG"'

# ---- what happens when a pull fails ----
unit "a failed refresh of an image that is already here is only a warning" 'ready' \
    'pull_images() { PULL_FAILED="postgres:16-alpine"; }; docker() { return 0; }
     APP_DIR="$(mktemp -d)"; obtain_images >/dev/null 2>&1 && echo ready'
unit_fails "a failed pull of an image that is not here stops the install" \
    'pull_images() { PULL_FAILED="postgres:16-alpine"; }; docker() { return 1; }
     APP_DIR="$(mktemp -d)"; obtain_images'
unit_fails "a lite install with no panel image stops with instructions" \
    'pull_images() { PULL_FAILED="ghcr.io/x/rapido-go-panel:latest"; }; docker() { return 0; }
     APP_DIR="$(mktemp -d)"; obtain_images'
unit "a git install falls back to building the panel image" 'BUILD' \
    'pull_images() { PULL_FAILED="ghcr.io/x/rapido-go-panel:latest"; }; docker() { return 0; }
     build_local_image() { echo BUILD; }
     APP_DIR="$(mktemp -d)"; mkdir "$APP_DIR/.git"; obtain_images 2>&1 | grep -x BUILD'
unit "RAPIDO_BUILD_LOCALLY builds instead of pulling the panel image" 'BUILD' \
    'pull_images() { PULL_FAILED=""; }; build_local_image() { echo BUILD; }
     APP_DIR="$(mktemp -d)"; RAPIDO_BUILD_LOCALLY=1 obtain_images 2>&1 | grep -x BUILD'

# ---- update decision ----
unit "update is needed when the compose file changed" 'needed' \
    'COMPOSE_CHANGED=1; images_fingerprint() { echo same; }; update_needed same && echo needed'
unit "update is needed when images changed" 'needed' \
    'COMPOSE_CHANGED=0; images_fingerprint() { echo new; }; missing_services() { echo; }; update_needed old && echo needed'
unit "update is skipped when nothing changed" 'skipped' \
    'COMPOSE_CHANGED=0; images_fingerprint() { echo same; }; missing_services() { echo; }
     compose() { case "$1" in ps) echo abc123 ;; esac; }
     image_list() { echo ghcr.io/x/rapido-go-panel:latest; }
     docker() { case "$1" in inspect) echo sha256:aaa ;; image) echo sha256:aaa ;; esac; }
     update_needed same || echo skipped'
unit "update is needed when the running container is on an older image" 'needed' \
    'COMPOSE_CHANGED=0; images_fingerprint() { echo same; }; missing_services() { echo; }
     compose() { case "$1" in ps) echo abc123 ;; esac; }
     image_list() { echo ghcr.io/x/rapido-go-panel:latest; }
     docker() { case "$1" in inspect) echo sha256:old ;; image) echo sha256:new ;; esac; }
     update_needed same && echo needed'

# ---- token handling ----
unit "a public repository drops the token and does not store it" \
    'dropped|0|' \
    'github_status() { echo 200; }; ghcr_image_visibility() { echo public; }
     APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=ghp_stale
     resolve_repo_access strict >/dev/null 2>&1
     printf "dropped|%s|%s" "$TOKEN_NEEDED" "$RAPIDO_REPO_TOKEN"'
unit "a public repository with a private image keeps the token" \
    'kept|1' \
    'github_status() { echo 200; }; ghcr_image_visibility() { echo private; }
     APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=ghp_good
     resolve_repo_access strict >/dev/null 2>&1
     printf "kept|%s" "$TOKEN_NEEDED"'
unit "an image that cannot be checked keeps the token that is already in hand" \
    'kept|1' \
    'github_status() { echo 200; }; ghcr_image_visibility() { echo unknown; }
     APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=ghp_good
     resolve_repo_access strict >/dev/null 2>&1
     printf "kept|%s" "$TOKEN_NEEDED"'
unit "a rate-limited GitHub with no token is treated as public" \
    '0' \
    'github_status() { echo 403; }; ghcr_image_visibility() { echo public; }
     APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=""
     resolve_repo_access strict >/dev/null 2>&1
     echo "$TOKEN_NEEDED"'
unit_fails "a private repository with no token and no terminal stops with a message" \
    'github_status() { echo 404; }; APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=""; resolve_repo_access strict'
unit "a rejected token is refused" 'refused' \
    'github_status() { if [ -z "$1" ]; then echo 404; else echo 401; fi; }
     APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=ghp_bad
     ( resolve_repo_access strict ) >/dev/null 2>&1 || echo refused'
unit "update mode never stops on an unusable token" 'continued' \
    'github_status() { if [ -z "$1" ]; then echo 404; else echo 401; fi; }
     APP_DIR="$(mktemp -d)"; RAPIDO_REPO_TOKEN=ghp_bad
     resolve_repo_access lenient >/dev/null 2>&1; echo continued'
unit "a new token replaces a saved one" 'ghp_new' \
    'APP_DIR="$(mktemp -d)"; printf "A=1\nRAPIDO_REPO_TOKEN=\"ghp_old\"\nB=2\n" > "$APP_DIR/.env"
     TOKEN_NEEDED=1; RAPIDO_REPO_TOKEN=ghp_new; save_token; env_get RAPIDO_REPO_TOKEN "" "$APP_DIR/.env"'
if [ "$CHMOD_OK" = "1" ]; then
    unit "the saved token file stays private" '600' \
        'APP_DIR="$(mktemp -d)"; printf "A=1\n" > "$APP_DIR/.env"; chmod 644 "$APP_DIR/.env"
         TOKEN_NEEDED=1; RAPIDO_REPO_TOKEN=ghp_new; save_token; stat -c %a "$APP_DIR/.env"'
else
    echo "  skip rapido-go.sh - saved token file mode (this filesystem ignores chmod)"
fi

# ---- confirm / no terminal ----
unit "confirm answers the default when there is no terminal" 'yes|no' \
    'confirm "go?" y </dev/null && printf yes; printf "|"; confirm "go?" n </dev/null && printf yes || printf no'
unit_has "an existing .env keeps its admin login and says so" 'admin login is kept' \
    'APP_DIR="$(mktemp -d)"; printf "RAPIDO_DOMAIN=\"a.example.com\"\nSUDO_PASSWORD=x\n" > "$APP_DIR/.env"
     RAPIDO_DOMAIN=""; RAPIDO_ADMIN_PASS="whatever-123"; resolve_inputs </dev/null 2>&1'
unit_has "a different --domain over an existing .env is called out" 'names a different domain' \
    'APP_DIR="$(mktemp -d)"; printf "RAPIDO_DOMAIN=\"a.example.com\"\nSUDO_PASSWORD=x\n" > "$APP_DIR/.env"
     RAPIDO_DOMAIN="b.example.com"; resolve_inputs </dev/null 2>&1'
unit "an existing .env yields no new password" 'none' \
    'APP_DIR="$(mktemp -d)"; printf "RAPIDO_DOMAIN=\"a.example.com\"\nSUDO_PASSWORD=x\n" > "$APP_DIR/.env"
     RAPIDO_DOMAIN=""; resolve_inputs </dev/null >/dev/null 2>&1; echo "${ADMIN_PASSWORD:-none}"'
unit_fails "install with no domain and no terminal stops instead of hanging" \
    'RAPIDO_DOMAIN=""; APP_DIR="$(mktemp -d)"; resolve_inputs </dev/null'

# ---- help screen ----
if [ -n "$PY" ]; then
    width_out="$(run_unit 'usage' | PYTHONIOENCODING=utf-8 "$PY" -c '
import io, sys
data = io.TextIOWrapper(sys.stdin.buffer, encoding="utf-8")
widths = {len(l.rstrip("\r\n")) for l in data if l.startswith(("│", "╭", "├", "╰"))}
print("aligned" if len(widths) == 1 else "ragged: %s" % sorted(widths))')" || true
    if [ "$width_out" = "aligned" ]; then
        echo "  ok   rapido-go.sh - the help box lines are all the same width"
    else
        echo "  FAIL rapido-go.sh - the help box lines are all the same width ($width_out)"
        fail=1
    fi
else
    echo "  skip rapido-go.sh - help box alignment (no working python)"
fi
unit_has "help lists doctor" 'doctor' 'usage'

# ---- files that are run on Linux must be LF ----
for f in rapido-go.sh migrate-from-rapido.sh scripts/installer_test.sh docker-compose.prod.yml Caddyfile.example; do
    [ -f "$f" ] || continue
    if [ "$(tr -cd '\r' < "$f" | wc -c)" -eq 0 ]; then
        echo "  ok   $f - LF line endings"
    else
        echo "  FAIL $f - contains CR characters (CRLF breaks it on Linux)"
        fail=1
    fi
done

exit $fail
