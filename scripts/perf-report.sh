#!/usr/bin/env bash
# Read-only performance snapshot of a running Rapido panel stack. Run it on the
# panel server, ideally from /opt/rapido-go:  bash scripts/perf-report.sh
# Takes ~35 s (a 30 s CPU sampling window). Every database statement is a
# SELECT run in a read-only session; nothing is written, restarted or deleted.
# RAPIDO_DIR overrides the compose directory, LOG_WINDOW the HTTP log window.
set -uo pipefail
cd "${RAPIDO_DIR:-/opt/rapido-go}" 2>/dev/null || cd "$(dirname "$0")/.." || true

if [ -f docker-compose.prod.yml ]; then
  dc() { docker compose -f docker-compose.prod.yml --env-file .env "$@"; }
else
  dc() { docker compose "$@"; }
fi
psql() { dc exec -T -e PGOPTIONS='-c default_transaction_read_only=on' postgres psql -U rapido -d rapido -X -P footer=off -c "$1"; }
redis() { dc exec -T redis redis-cli "$@"; }
section() { printf '\n== %s ==\n' "$1"; }
LOG_WINDOW="${LOG_WINDOW:-15m}"

section "Containers (CPU / memory)"
docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}\t{{.BlockIO}}' $(dc ps -q) 2>&1

section "Postgres size, cache hit ratio, WAL written"
psql "SELECT pg_size_pretty(pg_database_size(current_database())) AS db_size,
  round(100.0 * sum(blks_hit) / nullif(sum(blks_hit) + sum(blks_read), 0), 2) AS cache_hit_pct,
  (SELECT pg_size_pretty(wal_bytes) FROM pg_stat_wal) AS wal_written,
  (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database()) AS since
  FROM pg_stat_database WHERE datname = current_database()"

section "Biggest tables (live/dead tuples, scans, updates, HOT share)"
psql "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS total, n_live_tup AS live, n_dead_tup AS dead,
  seq_scan, idx_scan, n_tup_upd AS upd, n_tup_hot_upd AS hot_upd, last_autovacuum::timestamp(0) AS last_autovac
  FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 8"

section "Indexes on users (size, scans) and table options"
psql "SELECT i.indexrelname AS index, pg_size_pretty(pg_relation_size(i.indexrelid)) AS size, i.idx_scan AS scans FROM pg_stat_user_indexes i WHERE i.relname = 'users' ORDER BY pg_relation_size(i.indexrelid) DESC"
psql "SELECT relname, pg_size_pretty(pg_relation_size(oid)) AS heap, reloptions FROM pg_class WHERE relname IN ('users', 'node_user_usages')"

section "Key Postgres settings"
psql "SELECT name, current_setting(name) AS value FROM pg_settings WHERE name IN ('shared_buffers', 'effective_cache_size', 'work_mem', 'jit', 'random_page_cost', 'wal_compression', 'max_wal_size', 'checkpoint_completion_target', 'autovacuum_vacuum_scale_factor', 'max_connections', 'log_min_duration_statement') ORDER BY name"

section "node_user_usages retention"
psql "SELECT count(*) AS rows, min(created_at)::date AS oldest, max(created_at)::date AS newest,
  (max(created_at)::date - min(created_at)::date + 1) AS days,
  count(*) FILTER (WHERE created_at > now() - interval '24 hours') AS rows_last_24h,
  pg_size_pretty(pg_total_relation_size('node_user_usages')) AS size FROM node_user_usages"

section "Redis"
redis INFO stats | tr -d '\r' | grep -E '^(instantaneous_ops_per_sec|total_commands_processed|keyspace_hits|keyspace_misses|evicted_keys):'
redis INFO memory | tr -d '\r' | grep -E '^used_memory_human:'
keys=$(redis --scan --pattern 'rapido:node_config*' | tr -d '\r' | head -5)
if [ -z "$keys" ]; then echo "node_config: no key in Redis"; fi
for k in $keys; do echo "$k: $(redis MEMORY USAGE "$k" | tr -d '\r') bytes"; done

