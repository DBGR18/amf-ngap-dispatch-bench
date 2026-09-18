# Design: what is measured, and why it is comparable

## The one variable

Both arms are the same AMF binary, the same config template, the same core, the
same load. They differ in one function: how a freshly read NGAP message is
assigned to a worker.

| | `hash` (free5gc v4.2.3) | `supi` (paper's mechanism) |
|---|---|---|
| Where the decision is made | on the SCTP reader goroutine, right after the NGAP PDU is decoded | same goroutine, but only after the NAS Registration Request is also decoded |
| Dispatch key | RAN-UE-NGAP-ID, then AMF-UE-NGAP-ID | MSIN from the SUCI, for the UE's whole life |
| Key changes mid-procedure | yes | no |
| Extra work in the serial section | none beyond the NGAP decode | one NAS decode per InitialUEMessage |
| Code | `internal/ngap/ue_id_extractor.go` | `internal/ngap/supi_dispatch.go` |

Selected by `ngapSchedulerMode: hash|supi` in `amfcfg.yaml`
(`pkg/factory/config.go`). Default is `hash`, i.e. stock upstream behaviour.

**The paper's IMSI-parity prioritisation is deliberately not implemented.** It
confines half the UEs to a single thread, so it would lose on aggregate
throughput by construction and the comparison would only be able to say
something about high-vs-low priority. Removing it isolates the part of the
paper's design this project is actually asking about: the dispatch point and
the dispatch key.

## Instrumentation

Two trace files, both nanosecond, both off by default so that an untraced run
costs exactly what upstream costs (one atomic load per hook).

`amf/internal/ngap/trace.go` - `AMF_BENCH_TRACE=<path>`, one row per inbound
NGAP message:

```
recv ──► key_extracted ──► submitted ──► worker_start ──► handled
     └──── serial section ────┘        └─ queue wait ─┘└─ processing ─┘
```

`free-ran-ue/ue/bench_trace.go` - `RANUE_BENCH_TRACE=<path>`, one row per UE
milestone (`reg_start`, `reg_done`, `pdu_start`, `pdu_done`). This exists
because the simulator's stock logger prints whole seconds, which cannot express
a per-UE latency at all.

Both writers flush every 500 ms, so a run that ends in SIGKILL still leaves
usable data.

## Metrics

| Metric | Definition | Source |
|---|---|---|
| `total_s` | per UE, `pdu_done - reg_start`. The paper's "connection processing time" | UE trace |
| `storm_s` | `max(pdu_done) - min(reg_start)` over all UEs | UE trace |
| `completion_rate` | UEs that reached `pdu_done` / UEs started | UE trace |
| `serial_us` | per message, `submitted - recv`: time on the single reader goroutine | AMF trace |
| `queue_us` | per message, `worker_start - submitted` | AMF trace |
| `process_us` | per message, `handled - worker_start`, includes blocking SBI round trips | AMF trace |
| `starttime_us` | per InitialUEMessage, `handled - recv` | AMF trace |
| `worker_switches` | UEs whose messages moved between workers mid-procedure | AMF log ID pairs |
| `keys_multi_worker` | dispatch keys seen on more than one worker. **Invariant: must be 0** | AMF trace |
| `load_cv` | coefficient of variation of per-worker message counts | AMF trace |
| `retrans_total`, `aborted_total` | NAS timer retransmissions and give-ups, by timer | AMF log |
| `cpu_seconds`, `cpu_peak_pct` | `U(t) = Σ_NF %CPU`; `CPU-sec = Σ(U/100)Δt` | pidstat 1 s |
| `supi_fallbacks` | messages where the subscriber key could not be resolved | AMF trace |

### Deviations from the paper, stated plainly

- **StartTime.** The paper measures from the gNB's receipt of the Registration
  Request. This measures from the moment the AMF reads it off the socket, and
  ends when the AMF finishes handling InitialUEMessage - which is where it has
  just sent the Authentication Request. Same endpoint, later start.
- **Simulator.** free-ran-ue, not UERANSIM. Registration timing differs.
- **Hardware.** 12 cores shared between core and simulator (cpuset 0-7 and
  8-11), against the paper's 16-core core host plus a separate RAN host.
  Absolute seconds are therefore not comparable with the paper; only the
  difference between arms measured here is.
- **No prioritisation**, as argued above.

### Metrics that are only valid mode-aware

`worker_switches` under `hash` is computed from the ID pairs, because the NGAP
IDs *are* the dispatch key. Under `supi` the NGAP IDs are not the key, so the
same formula would invent switches that never happened; it is 0 by
construction there, and `keys_multi_worker` is the empirical check that the
invariant actually held.

## Reading a collapsed run

Under-provisioned configurations do not merely get slower, they collapse: NAS
timers expire, the AMF gives up on UEs (`T3550 Expires 4 times, abort`), and
the simulator's UE state machines desync. `hash:1` at 200 UEs completed 70/200.

A latency mean computed over the survivors of a collapse is **not** comparable
to one from a run where everyone finished - the survivors are the lucky tail.
Always read `completion_rate` first, then latency.

## Guards in the harness

Silent misconfiguration is the main threat to this experiment, so the runner
refuses to produce data it cannot trust:

- duplicate `ngap*` keys in the rendered config (yaml.v2 lets the last one win,
  which once pinned every arm to `runtime.NumCPU()` workers with no warning)
- unreplaced `__PLACEHOLDER__` in the rendered config
- the AMF log not reporting the requested worker count
- no PFCP association between SMF and UPF (every UE would register and then
  fail PDU session establishment, which looks like an AMF result and is not)
- a stale `upfgtp` link from a SIGKILLed UPF, removed before each start

Each run records `provenance.txt`: mode, worker count, cpuset, the AMF binary's
sha256, the AMF source commit, and the rendered config's sha256.

## Matrix

- arms: `hash` and `supi`, each at 1, 2, 4, 8 workers
- UE counts: 100, 200, 400
- 10 runs per cell, subscribers re-provisioned before every run (this resets the
  authentication SQN, which otherwise drifts and eventually breaks auth)
