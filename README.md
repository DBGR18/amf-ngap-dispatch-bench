# AMF NGAP dispatch modes: `blog` and `paper`

A copy of free5gc's AMF with one addition: the AMF's NGAP worker pool can
dispatch incoming messages to its workers in one of two ways, selected by a
setting in `amfcfg.yaml`.

| mode | where it comes from |
|---|---|
| `blog` | free5gc's own worker pool, as described in the [free5gc blog post](https://free5gc.org/blog/20260429/20260429/) ([AMF PR #194](https://github.com/free5gc/amf/pull/194)). Dispatches on the NGAP UE ID. |
| `paper` | The dispatch idea from Nha & Nakao, *Multithreading-Based AMF Optimization for Pre-Slice Congestion Control in 5G Core Networks* (IEEE GC Wkshps 2025, pp. 2167–2172). Dispatches on the subscriber identity. |

Everything else — the worker pool, the per-worker queues, backpressure, the
shutdown drain — is free5gc's and is shared by both modes. The only thing that
differs is **how the dispatch key is computed** for each message.

> The load-generation and measurement harness used to compare the two modes is
> not published yet; this repository contains the implementation only.

## Configuration

A ready-to-use file is [`config/amfcfg.yaml`](config/amfcfg.yaml): free5gc
v4.2.3's stock `amfcfg.yaml` (loopback addresses, certificate paths relative to
the working directory, so it drops into a free5gc tree) with one added setting.
Adjust the addresses for any other deployment.

Three keys under `configuration:`:

```yaml
configuration:
  # ... the rest of the usual free5gc AMF config ...
  ngapSchedulerMode: paper   # blog | paper   (default: blog)
  ngapWorkerPoolSize: 4      # number of workers; 0 = runtime.NumCPU()
  ngapTaskBufferSize: 4096   # queue depth of each worker
```

| key | values | default |
|---|---|---|
| `ngapSchedulerMode` | `blog`, `paper` | `blog` (stock free5gc behaviour) |
| `ngapWorkerPoolSize` | integer ≥ 0 | `0`, meaning one worker per CPU |
| `ngapTaskBufferSize` | integer > 0 | `1000` |

- **Switching modes** means editing `ngapSchedulerMode` and restarting the AMF;
  the mode is read once at startup.
- Values are case-sensitive. Anything other than `blog` or `paper` (including
  `Paper`) is rejected at startup by the config validator.
- **Do not add a second copy of the worker keys.** free5gc's stock `amfcfg.yaml`
  already contains `ngapWorkerPoolSize` and `ngapTaskBufferSize` near the end of
  `configuration:`. Edit those lines. The YAML parser silently lets the *last*
  duplicate key win, so a stray earlier copy is ignored without any message and
  you end up with a different worker count than you configured.
- **Check it took effect** in the AMF log at startup:

  ```
  Initializing NGAP worker pool with 4 workers (buffer size: 4096, mode: paper)
  Initializing UE Scheduler with 4 workers
  ```

  The second line is the pool that was actually built; if it does not show the
  worker count you set, the config is not what you think it is.

## How the two modes dispatch

Both modes share one path. A single goroutine reads NGAP messages from the SCTP
socket and hands each to `dispatchToWorkerPool()`
(`amf/internal/ngap/service/service.go`), which computes a **dispatch key**,
picks `worker = key % N`, and puts the message on that worker's own buffered
channel. Each worker drains its own channel in order and does the full message
handling. The mode only changes step 1:

```
SCTP reader goroutine
  │
  │  dispatchToWorkerPool()                     internal/ngap/service/service.go
  │
  ├─ mode = blog ─────► ExtractUEIDWithMeta()   internal/ngap/ue_id_extractor.go
  │                     decode the NGAP PDU, read the UE's NGAP ID out of it
  │
  └─ mode = paper ────► PaperDispatchKey()      internal/ngap/paper_dispatch.go
                        decode the NGAP PDU; on a UE's first message also decode
                        the NAS payload and read its subscriber identity;
                        on later messages look that identity up
  │
  ▼  worker = key % N                           internal/ngap/scheduler.go
  ▼  that worker's own channel  ──►  worker goroutine handles the message
```

