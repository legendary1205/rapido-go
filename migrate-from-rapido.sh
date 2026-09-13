#!/usr/bin/env bash
#
#  Migrate a Rapido / Marzban (Python + MySQL + Xray) panel to Rapido-Go.
#
#  Run this ON THE OLD PANEL SERVER. It reads that panel and pushes into a
#  Rapido-Go panel you have already installed somewhere else:
#
#    bash migrate-from-rapido.sh --target https://newpanel.example.com \
#                                --user admin --pass 'xxxxx'
#
#  It NEVER writes to the old panel - no schema change, no restart, not even
#  a file touched. The old panel keeps serving customers throughout, so you
#  can run this as many times as you like and cut DNS over only once you are
#  happy with what landed.
#
#  Three things have to move, and only the first is in the database dump.
#  Migrations that copy just the dump look like they worked and then serve
#  nobody, so this script does all three:
#
#    1. admins, users, proxies, hosts, inbounds, templates  (the MySQL dump)
#    2. what each inbound actually IS - protocol/network/TLS/ports. The
#       legacy MySQL schema never stored this; it lives only in the live
#       xray_config.json, whose path comes from the container's own
#       XRAY_JSON, not from whichever copy happens to sit in /opt.
#    3. each host's address and remark - the text customers see and the
#       name they dial. These are in neither the Xray config nor anything
#       the Xray importer can infer, only in the old `hosts` table.
#
set -euo pipefail

C_RESET=$(printf '\033[0m'); C_DIM=$(printf '\033[2m'); C_BOLD=$(printf '\033[1m')
C_RED=$(printf '\033[0;31m'); C_GREEN=$(printf '\033[0;32m')
C_YELLOW=$(printf '\033[0;33m'); C_CYAN=$(printf '\033[0;36m')

log()  { printf "${C_CYAN}▶${C_RESET} %s\n" "$*"; }
ok()   { printf "${C_GREEN}✔${C_RESET} %s\n" "$*"; }
warn() { printf "${C_YELLOW}!${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}✘ %s${C_RESET}\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

TARGET=""; TUSER=""; TPASS=""
PANEL_CONTAINER=""; MYSQL_CONTAINER=""; DB_NAME=""
WORK_DIR="${MIGRATE_WORK_DIR:-/var/tmp/rapido-migrate}"
DRY_RUN=0

usage() {
    cat <<EOF
${C_BOLD}migrate-from-rapido.sh${C_RESET} - move a Python Rapido/Marzban panel to Rapido-Go

  --target URL       Rapido-Go panel base URL      (required)
  --user NAME        a sudo admin on the TARGET    (required)
  --pass SECRET      that admin's password         (required)
  --panel NAME       old panel container   (default: autodetect)
  --mysql NAME       old MySQL container   (default: autodetect)
  --db NAME          old database name     (default: autodetect)
  --dry-run          export and report, push nothing
  -h, --help         this text

Run it on the OLD panel server. Nothing on the old panel is modified.
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --target) TARGET="${2:-}"; shift 2 ;;
        --user)   TUSER="${2:-}"; shift 2 ;;
        --pass)   TPASS="${2:-}"; shift 2 ;;
        --panel)  PANEL_CONTAINER="${2:-}"; shift 2 ;;
        --mysql)  MYSQL_CONTAINER="${2:-}"; shift 2 ;;
        --db)     DB_NAME="${2:-}"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "Unknown option: $1 (see --help)" ;;
    esac
done

[ -n "$TARGET" ] || { usage; exit 1; }
if [ "$DRY_RUN" -eq 0 ]; then
    [ -n "$TUSER" ] && [ -n "$TPASS" ] || die "--user and --pass are required unless --dry-run"
fi
TARGET="${TARGET%/}"

command -v docker >/dev/null 2>&1 || die "docker not found - run this on the old panel server."
command -v curl   >/dev/null 2>&1 || die "curl not found."
command -v python3 >/dev/null 2>&1 || die "python3 not found (used to read the Xray config and build JSON)."

