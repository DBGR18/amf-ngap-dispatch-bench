package ngap

import (
	"encoding/csv"
	"encoding/json"
	"github.com/free5gc/amf/internal/benchtrace"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/free5gc/amf/internal/logger"
)

// Benchmark instrumentation for amf-mt-bench.
//
// Set AMF_BENCH_TRACE=<path> to have every inbound NGAP message emit one CSV
// row describing where its time went:
//
//	recv ──► key_extracted ──► submitted ──► worker_start ──► handled
//	     └── serial section ──┘             └── queue wait ─┘└ processing ┘
//
// The serial section is the part that runs on the single SCTP reader
// goroutine, and is therefore the Amdahl bottleneck that no amount of workers
// can shrink. Measuring it is the whole point of the benchmark: the two
// dispatch policies under test differ precisely in how much work they do
// there.
//
// When the variable is unset every hook costs one atomic load.

type MsgTrace struct {
	RecvMono         int64
	KeyExtractedMono int64
	SubmittedMono    int64
	WorkerStartMono  int64
	HandledMono      int64
	ConnectionID     string
	MessageSeq       uint64
	Recv             time.Time
	KeyExtracted     time.Time
	Submitted        time.Time
	WorkerStart      time.Time
	Handled          time.Time

	WorkerID      int
	Key           uint64
	ProcedureCode int64
	Fallback      bool // paper mode only: key came from the blog fallback path
}

var (
	traceEnabled atomic.Bool
	traceCh      chan *MsgTrace
	traceDropped atomic.Uint64
	traceWG      sync.WaitGroup
	traceOnce    sync.Once
	tracePath    string
	traceWritten atomic.Uint64
	traceErr     atomic.Value
	messageSeqs  sync.Map // connection ID -> *atomic.Uint64
)

// InitTrace wires up the CSV writer if AMF_BENCH_TRACE names an output file.
// Safe to call more than once; only the first call has an effect.
func InitTrace() {
	traceOnce.Do(func() {
		path := os.Getenv("AMF_BENCH_TRACE")
		if path == "" {
			return
		}

		f, err := os.Create(path)
		if err != nil {
			logger.NgapLog.Errorf("bench trace: cannot create %s: %v", path, err)
			return
		}

		tracePath = path

		// Generous buffer: dropping rows would silently bias the results, so
		// prefer memory over loss and report any drop that still happens.
		traceCh = make(chan *MsgTrace, 1<<16)
		traceEnabled.Store(true)

		traceWG.Add(1)
		go func() {
			defer traceWG.Done()
			defer func() {
				if err := f.Close(); err != nil {
					traceErr.Store(err.Error())
				}
			}()

			w := csv.NewWriter(f)
			defer func() {
				w.Flush()
				if err := w.Error(); err != nil {
					traceErr.Store(err.Error())
				}
			}()

			if err := w.Write([]string{
				"recv_ns", "key_extracted_ns", "submitted_ns", "worker_start_ns", "handled_ns",
				"worker_id", "key", "procedure_code", "fallback",
				"schema_version", "run_id", "mode", "worker_count", "connection_id", "message_seq",
				"recv_mono_ns", "key_extracted_mono_ns", "submitted_mono_ns", "worker_start_mono_ns", "handled_mono_ns",
			}); err != nil {
				traceErr.Store(err.Error())
				logger.NgapLog.Errorf("bench trace: header write failed: %v", err)
				return
			}
			w.Flush()

			// Flush on a timer as well as at shutdown: a run that ends in
			// SIGKILL still leaves usable data on disk instead of an empty
			// file, and the CSV can be watched live while a run is going.
			flush := time.NewTicker(500 * time.Millisecond)
			defer flush.Stop()

			for {
				select {
				case t, ok := <-traceCh:
					if !ok {
						return
					}
					if err := w.Write([]string{
						nanos(t.Recv), nanos(t.KeyExtracted), nanos(t.Submitted),
						nanos(t.WorkerStart), nanos(t.Handled),
						strconv.Itoa(t.WorkerID),
						strconv.FormatUint(t.Key, 10),
						strconv.FormatInt(t.ProcedureCode, 10),
						strconv.FormatBool(t.Fallback),
						"2", os.Getenv("RUN_ID"), os.Getenv("AMF_MODE"), os.Getenv("AMF_WORKERS"), t.ConnectionID, strconv.FormatUint(t.MessageSeq, 10),
						mono(t.RecvMono), mono(t.KeyExtractedMono), mono(t.SubmittedMono), mono(t.WorkerStartMono), mono(t.HandledMono),
					}); err != nil {
						traceErr.Store(err.Error())
						logger.NgapLog.Errorf("bench trace: row write failed: %v", err)
						return
					}
					traceWritten.Add(1)
				case <-flush.C:
					w.Flush()
					if err := w.Error(); err != nil {
						traceErr.Store(err.Error())
						return
					}
				}
			}
		}()

		logger.NgapLog.Infof("bench trace enabled -> %s", path)
	})
}

