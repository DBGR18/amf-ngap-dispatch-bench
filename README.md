# AMF NGAP dispatch modes: `blog`, `paper-early` and `paper`

A copy of free5gc's AMF with one addition: the AMF can dispatch incoming NGAP
work to parallel workers in one of three ways, selected by a setting in
`amfcfg.yaml`.

| mode | where it comes from |
|---|---|
| `blog` | free5gc's own worker pool, as described in the [free5gc blog post](https://free5gc.org/blog/20260429/20260429/) ([AMF PR #194](https://github.com/free5gc/amf/pull/194)). Dispatches on the NGAP UE ID. |
| `paper-early` | The paper's dispatch key, at free5gc's dispatch point. |
| `paper` | The dispatch idea from Nha & Nakao, *Multithreading-Based AMF Optimization for Pre-Slice Congestion Control in 5G Core Networks* (IEEE GC Wkshps 2025, pp. 2167–2172), at the paper's own dispatch point. |

The three arms differ along two independent axes — **which key** a UE is routed
by, and **where** in a message's life the hand-off to a worker happens:

| | dispatch key | hand-off point |
|---|---|---|
| `blog` | NGAP UE ID | on the raw message, before the NGAP handler runs |
| `paper-early` | subscriber IMSI | on the raw message, before the NGAP handler runs |
| `paper` | subscriber IMSI | inside the NGAP handler, at the NAS boundary |

Comparing `blog` with `paper-early` isolates the choice of key. Comparing
`paper-early` with `paper` isolates the choice of hand-off point. That
separation is the reason all three exist: the paper changes both at once, and
the two changes have very different consequences.

> The load-generation and measurement harness used to compare the modes is not
> published yet; this repository contains the implementation only.

## Configuration

A ready-to-use file is [`config/amfcfg.yaml`](config/amfcfg.yaml): free5gc
v4.2.3's stock `amfcfg.yaml` (loopback addresses, certificate paths relative to
the working directory, so it drops into a free5gc tree) with one added setting.
Adjust the addresses for any other deployment.

Three keys under `configuration:`:

```yaml
configuration:
  # ... the rest of the usual free5gc AMF config ...
  ngapSchedulerMode: paper   # blog | paper-early | paper   (default: blog)
  ngapWorkerPoolSize: 4      # number of workers; 0 = runtime.NumCPU()
  ngapTaskBufferSize: 4096   # queue depth of each worker
```

| key | values | default |
|---|---|---|
| `ngapSchedulerMode` | `blog`, `paper-early`, `paper` | `blog` (stock free5gc behaviour) |
| `ngapWorkerPoolSize` | integer ≥ 0 | `0`, meaning one worker per CPU |
| `ngapTaskBufferSize` | integer > 0 | `1000` |

- **Switching modes** means editing `ngapSchedulerMode` and restarting the AMF;
  the mode is read once at startup.
- Values are case-sensitive. Anything other than the three listed (including
  `Paper` or `paper_early`) is rejected at startup by the config validator.
- **Do not add a second copy of the worker keys.** free5gc's stock `amfcfg.yaml`
  already contains `ngapWorkerPoolSize` and `ngapTaskBufferSize` near the end of
  `configuration:`. Edit those lines. The YAML parser silently lets the *last*
  duplicate key win, so a stray earlier copy is ignored without any message and
  you end up with a different worker count than you configured.
- **Check it took effect** in the AMF log at startup. Every mode prints:

  ```
  Initializing NGAP worker pool with 4 workers (buffer size: 4096, mode: paper)
  ```

  followed by the pool that was actually built — for `blog` and `paper-early`:

  ```
  Initializing UE Scheduler with 4 workers
  ```

  and for `paper`:

  ```
  Initializing NAS Scheduler with 4 workers (buffer size: 4096)
  ```

  If the second line names the wrong pool or the wrong worker count, the config
  is not what you think it is.

## How the modes dispatch

### `blog` and `paper-early`

A single goroutine per gNB connection reads NGAP messages off the SCTP socket
and hands each to `dispatchToWorkerPool()`
(`amf/internal/ngap/service/service.go`), which computes a **dispatch key**,
picks `worker = key % N`, and puts the raw message on that worker's own buffered
channel. The worker then runs the entire NGAP handler, NAS processing included.
The mode only changes how the key is computed:

