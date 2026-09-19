#!/usr/bin/env bash
# Stop everything this benchmark starts, and verify it is actually stopped.
#
#   sudo ./bench/stop_all.sh
#
# Written after two failures that this script exists to make impossible:
#
#   1. run_all.sh is launched as `./bench/run_all.sh`, so killing it by
#      absolute path matched nothing - while the verification used the same
#      non-matching pattern and cheerfully reported success. The orchestrator
#      survived, and its per-run cleanup then SIGKILLed the next experiment's
#      simulator mid-run. Every cell reported 0/N.
#   2. A bare `pkill -f <pattern>` also matches the shell that is running this
#      script, when that shell's own command line mentions the pattern. It
#      kills itself halfway through.
#
# So: match on how the processes are really spelled, never kill ourselves or
# our ancestors, and verify by re-checking rather than by assuming.
set -uo pipefail

if [[ $EUID -ne 0 ]]; then echo "must run as root" >&2; exit 2; fi

PATTERNS=(
  "bench/run_all\.sh"
  "bench/run_ran\.sh"
  "bench/run_core\.sh"
  "build/free-ran-ue"
  "free5gc/bin/"
  "bin/amf-bench"
)

# Our own process and every ancestor, so we never kill the shell we are in.
protected=""
p=$$
while [[ -n "$p" && "$p" != "0" && "$p" != "1" ]]; do
  protected="${protected} ${p}"
  p=$(ps -o ppid= -p "$p" 2>/dev/null | tr -d ' ')
done

is_protected() {
  for q in $protected; do [[ "$1" == "$q" ]] && return 0; done
  return 1
}

victims() {
  local pat pid
  for pat in "${PATTERNS[@]}"; do
    for pid in $(pgrep -f "$pat" 2>/dev/null); do
      is_protected "$pid" || echo "$pid"
    done
  done | sort -un
}

# Orchestrator first, so it cannot launch a replacement mid-cleanup.
for sig in TERM KILL; do
  list=$(victims)
  [[ -z "$list" ]] && break
  # shellcheck disable=SC2086
  kill -"$sig" $list 2>/dev/null
  sleep 2
done

ip link del upfgtp 2>/dev/null

leftover=0
list=$(victims)
if [[ -n "$list" ]]; then
  echo "still running:" >&2
  ps -o pid,cmd -p $list >&2 2>/dev/null
  leftover=1
fi

for port in 2152 38412; do
  holders=$(ss -Hlnp "sport = :${port}" 2>/dev/null |
            grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u || true)
  if [[ -n "$holders" ]]; then
    echo "port ${port} still bound by pid(s): ${holders}" >&2
    leftover=1
  fi
done

if [[ "$leftover" -ne 0 ]]; then
  echo "cleanup incomplete" >&2
  exit 1
fi
echo "all stopped; ports 2152 and 38412 free; no upfgtp link"
