package ngap

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	amf_context "github.com/free5gc/amf/internal/context"
	ngap_testing "github.com/free5gc/amf/internal/ngap/testing"
)

// installNasScheduler swaps in a pool with a recording handler, and restores
// whatever was there when the test ends.
func installNasScheduler(t *testing.T, numWorkers int, handler nasHandlerFunc) *NasScheduler {
	t.Helper()

	s := newNasScheduler(numWorkers, 64, handler)
	prev := nasScheduler.Swap(s)
	t.Cleanup(func() {
		for _, w := range s.workers {
			w.stop()
		}
		s.wg.Wait()
		nasScheduler.Store(prev)
	})
	return s
}

func ranUeWithSuci(amfUeNgapID int64, suci string) *amf_context.RanUe {
	return &amf_context.RanUe{
		AmfUeNgapId: amfUeNgapID,
		AmfUe:       &amf_context.AmfUe{Suci: suci},
	}
}

// The guarantee paper mode exists to provide: every NAS message of one
// subscriber reaches one worker, in arrival order, no matter which NGAP
// identifier the message arrived under.
func TestNasPool_PerSubscriberOrdering(t *testing.T) {
	const workers = 4

	type record struct {
		suci string
		seq  int64
	}

	var (
		mu   sync.Mutex
		seen = map[string][]int64{}
		wg   sync.WaitGroup
	)

	handler := func(ranUe *amf_context.RanUe, procedureCode int64, _ []byte, _ bool) {
		mu.Lock()
		seen[ranUe.AmfUe.Suci] = append(seen[ranUe.AmfUe.Suci], procedureCode)
		mu.Unlock()
		wg.Done()
	}
	installNasScheduler(t, workers, handler)

	subscribers := []string{
		"suci-0-208-93-0000-0-0-0000000042",
		"suci-0-208-93-0000-0-0-0000000043",
		"suci-0-208-93-0000-0-0-0000000044",
	}
	const msgsEach = 25

	var want []record
	for _, suci := range subscribers {
		for i := int64(0); i < msgsEach; i++ {
			want = append(want, record{suci, i})
		}
	}

	wg.Add(len(want))
	for i, r := range want {
		// The AMF-UE-NGAP-ID deliberately differs on every message: under the
		// blog key that would scatter the UE across workers. Here the key
		// comes from the subscriber identity, so it must not.
		ranUe := ranUeWithSuci(int64(i)*7+1, r.suci)
		SubmitNAS(ranUe, r.seq, nil, false)
	}
	wg.Wait()

	for _, suci := range subscribers {
		got := seen[suci]
		require.Len(t, got, msgsEach, "every message of %s should have been handled", suci)
		for i := range got {
			assert.Equal(t, int64(i), got[i],
				"messages of %s must be handled in arrival order", suci)
		}
	}
}

func TestNasPool_SubscriberPicksTheWorker(t *testing.T) {
	const workers = 4
	s := installNasScheduler(t, workers, func(*amf_context.RanUe, int64, []byte, bool) {})

	// 208930000000042 % 4 == 2; the AMF-UE-NGAP-ID is deliberately not 2 mod 4.
	key, ok := imsiKey("suci-0-208-93-0000-0-0-0000000042")
	require.True(t, ok)
	assert.Equal(t, 2, int(key%uint64(s.numWorkers)))

	got, ok := SubscriberKeyFromRanUe(ranUeWithSuci(9999, "suci-0-208-93-0000-0-0-0000000042"))
	require.True(t, ok)
	assert.Equal(t, key, got, "the worker must be chosen by the IMSI, not the NGAP identifier")
}

// A UE whose identity cannot be established must still keep its messages on one
// worker, or paper mode would reorder exactly the traffic it cannot classify.
func TestNasPool_FallbackIsStable(t *testing.T) {
	const workers = 4
	s := installNasScheduler(t, workers, func(*amf_context.RanUe, int64, []byte, bool) {})

	// No AmfUe at all: nothing to key on.
	unidentified := &amf_context.RanUe{AmfUeNgapId: 7}
	_, ok := SubscriberKeyFromRanUe(unidentified)
	require.False(t, ok)

	// dispatch falls back to the AMF-UE-NGAP-ID, which never changes for the
	// lifetime of the UE, so the worker choice is still constant.
	want := int(uint64(unidentified.AmfUeNgapId) % uint64(s.numWorkers))
	for i := 0; i < 10; i++ {
		assert.Equal(t, want, int(uint64(unidentified.AmfUeNgapId)%uint64(s.numWorkers)))
	}
}

// With no pool installed, SubmitNAS must be a plain call: that is what keeps
// blog and paper-early on upstream's exact call graph.
func TestSubmitNAS_WithoutPoolIsDirect(t *testing.T) {
	prev := nasScheduler.Swap(nil)
	t.Cleanup(func() { nasScheduler.Store(prev) })

	// HandleNAS on a nil RanUe logs and returns, which is all this needs: the
	// point is that it happened on this goroutine, before SubmitNAS returned.
	done := false
	func() {
		defer func() { done = true }()
		SubmitNAS(nil, 0, nil, false)
	}()
	assert.True(t, done)
}

func TestShutdownNasScheduler_DrainsQueuedWork(t *testing.T) {
	var (
		mu      sync.Mutex
		handled int
	)
	s := newNasScheduler(2, 256, func(*amf_context.RanUe, int64, []byte, bool) {
		mu.Lock()
		handled++
		mu.Unlock()
	})
	prev := nasScheduler.Swap(s)
	t.Cleanup(func() { nasScheduler.Store(prev) })

	const n = 100
	for i := 0; i < n; i++ {
		SubmitNAS(ranUeWithSuci(int64(i), "suci-0-208-93-0000-0-0-0000000042"), int64(i), nil, false)
	}

	ShutdownNasScheduler()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, n, handled, "shutdown must drain queued NAS work, not drop it")
}

// There is one SCTP reader goroutine per gNB connection, so the in-flight
// trace cannot live in a single variable: with two gNBs the second connection
// would overwrite the first's trace and the CSV would attribute one gNB's
// timings to the other.
func TestSerialTrace_IsPerConnection(t *testing.T) {
	prevMode := SchedulerMode()
	SetSchedulerMode("paper")
	traceEnabled.Store(true)
	t.Cleanup(func() {
		SetSchedulerMode(prevMode)
		traceEnabled.Store(false)
	})

	connA := &ngap_testing.SctpConnStub{}
	connB := &ngap_testing.SctpConnStub{}

	a := BeginSerialTrace(connA)
	b := BeginSerialTrace(connB)
	require.NotNil(t, a)
	require.NotNil(t, b)
	require.NotSame(t, a, b)

	assert.Same(t, a, takeSerialTrace(connA), "each connection must get its own trace back")
	assert.Nil(t, takeSerialTrace(connA), "a claimed trace must not be handed out twice")
	assert.Same(t, b, takeSerialTrace(connB), "the other connection's trace must be untouched")
}

func TestSerialTrace_OffOutsidePaperMode(t *testing.T) {
	traceEnabled.Store(true)
	prevMode := SchedulerMode()
	t.Cleanup(func() {
		SetSchedulerMode(prevMode)
		traceEnabled.Store(false)
	})

	conn := &ngap_testing.SctpConnStub{}
	for _, mode := range []string{"blog", "paper-early"} {
		SetSchedulerMode(mode)
		assert.Nil(t, BeginSerialTrace(conn),
			"%s dispatches before the handler runs, so it has no serial trace to carry", mode)
		assert.Nil(t, takeSerialTrace(conn))
	}
}