```
SCTP reader goroutine
  │
  │  dispatchToWorkerPool()                     internal/ngap/service/service.go
  │
  ├─ blog ────────────► ExtractUEIDWithMeta()   internal/ngap/ue_id_extractor.go
  │                     decode the NGAP PDU, read the UE's NGAP ID out of it
  │
  └─ paper-early ─────► PaperEarlyDispatchKey() internal/ngap/paper_dispatch.go
                        decode the NGAP PDU; on a UE's first message also decode
                        the NAS payload for its SUCI; on later messages look the
                        UE up in the AMF context
  │
  ▼  worker = key % N                           internal/ngap/scheduler.go
  ▼  that worker's channel ──► worker goroutine runs the whole handler
```

### `paper`

The NGAP handler runs on the reader goroutine, and the hand-off happens inside
it, where it calls into NAS. By then the handler has already resolved the UE's
identity for its own reasons, so the key costs no extra decoding.

```
SCTP reader goroutine
  │
  │  handler.HandleMessage()                    internal/ngap/service/service.go
  ▼
  ngap.Dispatch ──► handleInitialUEMessageMain  internal/ngap/handler.go
  │                   :457  ran.NewRanUe()
  │                   :468  DecodePlainNasNoIntegrityCheck()   (upstream's own)
  │                   :494  GetNas5GSMobileIdentity()  ──► SUCI
  │                   :513  findAmfUe()
  │                   :580  SubmitInitialNAS(ranUe, …, id, idType)
  │                 handleUplinkNASTransportMain
  │                   :136  SubmitNAS(ranUe, …)      key from ranUe.AmfUe
  │
  ▼  worker = IMSI % N                          internal/ngap/paper_nas_pool.go
  ▼  that worker's channel ──► worker goroutine runs HandleNAS onward
```

Messages that never reach NAS — `InitialContextSetupResponse`,
`PDUSessionResourceSetupResponse`, `UEContextReleaseComplete` and the rest of
the N2 procedures — have no hand-off point and are handled end to end on the
reader goroutine. See **Known consequences of `paper` mode** below.

### Summary

|  | `blog` | `paper-early` | `paper` |
|---|---|---|---|
| dispatch key | RAN-UE-NGAP-ID on InitialUEMessage, AMF-UE-NGAP-ID afterwards | the UE's IMSI | the UE's IMSI |
| key known after | the NGAP decode | the NAS decode (first message) or a cache lookup (later ones) | the NGAP handler has resolved the UE |
| work on the reader goroutine | NGAP decode | NGAP decode, plus a NAS decode on InitialUEMessage and a cache lookup otherwise | the whole NGAP handler |
| key stable for the UE's lifetime | no — it changes when the AMF assigns AMF-UE-NGAP-ID, so a UE's later messages can land on a different worker | yes | yes |
| one UE touched by one goroutine at a time | no (see above) | yes | **no** |
| non-NAS N2 messages | in a worker | in a worker | on the reader goroutine |
| messages with no UE (e.g. NGSetupRequest) | worker 0 | worker 0 | reader goroutine |

## How the subscriber key is derived

Both paper-derived modes key on the full IMSI (MCC+MNC+MSIN, e.g.
`208930000000001`), and `worker = key % N`. They obtain it differently, and the
difference is forced by *where* each one decides.

**The first message** is the only one carrying the identity and the only one
with no UE context yet, so it comes from the NAS payload: `paper-early` decodes
the Registration Request, `paper` reuses the decode `handleInitialUEMessageMain`
already does at `handler.go:468`.

**Later messages** carry only the AMF-UE-NGAP-ID.

- `paper-early` looks the key up in a cache populated at registration, keyed by
  `{gNB connection, RAN-UE-NGAP-ID}`. It **must not** read `AmfUe` here: it
  decides on the reader goroutine while a worker may be inside the same UE's
  previous message, and `Suci`/`Supi` are written from there
  (`internal/gmm/handler.go:477`, `:1563`, `:2035`) — a data race, confirmed
  with `-race`. The cache reads only fields fixed when the `RanUe` was created.
