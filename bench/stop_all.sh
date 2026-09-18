#!/usr/bin/env bash
# Stop everything this benchmark starts, and verify it is actually stopped.
#
#   sudo ./bench/stop_all.sh
#
# Written because ad-hoc pkill is not reliable here: run_all.sh is started with
# sudo, so a non-root kill silently fails on it, and the orchestrator then keeps
# launching runs that fight the next experiment for the same ports and network
# namespace. That failure mode is invisible except as inexplicable 0/N results.
set -uo pipefail

PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FREE5GC="${FREE5GC:-/home/dbgr/free5gc}"

if [[ $EUID -ne 0 ]]; then echo "must run as root" >&2; exit 2; fi

PATTERNS=(
  "${PROJ}/bench/run_all.sh"
  "${PROJ}/bench/run_ran.sh"
  "${PROJ}/bench/run_core.sh"
  "free-ran-ue/build/free-ran-ue"
  "./build/free-ran-ue"
  "${FREE5GC}/bin/"
  "${PROJ}/bin/amf-bench"
)

# The orchestrator first, so it cannot start a replacement mid-cleanup.
for pat in "${PATTERNS[@]}"; do pkill -f "$pat" 2>/dev/null; done
sleep 2
for pat in "${PATTERNS[@]}"; do pkill -9 -f "$pat" 2>/dev/null; done
sleep 1

ip link del upfgtp 2>/dev/null

leftover=0
for pat in "${PATTERNS[@]}"; do
  if pgrep -f "$pat" > /dev/null 2>&1; then
    echo "still running: $pat" >&2
    pgrep -af "$pat" >&2
    leftover=1
  fi
done

for port in 2152 38412; do
  if ss -Hln "sport = :${port}" 2>/dev/null | grep -q .; then
    echo "port ${port} still bound:" >&2
    ss -lnp "sport = :${port}" >&2
    leftover=1
  fi
done

if [[ "$leftover" -ne 0 ]]; then
  echo "cleanup incomplete" >&2
  exit 1
fi
echo "all stopped; ports 2152 and 38412 free; no upfgtp link"
