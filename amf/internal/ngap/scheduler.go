package ngap

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/free5gc/amf/internal/logger"
	"github.com/free5gc/amf/pkg/factory"
)

// Task represents a work item to be processed by a worker.
// It contains the UE identifier and the raw NGAP message.
type Task struct {
	UEID    uint64   // AMF-UE-NGAP-ID or RAN-UE-NGAP-ID
	Conn    net.Conn // The network connection for this message
	Message []byte   // The raw NGAP message bytes

	// Trace is nil unless AMF_BENCH_TRACE is set. The channel hand-off gives
	// the reader goroutine's writes a happens-before edge to the worker's, so
	// both sides may touch it without further synchronisation.
	Trace *MsgTrace
}

// Worker represents a goroutine that processes tasks from its dedicated queue.
type Worker struct {
	ID       int
	taskChan chan Task
	stopChan chan struct{} // Signal channel for shutdown
	stopOnce sync.Once     // Ensures stopChan is closed only once
	handler  func(conn net.Conn, msg []byte)
	wg       *sync.WaitGroup
}

// NewWorker creates and starts a new worker goroutine.
func NewWorker(id int, bufferSize int, handler func(conn net.Conn, msg []byte), wg *sync.WaitGroup) *Worker {
	w := &Worker{
		ID:       id,
		taskChan: make(chan Task, bufferSize),
		stopChan: make(chan struct{}),
		handler:  handler,
		wg:       wg,
	}
	wg.Add(1)
	go w.run()
	return w
}

// run is the main event loop for the worker.
func (w *Worker) run() {
	defer func() {
		if p := recover(); p != nil {
			logger.NgapLog.Errorf("Worker %d panic: %v", w.ID, p)
		}
		w.wg.Done()
	}()
	logger.NgapLog.Infof("Worker %d started", w.ID)

	for {
		select {
		case task := <-w.taskChan:
			logger.NgapLog.Debugf("Worker %d processing task for UE ID %d (ensuring per-UE sequentiality)",
				w.ID, task.UEID)
			task.Trace.MarkWorkerStart()
			w.handler(task.Conn, task.Message)
			task.Trace.MarkHandled()

		case <-w.stopChan:
			logger.NgapLog.Infof("Worker %d: shutdown signal received, draining queue...", w.ID)
			w.drainAndExit()
			return
		}
	}
}

// drainAndExit consumes remaining tasks in the buffer without blocking.
func (w *Worker) drainAndExit() {
	for {
		select {
		case task := <-w.taskChan:
			logger.NgapLog.Debugf("Worker %d processing residual task for UE ID %d", w.ID, task.UEID)
			task.Trace.MarkWorkerStart()
			w.handler(task.Conn, task.Message)
			task.Trace.MarkHandled()
		default:
			// Channel is empty, exit safely
			logger.NgapLog.Infof("Worker %d: queue drained, stopped.", w.ID)
			return
		}
	}
}

// Submit submits a task to this worker's queue.
// Returns true if the task was successfully queued, false if the worker is stopped.
func (w *Worker) Submit(task Task) bool {
	select {
	case w.taskChan <- task:
		// Successfully queued (blocks here if buffer is full, providing backpressure)
		return true
	case <-w.stopChan:
		// Worker stopped (either before submission or while waiting). Unblock and return false.
		logger.NgapLog.Warnf("Worker %d stopped, rejecting task for UE ID %d", w.ID, task.UEID)
		return false
	}
}

// Stop signals the worker to shut down.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopChan)
	})
}

// UEScheduler distributes NGAP tasks to workers based on UE ID.
type UEScheduler struct {
	workers    []*Worker
	numWorkers int
	wg         sync.WaitGroup
}

// NewUEScheduler creates a new UE scheduler with the specified number of workers.
func NewUEScheduler(numWorkers int, taskBufferSize int, handler func(conn net.Conn, msg []byte)) *UEScheduler {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	logger.NgapLog.Infof("Initializing UE Scheduler with %d workers", numWorkers)

	scheduler := &UEScheduler{
		workers:    make([]*Worker, numWorkers),
		numWorkers: numWorkers,
	}

	for i := 0; i < numWorkers; i++ {
		scheduler.workers[i] = NewWorker(i, taskBufferSize, handler, &scheduler.wg)
	}

	return scheduler
}