- `paper` holds the `*RanUe` and reads `AmfUe` directly, `Suci` before `Supi`
  since `Supi` stays empty until AUSF answers. Same race, but its hand-off point
  already splits a UE across two goroutines — see **Known consequences**.

**Fallback** is the RAN-UE-NGAP-ID, cached too, so a UE whose SUCI arrives late
(an unmatched 5G-GUTI, answered with an Identity Request) keeps its first key
instead of moving worker mid-registration. The cache is connection-scoped
because a RAN-UE-NGAP-ID is unique only within one gNB.

**Requires the null scheme.** A profile A/B SUCI conceals the MSIN, and nobody
in the AMF holds the IMSI until AUSF/UDM de-conceals it. Such a UE falls back
for its whole lifetime: `subscriberKeyFromAmfUe` refuses the SUPI while a
concealed SUCI is present, since taking it would move the UE the instant
authentication completed. Those runs are **not a valid paper arm** — check the
fallback column.

**Beyond the paper**: it takes the IMSI from the Initial UE Message +
Registration Request or the PDU Session Establishment Request, and never says
how a UE's other messages — most of its traffic — obtain it. The lookup above is
this project's own design. **Not implemented**: the paper's IMSI-parity split
(even to threads `0..N-2`, odd confined to `N-1`); only the identity-keyed
dispatch is here.

## Known consequences of `paper` mode

free5gc nests NAS processing inside the NGAP handler rather than after it, so
the paper's hand-off point has three effects its Fig. 4 does not model. They are
reported rather than worked around: working around them would stop this being
the paper's design.

