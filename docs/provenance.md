# Provenance of the vendored upstream code

`amf/` and `free-ran-ue/` are third-party projects, vendored into this
repository at a pinned commit and then modified. Their own `.git` directories
were removed so that this tree is a single repository, so this file is the only
remaining record of where the code came from. **Do not delete it.**

Both upstream projects are Apache-2.0; their `LICENSE` files are kept in place.

## `amf/` — free5gc AMF

| | |
|---|---|
| Upstream | https://github.com/free5gc/amf |
| Pinned commit | `2962b9fdbdff1fb04de906063438e7acce761ce4` |
| Tag at that commit | `v1.4.5` |
| Commit date | 2026-06-24 17:25:45 +0800 |
| Subject | Merge pull request #222 from neelareddybollu/fix/uplink-ran-config-transfer-nil-deref |

This is the exact commit that `~/free5gc` v4.2.3 carries as its `NFs/amf`
submodule, so the benchmark runs the same AMF the released free5gc does. The
other network functions are used as-is from `~/free5gc/bin/`.

### Local modifications

| File | Change |
|---|---|
| `internal/ngap/supi_dispatch.go` | new — the paper's dispatch policy (subscriber-identity key) |
| `internal/ngap/supi_dispatch_test.go` | new — unit tests for the above |
| `internal/ngap/trace.go` | new — per-message timing instrumentation, off unless `AMF_BENCH_TRACE` is set |
| `internal/ngap/scheduler.go` | dispatch-policy selection; trace hooks on the worker path |
| `internal/ngap/service/service.go` | pick the dispatch policy; open the trace on the reader goroutine |
| `internal/ngap/ue_id_extractor.go` | `ExtractUEIDWithMeta` also returns the NGAP procedure code |
| `pkg/factory/config.go` | new `ngapSchedulerMode: hash\|supi` setting (defaults to upstream behaviour) |
| `pkg/service/init.go` | trace lifecycle; log the active dispatch mode |

With `ngapSchedulerMode` unset or `hash`, behaviour is upstream's. That was
checked empirically, not assumed: the same workload on the pre-patch and
post-patch binaries completed in 11.14 s vs 11.15/11.17/11.26 s.

## `free-ran-ue/` — RAN/UE simulator

| | |
|---|---|
| Upstream | https://github.com/free-ran-ue/free-ran-ue |
| Pinned commit | `5962128fd13fb7fbd57056882d8b89391cf5b075` |
| Tag | `v2.5.0` |
| Commit date | 2026-08-20 15:43:46 +0800 |

### Local modifications

| File | Change |
|---|---|
| `ue/bench_trace.go` | new — per-UE milestone timing, off unless `RANUE_BENCH_TRACE` is set |
| `ue/ue.go` | four `benchMark()` calls at the registration and PDU-session milestones |
| `cmd/ue.go` | trace lifecycle, registered so it shuts down after the UEs do |

The simulator's stock logger prints whole-second timestamps, which cannot
express a per-UE registration latency; that is the only reason these changes
exist.

## Recovering the diffs

`docs/patches/` holds the same changes as patch files, so they can be read or
re-applied against a fresh upstream checkout without this repository's history:

```
docs/patches/amf-tracked.patch            # changes to existing AMF files
docs/patches/amf-new-files.patch          # AMF files added
docs/patches/free-ran-ue-tracked.patch    # changes to existing simulator files
docs/patches/free-ran-ue-new-files.patch  # simulator files added
```

To rebuild from upstream:

```bash
git clone https://github.com/free5gc/amf.git
cd amf && git checkout 2962b9fdbdff1fb04de906063438e7acce761ce4
git apply ../docs/patches/amf-tracked.patch ../docs/patches/amf-new-files.patch
```

## Environment this was built against

| | |
|---|---|
| free5gc | v4.2.3 (`~/free5gc`, commit `3b34a08`) — supplies every NF except the AMF |
| gtp5g | v0.10.2 |
| Go | 1.26.2 |
| OS | Ubuntu 24.04.4 LTS, kernel 7.0.0-31-generic |
| Host | 12 cores / 23 GB RAM; core pinned to CPUs 0-7, simulator to 8-11 |