// schedulerMode selects the dispatch policy under test. It is written once at
// startup, before any worker exists, and only read afterwards.
var schedulerMode = factory.NgapSchedulerModeHash

// SetSchedulerMode picks the dispatch policy. Call before InitScheduler.
func SetSchedulerMode(mode string) {
	if mode == factory.NgapSchedulerModeSupi {
		schedulerMode = factory.NgapSchedulerModeSupi
		return
	}
	schedulerMode = factory.NgapSchedulerModeHash
}

// SchedulerMode reports the active dispatch policy.
func SchedulerMode() string { return schedulerMode }

// DispatchTask dispatches a task to the appropriate worker based on UE ID hashing.
func (s *UEScheduler) DispatchTask(task Task) bool {
	return s.dispatchToIndex(task, s.hashUEID(task.UEID), "hash-based routing")
}

// dispatchToIndex hands a task to one specific worker. Both dispatch policies
// funnel through here so the instrumentation stays identical between them.
func (s *UEScheduler) dispatchToIndex(task Task, workerIndex int, why string) bool {
	worker := s.workers[workerIndex]

	logger.NgapLog.Debugf("Dispatching UE ID %d to Worker %d (%s)", task.UEID, workerIndex, why)

	// Untraced runs must cost exactly what upstream costs, or the control arm
	// is no longer a control: no extra clock reads on this path.
	if task.Trace == nil {
		return worker.Submit(task)
	}

	// Stamped before the hand-off: once the task is in the channel it belongs
	// to the worker, and the reader goroutine must not write to it any more.
	task.Trace.MarkSubmitted(workerIndex)

	start := time.Now()
	ok := worker.Submit(task)
	AddSubmitBlocked(time.Since(start))
	return ok
}

// hashUEID computes a hash of the UE ID and maps it to a worker index.
// This ensures all messages for the same UE go to the same worker.
func (s *UEScheduler) hashUEID(ueID uint64) int {
	return int(ueID % uint64(s.numWorkers))
}

// Shutdown gracefully shuts down all workers.
func (s *UEScheduler) Shutdown() {
	logger.NgapLog.Info("Shutting down UE Scheduler and all workers...")

	for i, worker := range s.workers {
		logger.NgapLog.Infof("Closing task channel for Worker %d", i)
		worker.Stop()
	}

	s.wg.Wait()
	logger.NgapLog.Info("All workers shut down successfully")
}

// Global scheduler instance
var (
	globalScheduler     *UEScheduler
	globalSchedulerOnce sync.Once
	schedulerMutex      sync.RWMutex
)

// InitScheduler initializes the global UE scheduler.
// Should be called once during AMF startup.
func InitScheduler(numWorkers int, taskBufferSize int, handler func(conn net.Conn, msg []byte)) {
	globalSchedulerOnce.Do(func() {
		// Apply sensible defaults if invalid values provided
		if numWorkers <= 0 {
			numWorkers = runtime.NumCPU()
		}
		if taskBufferSize <= 0 {
			taskBufferSize = 4096 // Default buffer size
		}

		schedulerMutex.Lock()
		defer schedulerMutex.Unlock()

		globalScheduler = NewUEScheduler(numWorkers, taskBufferSize, handler)
		logger.NgapLog.Infof("Global UE Scheduler initialized with %d workers, buffer size %d",
			numWorkers, taskBufferSize)
	})
}

// GetScheduler returns the global scheduler instance.
func GetScheduler() (*UEScheduler, error) {
	schedulerMutex.RLock()
	defer schedulerMutex.RUnlock()

	if globalScheduler == nil {
		return nil, fmt.Errorf("scheduler not initialized")
	}
	return globalScheduler, nil
}

// ShutdownScheduler gracefully shuts down the global scheduler.
func ShutdownScheduler() {
	schedulerMutex.Lock()
	defer schedulerMutex.Unlock()

	if globalScheduler != nil {
		globalScheduler.Shutdown()
	}
}
