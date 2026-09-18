#!/usr/bin/env bash
# Run the full experiment matrix.
#
#   sudo ./bench/run_all.sh [results_root]
#
# Each run is fully isolated: subscribers are re-provisioned (which resets the
# authentication sequence number - otherwise SQN drifts across runs and
# eventually breaks authentication), a fresh core is started, the storm is
# fired, and the core is torn down again.
#
# Override the matrix from the environment, e.g.
#   ARMS="hash:1 hash:4" UE_COUNTS="100" RUNS=2 sudo -E ./bench/run_all.sh
set -uo pipefail

PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESULTS_ROOT="${1:-${PROJ}/results/raw}"

# "<mode>:<workers>" - seq is just the hash policy with a single worker.
ARMS="${ARMS:-hash:1 hash:2 hash:4 hash:8 supi:1 supi:2 supi:4 supi:8}"
UE_COUNTS="${UE_COUNTS:-100 200 400}"
RUNS="${RUNS:-10}"
MAX_WAIT="${MAX_WAIT:-300}"
SETTLE="${SETTLE:-5}"   # seconds between teardown and the next start

if [[ $EUID -ne 0 ]]; then echo "must run as root" >&2; exit 2; fi

cd "$PROJ"
mkdir -p "$RESULTS_ROOT"
MANIFEST="${RESULTS_ROOT}/manifest.csv"
[[ -f "$MANIFEST" ]] || echo "rundir,mode,workers,ues,run,completed,expected,status,started_at" > "$MANIFEST"

stop_core() {
  local rundir="$1"
  [[ -f "${rundir}/pids.txt" ]] || return 0
  # SIGTERM first so the AMF drains its workers and flushes the trace.
  cut -d' ' -f2 "${rundir}/pids.txt" | while read -r p; do kill "$p" 2>/dev/null; done
  sleep 3
  cut -d' ' -f2 "${rundir}/pids.txt" | while read -r p; do kill -9 "$p" 2>/dev/null; done
  pkill -f '[a]mf-bench' 2>/dev/null
  sleep 1
}

total=0
for arm in $ARMS; do for u in $UE_COUNTS; do for r in $(seq 1 "$RUNS"); do total=$((total+1)); done; done; done
n=0

for arm in $ARMS; do
  mode="${arm%%:*}"
  workers="${arm##*:}"

  for ues in $UE_COUNTS; do
    for run in $(seq 1 "$RUNS"); do
      n=$((n+1))
      rundir="${RESULTS_ROOT}/${mode}-w${workers}-u${ues}-r${run}"
      started_at="$(date -Is)"
      echo "=== [${n}/${total}] ${mode} workers=${workers} ues=${ues} run=${run} ==="

      rm -rf "$rundir"

      # Reset SQN and guarantee every subscriber exists for this UE count.
      if ! "${PROJ}/bench/provision/provision" -n "$ues" > /dev/null 2>&1; then
        echo "${rundir},${mode},${workers},${ues},${run},0,${ues},provision_failed,${started_at}" >> "$MANIFEST"
        continue
      fi

      "${PROJ}/bench/run_core.sh" "$mode" "$workers" "$rundir" > "${rundir}.core.out" 2>&1 &
      core_pid=$!

      # run_core.sh blocks on the AMF; wait for it to report readiness.
      ready=0
      for _ in $(seq 1 60); do
        if grep -q "^core up" "${rundir}.core.out" 2>/dev/null; then ready=1; break; fi
        if ! kill -0 "$core_pid" 2>/dev/null; then break; fi
        sleep 0.5
      done

      if [[ "$ready" -ne 1 ]]; then
        echo "  core failed to come up; see ${rundir}.core.out"
        stop_core "$rundir"
        echo "${rundir},${mode},${workers},${ues},${run},0,${ues},core_failed,${started_at}" >> "$MANIFEST"
        continue
      fi

      MAX_WAIT="$MAX_WAIT" "${PROJ}/bench/run_ran.sh" "$ues" "$rundir" "$ues" \
        > "${rundir}/ran.out" 2>&1
      ran_rc=$?

      stop_core "$rundir"
      wait "$core_pid" 2>/dev/null

      completed=$(grep -m1 '^ue_completed=' "${rundir}/ran_timing.txt" 2>/dev/null | cut -d= -f2)
      completed="${completed:-0}"
      status=ok
      [[ "$ran_rc" -ne 0 ]] && status=ran_error
      [[ "$completed" -lt "$ues" ]] && status=incomplete

      echo "  -> ${completed}/${ues} UEs, status=${status}"
      echo "${rundir},${mode},${workers},${ues},${run},${completed},${ues},${status},${started_at}" >> "$MANIFEST"

      sleep "$SETTLE"
    done
  done
done

echo "matrix complete -> ${MANIFEST}"