section "HTTP time by path, last $LOG_WINDOW (ranked by total ms; ids/usernames/tokens folded)"
dc logs --no-log-prefix --since "$LOG_WINDOW" panel 2>/dev/null | awk '
  function val(s, key,   i, v) {
    i = index(s, "\"" key "\":"); if (!i) return ""
    v = substr(s, i + length(key) + 3)
    if (substr(v, 1, 1) == "\"") { v = substr(v, 2); return substr(v, 1, index(v, "\"") - 1) }
    return substr(v, 1, match(v, /[,}]/) - 1)
  }
  /"msg":"http"/ {
    p = val($0, "path"); m = val($0, "method"); d = val($0, "duration_ms") + 0
    sub(/^\/api\/user\/[^\/]+/, "/api/user/:name", p); sub(/^\/sub\/[^\/]+/, "/sub/:token", p); sub(/^\/api\/node\/[0-9]+/, "/api/node/:id", p); sub(/^\/api\/tickets\/[0-9]+/, "/api/tickets/:id", p)
    k = m " " p; n[k]++; t[k] += d; if (d > mx[k]) mx[k] = d; all += d; cnt++
  }
  END {
    printf "%d requests, %d ms total\n%-44s %8s %10s %8s %8s\n", cnt, all, "METHOD PATH", "COUNT", "TOTAL_MS", "AVG_MS", "MAX_MS"
    for (k in n) printf "%-44s %8d %10d %8.1f %8d\n", k, n[k], t[k], t[k] / n[k], mx[k]
  }' | { read -r first; read -r header; echo "$first"; echo "$header"; sort -k4,4 -nr | head -15; }

section "Failed logins by source, last 1h (top 10)"
dc logs --no-log-prefix --since 1h panel 2>/dev/null | grep -E '"msg":"apiclient".*"path":"/api/admin/token"' | grep -E '"status":(401|429)' | awk '
  function val(s, key,   i, v) {
    i = index(s, "\"" key "\":"); if (!i) return "?"
    v = substr(s, i + length(key) + 4)
    return substr(v, 1, index(v, "\"") - 1)
  }
  { n[val($0, "ip") "  user=" val($0, "admin") "  " val($0, "login_error")]++ }
  END { for (k in n) printf "%6d  %s\n", n[k], k }' | sort -nr | head -10

section "CPU per request (30 s window, from the container cgroups)"
cpu_usec() {
  local id f; id=$(dc ps -q "$1" 2>/dev/null | head -1); [ -n "$id" ] || return 0
  for f in "/sys/fs/cgroup/system.slice/docker-$id.scope/cpu.stat" "/sys/fs/cgroup/docker/$id/cpu.stat"; do
    if [ -r "$f" ]; then awk '/^usage_usec/ {print $2}' "$f"; return 0; fi
  done
  f="/sys/fs/cgroup/cpu,cpuacct/docker/$id/cpuacct.usage"; [ -r "$f" ] && awk '{print int($1 / 1000)}' "$f"
  return 0
}
services="panel backend postgres redis"
declare -A before
for s in $services; do before[$s]=$(cpu_usec "$s"); done
sleep 30
reqs=$(dc logs --no-log-prefix --since 30s panel 2>/dev/null | grep -c '"msg":"http"')
for s in $services; do
  b=${before[$s]:-}; a=$(cpu_usec "$s")
  if [ -z "$b" ] || [ -z "$a" ]; then echo "$s: cgroup CPU counter not readable (run as root on the host)"; continue; fi
  awk -v s="$s" -v ms="$(( (a - b) / 1000 ))" -v r="$reqs" 'BEGIN {
    printf "%-9s %7d ms CPU in 30 s = %5.2f%% of one core", s, ms, ms / 300
    if (s == "panel" && r > 0) printf "; %d requests => %.2f ms CPU/request", r, ms / r
    printf "\n" }'
done
