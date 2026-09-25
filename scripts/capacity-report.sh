#!/usr/bin/env bash
# How many clients can each node carry? A read-only estimate from the load the
# panel has already recorded (host_metrics) - it puts nothing on the nodes.
#
#   bash scripts/capacity-report.sh                 # 48 h window, 70% CPU target
#   WINDOW_HOURS=168 TARGET_CPU=80 bash scripts/capacity-report.sh
#
# Run it on the panel server, from /opt/rapido-go. What it does, per node:
#   1. takes every 10-second sample in the window and drops the ones taken while
#      the node was recovering from a restart (CPU above RESTART_CPU: a restart
#      makes every client reconnect at once and pins the CPU for a minute or two -
#      that is a burst, not what steady load costs);
#   2. fits CPU% = a + b * load by least squares (load = open client connections
#      when the panel has recorded them, else established TCP sockets, else Mbit/s);
#   3. reads off the load at which CPU would reach TARGET_CPU. That is the node's
#      capacity: put it in the node's "Capacity" field in the dashboard.
# The fit is only trustworthy inside the range of loads it has seen, so the
# report prints the busiest load it saw next to the answer, the correlation
# (a weak one means the estimate is a guess), and the multiple of headroom.
# The CPU line is not the only ceiling: run scripts/exit-speedtest.sh on a node
# to see what each WireGuard exit itself can push, and see README "Capacity".
set -uo pipefail
cd "${RAPIDO_DIR:-/opt/rapido-go}" 2>/dev/null || cd "$(dirname "$0")/.." || true

WINDOW_HOURS="${WINDOW_HOURS:-48}"
TARGET_CPU="${TARGET_CPU:-70}"
RESTART_CPU="${RESTART_CPU:-85}"

if [ -f docker-compose.prod.yml ]; then
  dc() { docker compose -f docker-compose.prod.yml --env-file .env "$@"; }
else
  dc() { docker compose "$@"; }
fi
has_col() { [ "$(psql -At -c "SELECT count(*) FROM information_schema.columns WHERE table_name='$1' AND column_name='$2'" 2>/dev/null)" = "1" ]; }
psql() { dc exec -T -e PGOPTIONS='-c default_transaction_read_only=on' postgres psql -U rapido -d rapido -X -P footer=off -P pager=off "$@"; }

# columns added in v1.3: fall back to NULL on an older schema instead of failing
if has_col nodes capacity; then CAP_EXPR='n.capacity'; else CAP_EXPR='NULL::int'; fi
if has_col host_metrics client_conns; then CLI_EXPR='m.client_conns'; CLI_EXPR_BARE='client_conns'; else CLI_EXPR='NULL::int'; CLI_EXPR_BARE='NULL::int'; fi

echo "Capacity estimate from the last ${WINDOW_HOURS} h of samples, target CPU ${TARGET_CPU}% (samples above ${RESTART_CPU}% CPU ignored as restart bursts)"
echo

psql -v hours="$WINDOW_HOURS" -v target="$TARGET_CPU" -v restart="$RESTART_CPU" <<SQL
WITH s AS (
  SELECT n.id, n.name, ${CAP_EXPR} AS capacity,
         m.cpu_percent AS cpu,
         ${CLI_EXPR} AS client_conns,
         m.connections AS estab,
         (m.rx_rate + m.tx_rate) / 1e6 * 8 AS mbps
  FROM host_metrics m JOIN nodes n ON n.id = m.node_id
  WHERE m.collected_at > now() - (:'hours' || ' hours')::interval
    AND m.cpu_percent IS NOT NULL AND m.cpu_percent < :restart
),
fit AS (
  SELECT id, name, capacity,
         count(*) AS samples,
         count(client_conns) AS with_clients,
         -- CPU against open client connections (needs the v1.3 sample field)
         regr_slope(cpu, client_conns) FILTER (WHERE client_conns > 0)     AS b_cli,
         regr_intercept(cpu, client_conns) FILTER (WHERE client_conns > 0) AS a_cli,
         corr(cpu, client_conns) FILTER (WHERE client_conns > 0)           AS r_cli,
         max(client_conns)                                                 AS max_cli,
         -- CPU against established TCP sockets (always recorded)
         regr_slope(cpu, estab)     AS b_est,
         regr_intercept(cpu, estab) AS a_est,
         corr(cpu, estab)           AS r_est,
         max(estab)                 AS max_est,
         regr_slope(cpu, mbps)      AS b_mbps,
         regr_intercept(cpu, mbps)  AS a_mbps,
         corr(cpu, mbps)            AS r_mbps,
         percentile_cont(0.99) WITHIN GROUP (ORDER BY mbps) AS max_mbps,
         percentile_cont(0.99) WITHIN GROUP (ORDER BY cpu)  AS max_cpu
  FROM s GROUP BY id, name, capacity
)
SELECT name,
       samples,
       round(max_cpu::numeric, 0)                                    AS p99_cpu_pct,
       round(max_mbps::numeric, 0)                                   AS p99_mbps,
       round(r_mbps::numeric, 2)                                     AS corr,
       round(((:'target'::float - a_mbps) / NULLIF(b_mbps, 0))::numeric, 0) AS mbps_at_target,
       round(((:'target'::float - a_mbps) / NULLIF(b_mbps, 0) / NULLIF(max_mbps, 0))::numeric, 2) AS x_p99_mbps
