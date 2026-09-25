#!/usr/bin/env bash
# How much can each WireGuard exit push? Run ON A NODE, as root:
#
#   sudo bash exit-speedtest.sh                        # every tunnel, 8 s each, capped at 200 Mbit/s
#   sudo bash exit-speedtest.sh --tunnel germany       # one tunnel
#   sudo bash exit-speedtest.sh --cap-mbps 1000 --seconds 10   # look for the real ceiling (off-peak!)
#
# For each tunnel it downloads from a public speed endpoint through that tunnel
# only (the socket is bound to the WireGuard interface, so it takes the exit and
# nothing else) with several parallel streams, and prints the aggregate Mbit/s.
# The cap keeps the test gentle on a live exit: if the result comes back at
# (nearly) the cap, the tunnel can carry AT LEAST that much - raise --cap-mbps
# and repeat, ideally in the quietest hour, until it stops growing. The
# ceiling found this way is what one config can carry, whatever the node's CPU
# says; the same client-count-per-Mbit/s ratio you see in the dashboard turns it
# into a number of connections.
set -uo pipefail

SECONDS_PER_TUNNEL=8
STREAMS=4
CAP_MBPS=200
# the endpoint refuses (HTTP 403) anything much above 50 MB; a stream simply repeats the download
URL="https://speed.cloudflare.com/__down?bytes=50000000"
ONLY=""

while [ $# -gt 0 ]; do
  case "$1" in
    --tunnel)   ONLY="$2"; shift 2 ;;
    --seconds)  SECONDS_PER_TUNNEL="$2"; shift 2 ;;
    --streams)  STREAMS="$2"; shift 2 ;;
    --cap-mbps) CAP_MBPS="$2"; shift 2 ;;
    --url)      URL="$2"; shift 2 ;;
    -h|--help)  sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" -eq 0 ] || { echo "run as root (binding a socket to an interface needs it)" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v wg   >/dev/null || { echo "wireguard-tools (wg) is required" >&2; exit 1; }

TUNNELS="$(wg show interfaces)"
[ -n "$ONLY" ] && TUNNELS="$ONLY"
[ -n "$TUNNELS" ] || { echo "no WireGuard interfaces are up on this host" >&2; exit 1; }

# bytes per second per stream, so the streams together stay under the cap
PER_STREAM=$(( CAP_MBPS * 1000000 / 8 / STREAMS ))

printf '%-14s %10s %14s %s\n' TUNNEL "MBIT/S" "ENDPOINT" NOTE
for t in $TUNNELS; do
  ip link show "$t" >/dev/null 2>&1 || { printf '%-14s %10s\n' "$t" "no such interface"; continue; }
  endpoint="$(wg show "$t" endpoints 2>/dev/null | awk '{print $2}' | head -1 | sed -E 's/:[0-9]+$//')"
  tmp="$(mktemp -d)"
  deadline=$(( $(date +%s) + SECONDS_PER_TUNNEL ))
  for i in $(seq 1 "$STREAMS"); do
    (
      sum=0
      while [ "$(date +%s)" -lt "$deadline" ]; do
        left=$(( deadline - $(date +%s) ))
        got="$(curl -s --interface "$t" --max-time "$left" --limit-rate "$PER_STREAM" \
                -o /dev/null -w '%{size_download}' "$URL" 2>/dev/null || true)"
        [ -n "$got" ] && sum=$(( sum + got ))
        # nothing useful came back (an error page, a dead tunnel): do not spin
        [ "${got:-0}" -lt 1000000 ] && break
      done
      echo "$sum" > "$tmp/$i"
    ) &
  done
  wait
  total=0
  for i in $(seq 1 "$STREAMS"); do total=$(( total + $(cat "$tmp/$i" 2>/dev/null || echo 0) )); done
  rm -rf "$tmp"
  mbps=$(( total * 8 / SECONDS_PER_TUNNEL / 1000000 ))
  note=""
  if [ "$total" -eq 0 ]; then
    note="no data - the tunnel is down or the speed endpoint is unreachable through it"
  elif [ "$mbps" -ge $(( CAP_MBPS * 90 / 100 )) ]; then
    note="reached the ${CAP_MBPS} Mbit/s cap: the tunnel carries at least this much, raise --cap-mbps"
  else
    note="below the cap: this is close to what the tunnel gives right now"
  fi
  printf '%-14s %10s %14s %s\n' "$t" "$mbps" "${endpoint:-?}" "$note"
done