# ── discover the old panel ───────────────────────────────────────────────────
# Autodetected rather than hard-coded: this same layout has shipped as
# "marzban", "rapido", one container or three, across the installs this has
# to handle.
discover() {
    # Identify the old panel by what it IS, not what it is called. Matching
    # on the name finds the wrong container the moment Rapido-Go is
    # installed on this same server (its own caddy/panel/backend all match
    # "rapido" too) - which is the normal case for an in-place migration.
    # The Python panel is the only container carrying XRAY_JSON or
    # SQLALCHEMY_DATABASE_URL in its environment.
    if [ -z "$PANEL_CONTAINER" ]; then
        local c
        for c in $(docker ps --format '{{.Names}}'); do
            if docker inspect "$c" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
                 | grep -qE '^(XRAY_JSON|SQLALCHEMY_DATABASE_URL)='; then
                PANEL_CONTAINER="$c"; break
            fi
        done
    fi
    [ -n "$PANEL_CONTAINER" ] || die "Could not find the old Python panel container - pass --panel NAME."

    if [ -z "$MYSQL_CONTAINER" ]; then
        MYSQL_CONTAINER=$(docker ps --format '{{.Names}}' | grep -iE 'mysql|mariadb' | head -1 || true)
    fi
    [ -n "$MYSQL_CONTAINER" ] || die "Could not find the MySQL container - pass --mysql NAME."

    if [ -z "$DB_NAME" ]; then
        DB_NAME=$(docker inspect "$PANEL_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' \
        | sed -n 's#^SQLALCHEMY_DATABASE_URL=.*/\([A-Za-z0-9_]*\).*#\1#p' | head -1 || true)
    fi
    [ -n "$DB_NAME" ] || DB_NAME="rapido"

    ok "Old panel:  $PANEL_CONTAINER"
    ok "Old MySQL:  $MYSQL_CONTAINER  (database: $DB_NAME)"
}

# mysql_q runs a query as root using the password already in the container's
# own environment, so no credential is ever typed here or echoed to a log.
mysql_q() {
    docker exec "$MYSQL_CONTAINER" sh -c \
        'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -N -B --default-character-set=utf8mb4 '"$DB_NAME"' -e "$1"' _ "$1" 2>/dev/null
}

# ── step 1: a consistent dump ────────────────────────────────────────────────
# --single-transaction is what makes this safe to run against a live panel:
# InnoDB gives the dump one consistent snapshot, so a user created halfway
# through is either wholly in it or wholly not - never half-written.
dump_database() {
    DUMP_FILE="$WORK_DIR/rapido-$(date +%Y%m%d-%H%M%S).sql.gz"
    log "Dumping $DB_NAME (consistent snapshot, old panel keeps running)..."
    docker exec "$MYSQL_CONTAINER" sh -c \
        'exec mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --single-transaction --routines --triggers --set-gtid-purged=OFF '"$DB_NAME" \
        2>/dev/null | gzip > "$DUMP_FILE"

    # A truncated dump restores *partially* and silently - exactly the way a
    # migration loses users. mysqldump writes this marker last, so its
    # presence is what proves the stream did not die midway.
    zcat "$DUMP_FILE" | tail -3 | grep -q "Dump completed" \
        || die "The dump is incomplete (no 'Dump completed' marker) - nothing was uploaded."
    ok "Dump complete: $DUMP_FILE ($(du -h "$DUMP_FILE" | cut -f1))"
}

# ── step 2: the LIVE Xray config ─────────────────────────────────────────────
# XRAY_JSON from the running container, never a copy found by guessing:
# every install of this panel has stale duplicates under /opt and /code, and
# importing one of those gives you an inbound set that has not matched
# reality for months.
export_xray_config() {
    local path
    path=$(docker inspect "$PANEL_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' \
        | sed -n 's/^XRAY_JSON=//p' | head -1)
    [ -n "$path" ] || path="/var/lib/rapido/xray_config.json"

    XRAY_FILE="$WORK_DIR/xray_config.json"
    docker cp "$PANEL_CONTAINER:$path" "$XRAY_FILE" 2>/dev/null \
        || die "Could not read the live Xray config at $path inside $PANEL_CONTAINER."
    python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$XRAY_FILE" \
        || die "The live Xray config is not valid JSON - refusing to import it."
    ok "Live Xray config: $path ($(python3 -c "
import json,sys
c=json.load(open(sys.argv[1]))
print('%d inbounds, %d outbounds' % (len(c.get('inbounds',[])), len(c.get('outbounds',[]))))
" "$XRAY_FILE"))"
}

# ── step 3: the real host rows ───────────────────────────────────────────────
# Address and remark decide what the customer dials and what they see in
# their client. The Xray config carries neither, so they come straight out
# of the old `hosts` table - joined to the live config for the port, because
# a legacy host row usually leaves port NULL and inherits the inbound's.
export_hosts() {
    HOSTS_FILE="$WORK_DIR/hosts.json"
    local raw="$WORK_DIR/hosts.tsv"
    mysql_q "SELECT id, inbound_tag, COALESCE(address,''), COALESCE(port,0), COALESCE(remark,''),
                    COALESCE(sni,''), COALESCE(host,''), COALESCE(path,''),
                    COALESCE(security,'inbound_default'), COALESCE(alpn,''), COALESCE(fingerprint,''),
                    COALESCE(is_disabled,0)
             FROM hosts ORDER BY id;" > "$raw"
    [ -s "$raw" ] || die "The old hosts table came back empty - aborting before importing a panel nobody can connect to."

    python3 - "$raw" "$XRAY_FILE" "$HOSTS_FILE" <<'PY'
import json, sys
raw, xray_path, out_path = sys.argv[1], sys.argv[2], sys.argv[3]

cfg = json.load(open(xray_path, encoding="utf-8"))
# A legacy host row's port is usually NULL - it inherited the inbound's. In
# Rapido-Go the HOST's port is what a node actually listens on, so an
# inherited one has to be made explicit here or every inbound comes up on
# the wrong port.
port_of_tag = {}
for inb in cfg.get("inbounds", []):
    p = inb.get("port")
    if isinstance(p, str):
        p = p.split(",")[0].strip()
    try:
        port_of_tag[inb.get("tag")] = int(p)
    except (TypeError, ValueError):
        pass

live_tags = set(port_of_tag)
by_tag, skipped, priority = {}, [], 0
for line in open(raw, encoding="utf-8"):
    line = line.rstrip("\n")
    if not line.strip():
        continue
    f = line.split("\t")
    if len(f) < 12:
        continue
    (_id, tag, address, port, remark, sni, host, path,
     security, alpn, fingerprint, disabled) = f[:12]

    # A host on a tag the live Xray config no longer has is a leftover from
    # an inbound that was deleted years ago. Importing it would recreate a
    # dead entry in every customer's client, so it is reported, not moved.
    if tag not in live_tags:
        skipped.append((tag, remark))
        continue

    port = int(port) if port.isdigit() and port != "0" else port_of_tag.get(tag, 0)

    def opt(v):
        return v if v not in ("", "NULL") else None

    priority += 1          # strictly unique: equal priorities make the
                           # dashboard's reorder arrows silently no-op
    by_tag.setdefault(tag, []).append({
        "id": 0,           # 0 = create
        "remark": remark,
        "address": address,
        "port": port,
        "path": opt(path),
        "sni": opt(sni),
        "host": opt(host),
        "security": security or "inbound_default",
        "alpn": alpn if alpn in ("none","h3","h2","http/1.1","h3,h2,http/1.1","h3,h2","h2,http/1.1") else "none",
        "fingerprint": fingerprint or "none",
        "allowinsecure": False,
        "is_disabled": disabled == "1",
        "mux_enable": False,
        "fragment_setting": None,
        "noise_setting": None,
        "random_user_agent": False,
        "use_sni_as_host": False,
        "priority": priority,
    })

json.dump(by_tag, open(out_path, "w", encoding="utf-8"), ensure_ascii=False)
print("%d host rows across %d tags" % (sum(len(v) for v in by_tag.values()), len(by_tag)))
for tag, remark in skipped:
    print("  skipped (tag '%s' is not in the live Xray config): %s" % (tag, remark))
PY
    ok "Host rows exported: $HOSTS_FILE"
}

# ── counting, so the result can actually be checked ──────────────────────────
source_counts() {
    mysql_q "SELECT CONCAT(
        (SELECT COUNT(*) FROM admins), ' ',
        (SELECT COUNT(*) FROM users),  ' ',
        (SELECT COUNT(*) FROM proxies));"
}

api() {  # api METHOD PATH [curl args...]
    local method="$1" path="$2"; shift 2
    curl -sk -X "$method" "$TARGET$path" -H "Authorization: Bearer $TOKEN" "$@"
}

login() {
    log "Logging in to $TARGET ..."
    TOKEN=$(curl -sk -X POST "$TARGET/api/admin/token" \
        --data-urlencode "username=$TUSER" --data-urlencode "password=$TPASS" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin).get("access_token",""))' 2>/dev/null || true)
    [ -n "$TOKEN" ] || die "Login failed on the target panel - check --user/--pass and that $TARGET is reachable."
    ok "Authenticated."
}