// StopTrace flushes and closes the trace file. Called during AMF shutdown.
func StopTrace() {
	if !traceEnabled.CompareAndSwap(true, false) {
		return
	}
	close(traceCh)
	traceWG.Wait()
	summary := map[string]any{"schema_version": 2, "run_id": os.Getenv("RUN_ID"), "rows_written": traceWritten.Load(), "rows_dropped": traceDropped.Load(), "submit_blocked_ns": submitBlockedNs.Load(), "clean_shutdown": true}
	if e := traceErr.Load(); e != nil {
		summary["error"] = e.(string)
		summary["clean_shutdown"] = false
	}
	if data, err := json.MarshalIndent(summary, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(filepath.Dir(tracePath), "amf_trace_summary.json"), append(data, '\n'), 0640); err != nil {
			logger.NgapLog.Errorf("bench trace summary: %v", err)
		}
	}
	if n := traceDropped.Load(); n > 0 {
		logger.NgapLog.Warnf("bench trace: dropped %d rows (buffer full) - results are incomplete", n)
	}
	if n := submitBlockedNs.Load(); n > 0 {
		// A worker queue filled up and stalled the reader goroutine. That time
		// is charged to queue wait in the CSV, so surface it separately or the
		// serial-section numbers read lower than they really were.
		logger.NgapLog.Warnf("bench trace: reader blocked %s total on full worker queues",
			time.Duration(n))
	}
}

var submitBlockedNs atomic.Int64

// AddSubmitBlocked records reader-goroutine time lost to a full worker queue.
func AddSubmitBlocked(d time.Duration) {
	if d > 0 && traceEnabled.Load() {
		submitBlockedNs.Add(int64(d))
	}
}

// NewMsgTrace starts a trace for a message just read off the wire.
// Returns nil when tracing is disabled, and every hook tolerates a nil trace.
func NewMsgTrace(conns ...net.Conn) *MsgTrace {
	if !traceEnabled.Load() {
		return nil
	}
	t := &MsgTrace{Recv: time.Now(), RecvMono: benchtrace.ClockMonotonicNS()}
	if len(conns) > 0 && conns[0] != nil {
		t.ConnectionID = benchtrace.ConnectionID(conns[0])
		if t.ConnectionID == "" {
			t.ConnectionID = conns[0].RemoteAddr().String()
		}
		counter, _ := messageSeqs.LoadOrStore(t.ConnectionID, &atomic.Uint64{})
		t.MessageSeq = counter.(*atomic.Uint64).Add(1)
	}
	return t
}

func (t *MsgTrace) MarkKeyExtracted(key uint64, procedureCode int64, fallback bool) {
	if t == nil {
		return
	}
	t.KeyExtractedMono = benchtrace.ClockMonotonicNS()
	t.KeyExtracted = time.Now()
	t.Key = key
	t.ProcedureCode = procedureCode
	t.Fallback = fallback
}

// MarkSerial tags a message that paper mode handles from end to end on the
// reader goroutine, because it never reaches the NAS hand-off. worker_id -1
// identifies those rows in the CSV; MarkSubmitted overwrites it for the
// messages that do get handed off.
func (t *MsgTrace) MarkSerial(procedureCode int64) {
	if t == nil {
		return
	}
	t.ProcedureCode = procedureCode
	t.WorkerID = -1
}

func (t *MsgTrace) MarkSubmitted(workerID int) {
	if t == nil {
		return
	}
	t.SubmittedMono = benchtrace.ClockMonotonicNS()
	t.Submitted = time.Now()
	t.WorkerID = workerID
}

func (t *MsgTrace) MarkWorkerStart() {
	if t == nil {
		return
	}
	t.WorkerStartMono = benchtrace.ClockMonotonicNS()
	t.WorkerStart = time.Now()
}

// MarkHandled closes the trace and queues it for writing.
func (t *MsgTrace) MarkHandled() {
	if t == nil || !traceEnabled.Load() {
		return
	}
	t.HandledMono = benchtrace.ClockMonotonicNS()
	t.Handled = time.Now()

	select {
	case traceCh <- t:
	default:
		traceDropped.Add(1)
	}
}

func nanos(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

func mono(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
