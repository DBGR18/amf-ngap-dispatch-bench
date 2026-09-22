package ngap

import (
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	amf_context "github.com/free5gc/amf/internal/context"
	"github.com/free5gc/amf/internal/logger"
	amf_nas "github.com/free5gc/amf/internal/nas"
	"github.com/free5gc/amf/pkg/factory"
	"github.com/free5gc/ngap/ngapType"
)

// The paper mode's worker pool.
//
// Where blog and paper-early hand a raw NGAP message to a worker and let the
// worker run the whole handler, paper mode runs the NGAP handler on the SCTP
// reader goroutine and hands off at the NAS boundary - the point the paper's
// Fig. 4 calls classification, and the last point at which nothing UE-specific
// has been decided yet. Reaching it costs no extra decoding: the handler has
// already resolved the UE's identity for its own reasons by then.
//
// This pool is deliberately separate from UEScheduler rather than a second
// dispatch policy inside it. The two carry different work: UEScheduler's task
// is (net.Conn, []byte) and its worker calls Dispatch; this one's task is an
// already-resolved *RanUe plus a NAS PDU. Sharing one Task type would mean
// changing free5gc's own worker, and the blog arm would no longer be upstream's
// code unmodified.
//
// Consequences of the later hand-off, both of them real and both reported in
// the README rather than worked around:
//
//   - NGAP messages that never reach NAS (InitialContextSetupResponse,
//     PDUSessionResourceSetupResponse, UEContextReleaseComplete, ...) are
//     handled entirely on the reader goroutine, including the synchronous SBI
//     calls they make to the SMF.
//   - A UE's NGAP half and NAS half run on different goroutines, so the
//     reader may be inside message N+1's handler while a worker is still in
//     message N's NAS processing for the same UE. free5gc assumes one
//     goroutine per UE at a time; paper mode gives that up, paper-early does
//     not.

type nasTask struct {
	ranUe          *amf_context.RanUe
	procedureCode  int64
	nasPdu         []byte
	initialMessage bool

	// Trace is nil unless AMF_BENCH_TRACE is set. The channel hand-off gives
	// the reader goroutine's writes a happens-before edge to the worker's.
	trace *MsgTrace
}

// nasHandlerFunc is amf_nas.HandleNAS, injected so the pool's routing and
// ordering guarantees can be exercised without running real NAS processing.
type nasHandlerFunc func(ranUe *amf_context.RanUe, procedureCode int64, nasPdu []byte, initialMessage bool)

type nasWorker struct {
	id       int
	taskChan chan nasTask
	stopChan chan struct{}
	stopOnce sync.Once
	handler  nasHandlerFunc
	wg       *sync.WaitGroup
}

func newNasWorker(id, bufferSize int, handler nasHandlerFunc, wg *sync.WaitGroup) *nasWorker {
	w := &nasWorker{
		id:       id,
		taskChan: make(chan nasTask, bufferSize),
		stopChan: make(chan struct{}),
		handler:  handler,
		wg:       wg,
	}
	wg.Add(1)
	go w.run()
	return w
}

func (w *nasWorker) run() {
	defer func() {
		if p := recover(); p != nil {
			logger.NgapLog.Errorf("NAS worker %d panic: %v", w.id, p)
		}
		w.wg.Done()
	}()
	logger.NgapLog.Infof("NAS worker %d started", w.id)

	for {
		select {
		case task := <-w.taskChan:
			w.handle(task)
		case <-w.stopChan:
			logger.NgapLog.Infof("NAS worker %d: shutdown signal received, draining queue...", w.id)
			w.drainAndExit()
			return
		}
	}
}

func (w *nasWorker) drainAndExit() {
	for {
		select {
		case task := <-w.taskChan:
			w.handle(task)
		default:
			logger.NgapLog.Infof("NAS worker %d: queue drained, stopped.", w.id)
			return
		}
	}
}

func (w *nasWorker) handle(task nasTask) {
	task.trace.MarkWorkerStart()
	w.handler(task.ranUe, task.procedureCode, task.nasPdu, task.initialMessage)
	task.trace.MarkHandled()
}