push() {
    log "1/3  Importing users, admins and proxies (this REPLACES them on the target)..."
    local res
    res=$(api POST "/api/settings/backup/restore-upload" -F "confirm=true" -F "file=@$DUMP_FILE")
    echo "$res" | grep -q '"detail"' && die "Import failed: $res"
    printf "     %s\n" "$res"

    log "2/3  Importing the live Xray config (what each inbound really is)..."
    python3 - "$XRAY_FILE" > "$WORK_DIR/import-xray.json" <<'PY'
import json, sys
print(json.dumps({"config": open(sys.argv[1], encoding="utf-8").read(), "confirm": True}))
PY
    res=$(api POST "/api/inbounds/import-xray" -H "Content-Type: application/json" \
              --data-binary "@$WORK_DIR/import-xray.json")
    echo "$res" | grep -q '"applied":true' || die "Xray import failed: $res"
    printf "     %s\n" "$res"

    # The database dump faithfully carries inbound rows that the live Xray
    # config abandoned long ago (a protocol the fleet stopped using, a tag
    # renamed years back). Left in place they show up in the dashboard as
    # real inbounds and their hosts land in customers' clients as dead
    # entries - and they collide with the unique priorities set below.
    log "Removing inbounds the live Xray config no longer has..."
    local live removed=0 tag
    live=$(python3 -c "
import json,sys
print(' '.join(i.get('tag','') for i in json.load(open(sys.argv[1], encoding='utf-8')).get('inbounds',[])))
" "$XRAY_FILE")
    # GET /api/inbounds is keyed by PROTOCOL, each value a list of inbound
    # objects - the tags are inside those, not in the dict's keys. Reading
    # the keys as tags silently compared "vless" against the real tag list
    # and so removed nothing at all.
    for tag in $(api GET "/api/inbounds" | python3 -c "
import json,sys
d=json.load(sys.stdin)
items=[x for v in d.values() for x in v] if isinstance(d,dict) else d
tags=[x.get('tag') if isinstance(x,dict) else x for x in items]
print(' '.join(t.replace(' ','%20') for t in tags if t))
" 2>/dev/null); do
        local plain="${tag//\%20/ }"
        case " $live " in
            *" $plain "*) ;;
            *) api DELETE "/api/inbounds/$tag" >/dev/null 2>&1 && { removed=$((removed+1)); printf "     removed stale inbound: %s
" "$plain"; } ;;
        esac
    done
    [ "$removed" -eq 0 ] && ok "No stale inbounds." || ok "$removed stale inbound(s) removed."

    log "3/3  Restoring the real host addresses and remarks..."
    res=$(api PUT "/api/hosts" -H "Content-Type: application/json" --data-binary "@$HOSTS_FILE")
    echo "$res" | grep -q '"detail"' && die "Host import failed: $res"
    ok "Hosts applied."
}

verify() {
    log "Verifying the target against the source..."
    local src tgt s_admins s_users s_proxies
    src=$(source_counts); read -r s_admins s_users s_proxies <<<"$src"
    tgt=$(api GET "/api/system" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("total_user",-1))' 2>/dev/null || echo -1)

    printf "     source: %s users (%s admins, %s proxies)\n" "$s_users" "$s_admins" "$s_proxies"
    printf "     target: %s users\n" "$tgt"
    if [ "$tgt" = "$s_users" ]; then
        ok "User counts match exactly."
    else
        warn "Counts differ. That is EXPECTED if the old panel kept creating users during the run -"
        warn "re-run this script at cutover time and the final sync will close the gap."
    fi
}

main() {
    mkdir -p "$WORK_DIR"; chmod 700 "$WORK_DIR"
    discover
    dump_database
    export_xray_config
    export_hosts

    if [ "$DRY_RUN" -eq 1 ]; then
        printf "\n"
        ok "Dry run - nothing was pushed. Exported files are in $WORK_DIR"
        exit 0
    fi

    login
    push
    verify

    printf "\n${C_BOLD}Next:${C_RESET}\n"
    printf "  1. Open the new dashboard and check a few real users and the host list.\n"
    printf "  2. Reinstall each node with rapido-go-node (the old node agent is not compatible).\n"
    printf "  3. Test one real subscription link end to end.\n"
    printf "  4. Re-run this script, THEN move DNS - the old panel keeps taking\n"
    printf "     writes until the domain moves, and that last gap is where users\n"
    printf "     otherwise go missing.\n\n"
    ok "Migration pass complete. The old panel was not modified."
}

main "$@"