`SetSchedulerMode()` is called from `pkg/service/init.go` right before the pool
is created, and `dispatchToWorkerPool()` checks the mode on every message.

|  | `blog` | `paper` |
|---|---|---|
| dispatch key | RAN-UE-NGAP-ID on the first message (InitialUEMessage), AMF-UE-NGAP-ID on every later one | the UE's IMSI (MCC+MNC+MSIN), taken from its SUCI, on every message |
| known after | the NGAP decode | the NAS decode (first message), or a lookup (later messages) |
| extra work on the reader goroutine | none | a NAS decode on InitialUEMessage; a map lookup on every other UE message |
| key stable for the UE's lifetime | no — it changes when the AMF assigns AMF-UE-NGAP-ID, so a UE's later messages can land on a different worker | yes — a UE's messages always go to the same worker queue |
| messages with no UE (e.g. NGSetupRequest) | worker 0 | worker 0 |

## How the paper's method is implemented

The paper classifies a UE from an identity that is available before any slice
information exists, and assigns UEs to threads on that basis. Here that is the
identity-keyed dispatch in `amf/internal/ngap/paper_dispatch.go`:

1. **First message (InitialUEMessage).** Read the RAN-UE-NGAP-ID and the NAS
   PDU out of the NGAP message. Decode the NAS Registration Request (plain NAS —
   there is no security context yet), require its mobile identity to be a SUCI,
   and turn it into a string such as `suci-0-208-93-0000-0-0-0000000001`
   (`subscriberKeyFromNAS`). `imsiKey` checks the protection scheme is `0`
   (null-scheme, so the MSIN is not encrypted) and concatenates the MCC, MNC and
   MSIN digits into the full IMSI, e.g. `208930000000001`. That number is the
   dispatch key, and it is remembered against the RAN-UE-NGAP-ID.
2. **Every later message.** These carry only the AMF-UE-NGAP-ID, not the
   subscriber identity. `lookupPaperKey` finds the key by AMF-UE-NGAP-ID in a
   `sync.Map`; on the first miss it bridges through the AMF context
   (`RanUeFindByAmfUeNgapID`) back to the RAN-UE-NGAP-ID remembered in step 1,
   and caches the result.
3. **Pick the worker.** `key % N`. Because the key never changes, all of a UE's
   messages from registration onward reach one worker, in order.


**Not implemented.** The paper additionally splits UEs into two priority
classes by IMSI parity (even IMSI to threads `0..N-2`, odd IMSI confined to
thread `N-1`). That prioritisation is **not** here; only the identity-keyed
dispatch is.


## Build, test, run

```bash
cd amf
go build -o ../bin/amf ./cmd
go test ./internal/ngap/ ./pkg/factory/
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
| `internal/ngap/paper_dispatch.go` | new — the `paper` dispatch key |
| `internal/ngap/paper_dispatch_test.go` | new — unit tests for it |
| `internal/ngap/trace.go` | new — the optional per-message timing above |
| `internal/ngap/scheduler.go` | mode selection; one shared dispatch helper both modes use; trace hooks |
| `internal/ngap/service/service.go` | choose `blog` or `paper` per message |
| `internal/ngap/ue_id_extractor.go` | also returns the NGAP procedure code; a variant that takes an already-decoded PDU |
| `pkg/factory/config.go` | the `ngapSchedulerMode` setting, its validation and default |
| `pkg/service/init.go` | apply the mode before the pool starts; trace start and stop |

With `ngapSchedulerMode` unset or `blog`, the AMF behaves as upstream does.

The sample config, `config/amfcfg.yaml`, is not part of `amf/` (upstream's AMF
repository ships none). It is free5gc v4.2.3's own `config/amfcfg.yaml` with the
`ngapSchedulerMode` setting added and nothing else changed.