// submit queues a task, blocking while the buffer is full so that backpressure
// reaches the reader goroutine exactly as it does in the other two modes.
func (w *nasWorker) submit(task nasTask) bool {
	select {
	case w.taskChan <- task:
		return true
	case <-w.stopChan:
		logger.NgapLog.Warnf("NAS worker %d stopped, rejecting task", w.id)
		return false
	}
}

func (w *nasWorker) stop() {
	w.stopOnce.Do(func() { close(w.stopChan) })
}

// NasScheduler routes NAS processing to a worker by subscriber identity.
type NasScheduler struct {
	workers    []*nasWorker
	numWorkers int
	wg         sync.WaitGroup
}

var nasScheduler atomic.Pointer[NasScheduler]

// InitNasScheduler starts the paper mode's pool. Calling it in any other mode
// is a no-op, so the other arms never pay for goroutines they do not use.
func InitNasScheduler(numWorkers, taskBufferSize int) {
	s := newNasScheduler(numWorkers, taskBufferSize, amf_nas.HandleNAS)
	logger.NgapLog.Infof("Initializing NAS Scheduler with %d workers (buffer size: %d)",
		s.numWorkers, taskBufferSize)
	nasScheduler.Store(s)
}

func newNasScheduler(numWorkers, taskBufferSize int, handler nasHandlerFunc) *NasScheduler {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}
	if taskBufferSize <= 0 {
		taskBufferSize = 4096
	}

	s := &NasScheduler{
		workers:    make([]*nasWorker, numWorkers),
		numWorkers: numWorkers,
	}
	for i := 0; i < numWorkers; i++ {
		s.workers[i] = newNasWorker(i, taskBufferSize, handler, &s.wg)
	}
	return s
}

// ShutdownNasScheduler drains and stops the pool. Safe when it was never started.
func ShutdownNasScheduler() {
	s := nasScheduler.Swap(nil)
	if s == nil {
		return
	}
	logger.NgapLog.Info("Shutting down NAS Scheduler and all workers...")
	for _, w := range s.workers {
		w.stop()
	}
	s.wg.Wait()
	logger.NgapLog.Info("All NAS workers shut down successfully")
}

// SubmitNAS is the hand-off point for a UE whose context the NGAP handler has
// already resolved. Outside paper mode - and before the pool exists - it is a
// direct call, so blog and paper-early keep upstream's exact call graph.
func SubmitNAS(ranUe *amf_context.RanUe, procedureCode int64, nasPdu []byte, initialMessage bool) {
	s := nasScheduler.Load()
	if s == nil || ranUe == nil {
		amf_nas.HandleNAS(ranUe, procedureCode, nasPdu, initialMessage)
		return
	}

	key, ok := SubscriberKeyFromRanUe(ranUe)
	s.dispatch(ranUe, procedureCode, nasPdu, initialMessage, key, ok)
}

// SubmitInitialNAS is SubmitNAS for an InitialUEMessage, where no AmfUe is
// attached yet for a UE registering for the first time. The identity the
// handler resolved a few lines earlier is passed in rather than re-derived:
// that is the whole economy of this hand-off point.
func SubmitInitialNAS(ranUe *amf_context.RanUe, procedureCode int64, nasPdu []byte,
	id, idType string,
) {
	s := nasScheduler.Load()
	if s == nil || ranUe == nil {
		amf_nas.HandleNAS(ranUe, procedureCode, nasPdu, true)
		return
	}

	key, ok := uint64(0), false
	if idType == "SUCI" {
		key, ok = imsiKey(id)
	}
	if !ok {
		// A 5G-S-TMSI or GUTI registration: the identity is not in the
		// message, but findAmfUe may have matched an existing context.
		key, ok = SubscriberKeyFromRanUe(ranUe)
	}
	s.dispatch(ranUe, procedureCode, nasPdu, true, key, ok)
}