Measured with a 100-UE registration storm against a `-race` build, four workers,
two runs per mode (a `-race` build's latencies are inflated and are not results):

| mode | UEs completed | data races |
|---|---|---|
| `blog` | 100/100, 100/100 | 0, 0 |
| `paper-early` | 100/100, 100/100 | 0, 0 |
| `paper` | 100/100, 100/100 | **4, 2** |

1. **Blocking SBI calls sit in the serial section.** The N2 response handlers
   call the SMF synchronously (`SendUpdateSmContextN2Info` at
   `internal/ngap/handler.go:650`, `:675`, `:795`, `:855`, `:980`, `:1006`;
   `SendUpdateSmContextDeactivateUpCnxState` at `:314`). In `paper` these run on
   the reader goroutine, so no other UE's message on that gNB connection can be
   decoded while one SMF call is in flight. **299 of 899 messages — a third —
   never reached a worker** (`worker_id = -1`): InitialContextSetupResponse
   ×100, PDUSessionResourceSetupResponse ×100, UEContextReleaseComplete ×98,
   NGSetup ×1; `blog` and `paper-early` handed off all of them. The paper's
   headline metric spans this phase (§IV.D), so expect `paper` to diverge from
   `blog` as the UE count grows, and report the serial section from the trace
   rather than only the end-to-end figure.

2. **A UE's NGAP half and NAS half run on different goroutines.** free5gc
   assumes one goroutine per UE; `AmfUe.Lock` (`internal/context/amf_ue.go:195`)
   guards the SBI↔signalling boundary, not two signalling goroutines. Every race
   found has one shape — the reader goroutine tears a UE down
   (`handleUEContextReleaseCompleteMain`, or `HandleSCTPNotification` →
   `AmfRan.Remove`) while a NAS worker is still in that UE's deregistration:

   | written on the reader goroutine | read in a NAS worker |
   |---|---|
   | `AmfUe.NASLog`/`GmmLog`, `amf_ue.go:380` | `internal/gmm/sm.go:27` |
   | the `AmfUe.RanUe` map, `delete()` at `amf_ue.go:354` | `ClearRegistrationRequestData` |

   The map entry is the serious one: a concurrent read and delete can abort the
   process, not merely yield a stale value. No run has hit that yet.
   `paper-early` has neither problem — one worker per UE, and its key comes from
   a cache rather than from the shared `AmfUe`.

3. **A UE whose identity arrives late changes worker once.** A registration the
   AMF cannot match by 5G-GUTI dispatches on the fallback key until the Identity
   Response has been processed, then on the IMSI. `paper-early` pins the first
   decision in its cache; pinning here would mean giving `paper` the same cache.
   Fresh SUCI registrations — the normal benchmark load — never move.

Neither the paper nor its evaluation discusses per-UE state sharing: its model
never splits a UE across two execution contexts.

## Build, test, run

```bash
cd amf
go build -o ../bin/amf ./cmd
go test ./internal/ngap/ ./pkg/factory/
```

Consequence 2 above makes `-race` worth running against a live registration
load, not just the unit tests:

```bash
go build -race -o ../bin/amf-race ./cmd
```

The binary is a drop-in replacement for the AMF of a free5gc v4.2.3 deployment
(same base commit, below): start it from your free5gc directory, with
`config/amfcfg.yaml` or your own `amfcfg.yaml`, and the rest of the core
(NRF, SMF, …) as normal:

```bash
./bin/amf -c config/amfcfg.yaml
```

**Optional per-message timing.** Set `AMF_BENCH_TRACE=/path/trace.csv` and the
AMF writes one CSV row per inbound NGAP message:

```
recv_ns, key_extracted_ns, submitted_ns, worker_start_ns, handled_ns,
worker_id, key, procedure_code, fallback
```

so time spent before dispatch (`submitted - recv`), waiting in the queue
(`worker_start - submitted`) and being handled (`handled - worker_start`) can be
separated. With the variable unset it does nothing beyond one atomic load per
message and a nil check at each hook.

In `paper` mode the serial section is larger by construction, and a message that
never reached NAS has **`worker_id = -1`** with the middle three timestamps
empty; for those rows `handled - recv` is the whole serial cost. Those rows are
how consequence 1 above is measured.

The trace is kept per gNB connection, not in one global, because there is one
SCTP reader goroutine per connection.

## What differs from upstream

`amf/` is free5gc's AMF, vendored with its own git history removed:

| | |
|---|---|
| Upstream | https://github.com/free5gc/amf |
| Base commit | `2962b9fdbdff1fb04de906063438e7acce761ce4` (tag `v1.4.5`, 2026-06-24) |
| Part of | free5gc v4.2.3 (this is the commit its `NFs/amf` submodule points at) |
| Licence | Apache-2.0, unchanged (`amf/LICENSE`) |

Changes relative to that commit:

| file | change |
|---|---|
| `internal/ngap/paper_dispatch.go` | new — the subscriber key, shared by both paper modes |
| `internal/ngap/paper_dispatch_test.go` | new — unit tests for it |
| `internal/ngap/paper_nas_pool.go` | new — `paper` mode's NAS-level worker pool and hand-off |
| `internal/ngap/paper_nas_pool_test.go` | new — routing, ordering, drain and trace-scoping tests |
| `internal/ngap/trace.go` | new — the optional per-message timing above |
| `internal/ngap/scheduler.go` | mode selection; one shared dispatch helper; trace hooks |
| `internal/ngap/service/service.go` | pick the mode per message |
| `internal/ngap/dispatcher.go` | one line: record the procedure code of a serially handled message |
| `internal/ngap/handler.go` | three call sites go through the NAS hand-off instead of calling `HandleNAS` directly |
| `internal/ngap/ue_id_extractor.go` | also returns the NGAP procedure code; a variant that takes an already-decoded PDU |
| `pkg/factory/config.go` | the `ngapSchedulerMode` setting, its validation and default |
| `pkg/factory/config_test.go` | tests for that setting |
| `pkg/service/init.go` | apply the mode, start the pool the mode needs; trace start and stop |

With `ngapSchedulerMode` unset or `blog`, the AMF behaves as upstream does:
`SubmitNAS` is a direct call to `HandleNAS`, and nothing else on the path
changes.

`internal/sbi/processor/callback.go:381` also calls `HandleNAS`, from an SBI
goroutine rather than the NGAP path. It is left as a direct call in every mode.

The sample config, `config/amfcfg.yaml`, is not part of `amf/` (upstream's AMF
repository ships none). It is free5gc v4.2.3's own `config/amfcfg.yaml` with the
`ngapSchedulerMode` setting added and nothing else changed.
