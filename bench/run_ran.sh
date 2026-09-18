#!/usr/bin/env bash
# Start the free-ran-ue gNB and fire a registration storm of <ues> UEs.
#
#   sudo ./bench/run_ran.sh <ues> <rundir> [concurrent]
#
# The gNB lives in free-ran-ns and the UEs in free-ue-ns (created by
# `make -C free-ran-ue ns-up`). Both are pinned to RAN_CPUS so the simulator
# competes with itself, never with the core under test.
#
# The storm ends as soon as every UE has completed its PDU session (or MAX_WAIT
# elapses), rather than after a fixed hold: across a few hundred runs that is
# the difference between hours of waiting and none.
set -euo pipefail

UES="${1:?usage: run_ran.sh <ues> <rundir> [concurrent]}"
RUNDIR="${2:?usage: run_ran.sh <ues> <rundir> [concurrent]}"
CONCURRENT="${3:-$UES}"

PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RAN="${PROJ}/free-ran-ue"
RAN_CPUS="${RAN_CPUS:-8-11}"
RAN_NS="${RAN_NS:-free-ran-ns}"
UE_NS="${UE_NS:-free-ue-ns}"
MAX_WAIT="${MAX_WAIT:-300}"

if [[ $EUID -ne 0 ]]; then echo "must run as root (netns + tun device)" >&2; exit 2; fi
mkdir -p "$RUNDIR"
cd "$RAN"

GNB_PID=""
UE_PID=""
cleanup() {
  set +e
  for p in "$UE_PID" "$GNB_PID"; do
    [[ -n "$p" ]] && kill -INT "$p" 2>/dev/null
  done
  sleep 2
  for p in "$UE_PID" "$GNB_PID"; do
    [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null
  done
}
trap cleanup EXIT INT TERM

# A gNB or UE surviving an aborted run keeps the namespace's SCTP address and
# tunnel devices, and the new gNB then cannot bind. Clear them first.
pkill -f 'free-ran-ue (gnb|ue) ' 2>/dev/null || true
pkill -f './build/free-ran-ue' 2>/dev/null || true
sleep 1
pkill -9 -f './build/free-ran-ue' 2>/dev/null || true
sleep 1

ip netns exec "$RAN_NS" taskset -c "$RAN_CPUS" \
  ./build/free-ran-ue gnb -c config/gnb.yaml > "${RUNDIR}/gnb.log" 2>&1 &
GNB_PID=$!
sleep 2

if ! kill -0 "$GNB_PID" 2>/dev/null; then
  echo "gNB failed to start; see ${RUNDIR}/gnb.log" >&2
  exit 1
fi

echo "ue_start=$(date +%s.%N)" >> "${RUNDIR}/ran_timing.txt"
RANUE_BENCH_TRACE="${RUNDIR}/ue_trace.csv" \
  ip netns exec "$UE_NS" taskset -c "$RAN_CPUS" \
  ./build/free-ran-ue ue -c config/ue.yaml -n "$UES" -p "$CONCURRENT" \
  > "${RUNDIR}/ue.log" 2>&1 &
UE_PID=$!

# Poll the UE-side trace rather than the log: it is the same data the analysis
# uses, so "done" here means "done" there too.
#
# Under-provisioned configurations genuinely collapse: NAS timers expire, the
# UE state machines desync, and those UEs never complete. Waiting MAX_WAIT for
# them would cost ten minutes per collapsed cell, so give up once progress has
# stopped for STALL seconds and record the partial completion - the completion
# ratio is itself a result worth reporting.
STALL="${STALL:-45}"
deadline=$(( $(date +%s) + MAX_WAIT ))
done_count=0
last_count=-1
last_progress=$(date +%s)
stalled=0

while :; do
  if [[ -f "${RUNDIR}/ue_trace.csv" ]]; then
    done_count=$(grep -c ',pdu_done,' "${RUNDIR}/ue_trace.csv" 2>/dev/null || echo 0)
  fi
  [[ "$done_count" -ge "$UES" ]] && break

  if [[ "$done_count" -ne "$last_count" ]]; then
    last_count=$done_count
    last_progress=$(date +%s)
  elif [[ $(( $(date +%s) - last_progress )) -ge $STALL ]]; then
    echo "stalled: no progress for ${STALL}s at ${done_count}/${UES} UEs" >&2
    stalled=1
    break
  fi

  if [[ $(date +%s) -ge $deadline ]]; then
    echo "timeout=${MAX_WAIT}s reached with ${done_count}/${UES} UEs complete" >&2
    break
  fi
  if ! kill -0 "$UE_PID" 2>/dev/null; then
    echo "UE process exited early with ${done_count}/${UES} complete" >&2
    break
  fi
  sleep 0.2
done
echo "ue_stalled=${stalled}" >> "${RUNDIR}/ran_timing.txt"

echo "ue_all_done=$(date +%s.%N)" >> "${RUNDIR}/ran_timing.txt"
echo "ue_completed=${done_count}" >> "${RUNDIR}/ran_timing.txt"
echo "ue_expected=${UES}" >> "${RUNDIR}/ran_timing.txt"

# Let the UEs deregister cleanly so the next run starts from an empty AMF.
kill -INT "$UE_PID" 2>/dev/null || true
wait "$UE_PID" 2>/dev/null || true
echo "ue_end=$(date +%s.%N)" >> "${RUNDIR}/ran_timing.txt"

echo "storm done: ${done_count}/${UES} UEs (concurrency ${CONCURRENT}) -> ${RUNDIR}"
