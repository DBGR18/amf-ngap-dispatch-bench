# amf-mt-bench

Comparing two ways of parallelising NGAP handling in the 5G AMF, under a
registration storm:

- **`hash`** — free5gc v4.2.3's NGAP worker pool: the dispatch decision is made
  on the SCTP reader goroutine as soon as the NGAP PDU is decoded, keyed on the
  NGAP UE ID. The key changes from RAN-UE-NGAP-ID to AMF-UE-NGAP-ID partway
  through registration, so a UE can move between workers.
- **`supi`** — the mechanism from Nha & Nakao, *Multithreading-Based AMF
  Optimization for Pre-Slice Congestion Control in 5G Core Networks*
  (IEEE GC Wkshps 2025, pp. 2167–2172): the key is the subscriber identity from
  the NAS Registration Request, which costs an extra NAS decode in the serial
  section but never changes for the life of the UE.

Both arms are the same binary, the same core, the same load. They differ in one
function. The question is how the dispatch point and the dispatch key show up in
the **StartTime phase** and the phases after it.

The paper's IMSI-parity prioritisation is deliberately **not** implemented — see
[docs/design.md](docs/design.md) for why.

## Layout

```
amf/              free5gc AMF, vendored and patched      (docs/provenance.md)
free-ran-ue/      RAN/UE simulator, vendored and patched (docs/provenance.md)
config/           NF configs; amfcfg.tmpl.yaml is rendered per arm
bench/            provisioning, run orchestration, parsing
  provision/      bulk subscriber provisioning (Go)
  run_core.sh     start one core for one arm
  run_ran.sh      fire one registration storm
  run_all.sh      the whole experiment matrix
  collect/        trace + pidstat -> metrics
results/parsed/   per-run metrics (tracked)
results/raw/      logs and traces (not tracked, regenerable)
docs/             design, provenance, results
```

## Running it

Needs root (network namespaces, gtp5g, TUN devices) and a free5gc v4.2.3 tree at
`~/free5gc` supplying the non-AMF network functions.

```bash
# once
make -C free-ran-ue bin
(cd amf && go build -o ../bin/amf-bench ./cmd)
(cd bench/provision && go build -o provision .)
sudo ./free-ran-ue/script/namespace-script/free-ran-ue-namespace.sh up

# the full matrix: 2 policies x {1,2,4,8} workers x {100,200,400} UEs x 10 runs
sudo ./bench/run_all.sh

# or one cell
ARMS="supi:4" UE_COUNTS="100" RUNS=1 sudo -E ./bench/run_all.sh

# metrics
python3 bench/collect/aggregate.py results/raw -o results/parsed/summary.csv
```

## Reading the results

Read `completion_rate` before any latency number. Under-provisioned
configurations do not just get slower, they collapse — NAS timers expire and the
AMF abandons UEs — and a latency mean over the survivors of a collapse is not
comparable to one where everybody finished.

Full metric definitions, deviations from the paper, and the harness guards
against silent misconfiguration: [docs/design.md](docs/design.md).

## Third-party code

`amf/` and `free-ran-ue/` are Apache-2.0 upstream projects vendored at pinned
commits and modified; their licences are kept in place and every change is
recorded, with recoverable patches, in [docs/provenance.md](docs/provenance.md).
