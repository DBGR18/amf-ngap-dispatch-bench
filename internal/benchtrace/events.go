// Package benchtrace writes optional AMF protocol action events.
package benchtrace

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

var state struct {
	sync.Mutex
	enabled        atomic.Bool
	file           *os.File
	writer         *csv.Writer
	path           string
	runID          string
	rows           uint64
	err            error
	connections    sync.Map
	nextConnection atomic.Uint64
}

func Enabled() bool { return state.enabled.Load() }

func Init() error {
	path := os.Getenv("AMF_BENCH_EVENT_TRACE")
	if path == "" {
		return nil
	}
	runID := os.Getenv("RUN_ID")
	if runID == "" {
		return fmt.Errorf("RUN_ID required for AMF event trace")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	state.Lock()
	defer state.Unlock()
	state.file = f
	state.path = path
	state.runID = runID
	state.writer = csv.NewWriter(f)
	if err = state.writer.Write([]string{"schema_version", "run_id", "event", "connection_id", "gnb_id", "ran_ue_ngap_id", "amf_ue_ngap_id", "supi_hash", "procedure_code", "clock_monotonic_ns", "wall_unix_ns"}); err != nil {
		f.Close()
		return err
	}
	state.writer.Flush()
	if err = state.writer.Error(); err != nil {
		f.Close()
		return err
	}
	state.enabled.Store(true)
	return nil
}

func ConnectionID(conn net.Conn) string {
	if !Enabled() || conn == nil {
		return ""
	}
	if id, ok := state.connections.Load(conn); ok {
		return id.(string)
	}
	id := strconv.FormatUint(state.nextConnection.Add(1), 10)
	actual, _ := state.connections.LoadOrStore(conn, id)
	return actual.(string)
}

func HashSUPI(supi string) string {
	if supi == "" {
		return ""
	}
	supi = strings.TrimPrefix(supi, "imsi-")
	hash := sha256.Sum256([]byte("supi-" + supi))
	return hex.EncodeToString(hash[:])
}

func ClockMonotonicNS() int64 {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
		return 0
	}
	return ts.Nano()
}

func Record(event, connectionID, gnbID, ranUEID, amfUEID, supiHash, procedureCode string, monoNS int64) {
	if !Enabled() {
		return
	}
	if monoNS == 0 {
		monoNS = ClockMonotonicNS()
	}
	wall := time.Now().UnixNano()
	state.Lock()
	defer state.Unlock()
	if state.err != nil {
		return
	}
	state.err = state.writer.Write([]string{"1", state.runID, event, connectionID, gnbID, ranUEID, amfUEID, supiHash, procedureCode, strconv.FormatInt(monoNS, 10), strconv.FormatInt(wall, 10)})
	if state.err == nil {
		state.rows++
	}
}

func Stop() error {
	if !state.enabled.Swap(false) {
		return nil
	}
	state.Lock()
	defer state.Unlock()
	state.writer.Flush()
	if state.err == nil {
		state.err = state.writer.Error()
	}
	if err := state.file.Close(); state.err == nil {
		state.err = err
	}
	summary := map[string]any{"schema_version": 1, "run_id": state.runID, "rows_written": state.rows, "rows_dropped": 0, "clean_shutdown": state.err == nil}
	if state.err != nil {
		summary["error"] = state.err.Error()
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	err := os.WriteFile(filepath.Join(filepath.Dir(state.path), "amf_event_trace_summary.json"), append(data, '\n'), 0640)
	if state.err != nil {
		return state.err
	}
	return err
}
