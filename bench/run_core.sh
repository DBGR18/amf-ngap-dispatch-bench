#!/usr/bin/env bash
# Start the 5G core for one benchmark arm.
#
#   sudo ./bench/run_core.sh <mode> <workers> <rundir>
#
#   mode    : hash | supi   -> ngapSchedulerMode
#   workers : NGAP worker pool size (0 = NumCPU)
#   rundir  : directory for logs / pidstat output of this run
#
# Every NF is pinned to CORE_CPUS so the RAN simulator (pinned to RAN_CPUS by
# run_ran.sh) cannot steal cycles from the core under test.
set -euo pipefail

MODE="${1:?usage: run_core.sh <hash|supi> <workers> <rundir>}"
WORKERS="${2:?usage: run_core.sh <hash|supi> <workers> <rundir>}"
RUNDIR="${3:?usage: run_core.sh <hash|supi> <workers> <rundir>}"

PROJ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FREE5GC="${FREE5GC:-/home/dbgr/free5gc}"
CORE_CPUS="${CORE_CPUS:-0-7}"

case "$MODE" in hash|supi) ;; *) echo "mode must be hash or supi" >&2; exit 2 ;; esac
if [[ $EUID -ne 0 ]]; then echo "must run as root (UPF needs gtp5g)" >&2; exit 2; fi

mkdir -p "$RUNDIR"
cd "$PROJ"

# --- render this arm's amfcfg -------------------------------------------------
sed -e "s/__SCHED_MODE__/${MODE}/" -e "s/__WORKERS__/${WORKERS}/" \
    config/amfcfg.tmpl.yaml > "${RUNDIR}/amfcfg.yaml"

# yaml.v2 lets a later duplicate key silently win, so a stray second copy of
# any of these would pin the arm to the wrong worker count without a word in
# the log. Refuse to run rather than produce data that looks fine and is not.
for key in ngapSchedulerMode ngapWorkerPoolSize ngapTaskBufferSize; do
  n=$(grep -c "^[[:space:]]*${key}:" "${RUNDIR}/amfcfg.yaml" || true)
  if [[ "$n" -ne 1 ]]; then
    echo "config error: ${key} appears ${n} times in ${RUNDIR}/amfcfg.yaml (want 1)" >&2
    exit 3
  fi
done
if grep -q '__[A-Z_]*__' "${RUNDIR}/amfcfg.yaml"; then
  echo "config error: unreplaced placeholder in ${RUNDIR}/amfcfg.yaml" >&2
  exit 3
fi

# --- provenance: exactly which binary and config produced this run ------------
{
  echo "mode=${MODE}"
  echo "workers=${WORKERS}"
  echo "core_cpus=${CORE_CPUS}"
  echo "amf_sha256=$(sha256sum bin/amf-bench | cut -d' ' -f1)"
  echo "amf_commit=$(git -C amf rev-parse HEAD)"
  echo "amfcfg_sha256=$(sha256sum "${RUNDIR}/amfcfg.yaml" | cut -d' ' -f1)"
  echo "started_at=$(date -Is)"
} > "${RUNDIR}/provenance.txt"

# --- drop stale NF registrations ---------------------------------------------
mongosh free5gc --quiet --eval '
  ["NfProfile","applicationData.influenceData.subsToNotify","applicationData.subsToNotify",
   "policyData.subsToNotify","exposureData.subsToNotify"].forEach(c => db[c].drop());' >/dev/null

PIDS=()
cleanup() {
  set +e
  [[ -n "${PIDSTAT_PID:-}" ]] && kill "$PIDSTAT_PID" 2>/dev/null
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
  sleep 1
  for p in "${PIDS[@]}"; do kill -9 "$p" 2>/dev/null; done
}
trap cleanup EXIT INT TERM

start_nf() {
  local nf="$1" bin="$2" cfg="$3"
  taskset -c "$CORE_CPUS" "$bin" -c "$cfg" > "${RUNDIR}/${nf}.log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  echo "$nf $pid" >> "${RUNDIR}/pids.txt"
  sleep 0.3
}

: > "${RUNDIR}/pids.txt"

# A UPF that was SIGKILLed leaves its gtp5g link behind, and the next UPF dies
# with "open link: create: file exists". Every UE in that run would then fail
# PDU session establishment while registration still succeeded - a silent,
# very plausible-looking bad result. Remove the stale link first.
ip link del upfgtp 2>/dev/null || true

start_nf upf "${FREE5GC}/bin/upf" "${PROJ}/config/upfcfg.yaml"
start_nf nrf "${FREE5GC}/bin/nrf" "${PROJ}/config/nrfcfg.yaml"
sleep 1
for nf in udr udm ausf nssf pcf smf; do
  start_nf "$nf" "${FREE5GC}/bin/${nf}" "${PROJ}/config/${nf}cfg.yaml"
done
sleep 1

# Without a PFCP association every UE registers and then fails at PDU session
# establishment, which looks like an AMF result but is not one. Refuse to run.
pfcp_ok=0
for _ in $(seq 1 40); do
  if grep -q "setup association" "${RUNDIR}/smf.log" 2>/dev/null; then pfcp_ok=1; break; fi
  sleep 0.5
done
if [[ "$pfcp_ok" -ne 1 ]]; then
  echo "smf error: no PFCP association with the UPF; see ${RUNDIR}/smf.log and ${RUNDIR}/upf.log" >&2
  exit 5
fi

# AMF last: it is the unit under test and must find every other NF already up.
AMF_BENCH_TRACE="${RUNDIR}/amf_trace.csv" \
  taskset -c "$CORE_CPUS" "${PROJ}/bin/amf-bench" -c "${RUNDIR}/amfcfg.yaml" \
  > "${RUNDIR}/amf.log" 2>&1 &
AMF_PID=$!
PIDS+=("$AMF_PID")
echo "amf $AMF_PID" >> "${RUNDIR}/pids.txt"
sleep 2

# Assert the AMF really built the pool we asked for. Config that parses but
# does not take effect is the failure mode this benchmark cannot tolerate.
if [[ "$WORKERS" -gt 0 ]]; then
  if ! grep -q "Initializing UE Scheduler with ${WORKERS} workers" "${RUNDIR}/amf.log"; then
    echo "amf error: worker pool is not ${WORKERS} workers; see ${RUNDIR}/amf.log" >&2
    grep -m1 "Initializing UE Scheduler" "${RUNDIR}/amf.log" >&2 || true
    exit 4
  fi
fi

# --- per-NF CPU sampling, 1 s, matching the paper's pidstat methodology -------
PID_CSV=$(cut -d' ' -f2 "${RUNDIR}/pids.txt" | paste -sd,)
pidstat -u -h -p "$PID_CSV" 1 > "${RUNDIR}/pidstat.log" 2>&1 &
PIDSTAT_PID=$!

echo "core up (amf pid ${AMF_PID}, mode=${MODE}, workers=${WORKERS})"
echo "logs: ${RUNDIR}"
wait "$AMF_PID"