func (s *NasScheduler) dispatch(ranUe *amf_context.RanUe, procedureCode int64, nasPdu []byte,
	initialMessage bool, key uint64, resolved bool,
) {
	fallback := !resolved
	if fallback {
		// The AMF-UE-NGAP-ID is stable for the UE's lifetime, so a UE that
		// falls back stays on one worker even though it is not on the one its
		// IMSI would pick.
		key = uint64(ranUe.AmfUeNgapId)
		logger.NgapLog.Tracef("paper dispatch: no subscriber identity for AmfUeNgapId %d, falling back",
			ranUe.AmfUeNgapId)
	}

	idx := int(key % uint64(s.numWorkers))
	worker := s.workers[idx]

	task := nasTask{
		ranUe:          ranUe,
		procedureCode:  procedureCode,
		nasPdu:         nasPdu,
		initialMessage: initialMessage,
		trace:          takeSerialTrace(ranUeConn(ranUe)),
	}

	if task.trace == nil {
		worker.submit(task)
		return
	}

	task.trace.MarkKeyExtracted(key, procedureCode, fallback)
	task.trace.MarkSubmitted(idx)

	start := time.Now()
	worker.submit(task)
	AddSubmitBlocked(time.Since(start))
}

// serialTraces carries each in-flight message's trace across the NGAP handler,
// without threading a parameter through every handler signature.
//
// There is one SCTP reader goroutine per gNB connection, so this is keyed by
// connection rather than kept in a single variable: with two gNBs a single
// variable would be both a data race and a way for one gNB's message to claim
// another's trace. Within one connection the handler is strictly sequential, so
// one entry per connection is all that is ever needed.
//
// Nothing is stored unless paper mode is active and AMF_BENCH_TRACE is set, so
// an untraced run does not touch this map at all.
var serialTraces sync.Map // net.Conn -> *MsgTrace

func serialTracingActive() bool {
	return schedulerMode == factory.NgapSchedulerModePaper && traceEnabled.Load()
}

// BeginSerialTrace opens a trace that the NGAP handler will carry to the NAS
// hand-off. Returns nil outside paper mode or with tracing off.
func BeginSerialTrace(conn net.Conn) *MsgTrace {
	if !serialTracingActive() || conn == nil {
		return nil
	}
	t := NewMsgTrace()
	serialTraces.Store(conn, t)
	return t
}

// takeSerialTrace claims the in-flight trace for a connection, so that whoever
// finishes the message can tell whether it was handed to a worker or handled
// on the reader goroutine from end to end.
func takeSerialTrace(conn net.Conn) *MsgTrace {
	if conn == nil {
		return nil
	}
	if v, ok := serialTraces.LoadAndDelete(conn); ok {
		return v.(*MsgTrace)
	}
	return nil
}

// EndSerialTrace closes out a message that never reached the NAS hand-off, and
// was therefore handled from end to end on the reader goroutine. Calling it
// after every message also guarantees the map does not accumulate entries.
func EndSerialTrace(conn net.Conn) {
	if t := takeSerialTrace(conn); t != nil {
		t.MarkHandled()
	}
}

// noteSerialPDU records what kind of message the reader goroutine is working
// on. Called from Dispatch with the PDU it has just decoded, so the serial
// section is not charged for a decode it would not otherwise do.
func noteSerialPDU(conn net.Conn, pdu *ngapType.NGAPPDU) {
	if !serialTracingActive() || pdu == nil || conn == nil {
		return
	}

	v, ok := serialTraces.Load(conn)
	if !ok {
		return
	}

	procedureCode := int64(-1)
	switch pdu.Present {
	case ngapType.NGAPPDUPresentInitiatingMessage:
		if pdu.InitiatingMessage != nil {
			procedureCode = pdu.InitiatingMessage.ProcedureCode.Value
		}
	case ngapType.NGAPPDUPresentSuccessfulOutcome:
		if pdu.SuccessfulOutcome != nil {
			procedureCode = pdu.SuccessfulOutcome.ProcedureCode.Value
		}
	case ngapType.NGAPPDUPresentUnsuccessfulOutcome:
		if pdu.UnsuccessfulOutcome != nil {
			procedureCode = pdu.UnsuccessfulOutcome.ProcedureCode.Value
		}
	}
	v.(*MsgTrace).MarkSerial(procedureCode)
}

// ranUeConn is the gNB connection a RanUe belongs to, which is the reader
// goroutine whose trace this message is part of.
func ranUeConn(ranUe *amf_context.RanUe) net.Conn {
	if ranUe == nil || ranUe.Ran == nil {
		return nil
	}
	return ranUe.Ran.Conn
}