FROM fit ORDER BY name;
SQL

echo
echo "Capacity in OPEN CLIENT CONNECTIONS (the unit the dashboard's node capacity uses)."
echo "Uses the agents' own count when the panel has recorded it, otherwise scales the"
echo "TCP-socket fit by the current clients-per-socket ratio read from Redis."
echo

# current ratio of client connections to established sockets, per node id
RATIOS="$(psql -At -F, -c "
  SELECT n.id, m.connections FROM nodes n
  JOIN LATERAL (SELECT connections FROM host_metrics h WHERE h.node_id = n.id ORDER BY h.id DESC LIMIT 1) m ON true
  WHERE n.status = 'connected' ORDER BY n.id" | while IFS=, read -r id estab; do
    total="$(dc exec -T redis redis-cli GET "presence:node:${id}:total" 2>/dev/null </dev/null | tr -d '\r')"
    printf '%s,%s,%s\n' "$id" "${estab:-0}" "${total:-0}"
  done)"

psql -v hours="$WINDOW_HOURS" -v target="$TARGET_CPU" -v restart="$RESTART_CPU" -v ratios="$RATIOS" <<SQL
WITH live AS (
  SELECT split_part(l, ',', 1)::int AS id,
         NULLIF(split_part(l, ',', 2), '')::float AS estab_now,
         NULLIF(split_part(l, ',', 3), '')::float AS clients_now
  FROM unnest(string_to_array(:'ratios', E'\n')) AS l WHERE l <> ''
),
s AS (
  SELECT n.id, n.name, ${CAP_EXPR} AS capacity, m.cpu, m.client_conns, m.estab FROM (
    SELECT node_id, cpu_percent AS cpu, ${CLI_EXPR_BARE} AS client_conns, connections AS estab
    FROM host_metrics WHERE collected_at > now() - (:'hours' || ' hours')::interval
      AND cpu_percent IS NOT NULL AND cpu_percent < :restart) m
  JOIN nodes n ON n.id = m.node_id
),
fit AS (
  SELECT id, name, capacity,
         regr_slope(cpu, client_conns) FILTER (WHERE client_conns > 0)     AS b_cli,
         regr_intercept(cpu, client_conns) FILTER (WHERE client_conns > 0) AS a_cli,
         count(client_conns) AS n_cli,
         regr_slope(cpu, estab) AS b_est, regr_intercept(cpu, estab) AS a_est,
         corr(cpu, estab) AS r_est, max(estab) AS max_est
  FROM s GROUP BY id, name, capacity
)
SELECT f.name,
       f.capacity                                                     AS set_now,
       CASE WHEN f.n_cli >= 500 THEN 'clients' ELSE 'sockets*ratio' END AS method,
       round(CASE WHEN f.n_cli >= 500
            THEN (:'target'::float - f.a_cli) / NULLIF(f.b_cli, 0)
            ELSE (:'target'::float - f.a_est) / NULLIF(f.b_est, 0) * (l.clients_now / NULLIF(l.estab_now, 0)) END)::bigint AS capacity_at_target,
       round(l.clients_now)                                            AS clients_now,
       round(CASE WHEN f.n_cli >= 500
            THEN (:'target'::float - f.a_cli) / NULLIF(f.b_cli, 0) / NULLIF(l.clients_now, 0)
            ELSE (:'target'::float - f.a_est) / NULLIF(f.b_est, 0) / NULLIF(l.estab_now, 0) END::numeric, 1) AS x_current,
       round(f.r_est::numeric, 2)                                      AS corr_sockets
FROM fit f LEFT JOIN live l USING (id) ORDER BY f.name;
SQL

echo
echo "How to read it: capacity_at_target is the number of open client connections at which the"
echo "node's CPU would sit at ${TARGET_CPU}% (about 1x = it is there already, 3x = it can carry three"
echo "times today's load). Peaks are what matters, so run it after a busy day. Round the figure DOWN"
echo "(the fit is a straight line and real load bends upward near the top) and enter it as the node's"
echo "Capacity. A restart at a busy hour costs far more CPU than steady load does - see README."
