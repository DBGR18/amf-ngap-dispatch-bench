package benchtrace

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
)

func TestDisabled(t *testing.T) {
	t.Setenv("AMF_BENCH_EVENT_TRACE", "")
	if err := Init(); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Fatal("enabled without path")
	}
}

func TestEventsAndFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amf_events.csv")
	t.Setenv("AMF_BENCH_EVENT_TRACE", path)
	t.Setenv("RUN_ID", "test")
	if err := Init(); err != nil {
		t.Fatal(err)
	}
	Record("authentication_initiated", "1", "314", "1", "5", HashSUPI("imsi-208930000000001"), "", 0)
	if err := Stop(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][2] != "authentication_initiated" || rows[1][9] == "0" {
		t.Fatalf("unexpected rows: %v", rows)
	}
	if HashSUPI("imsi-208930000000001") != HashSUPI("208930000000001") {
		t.Fatal("identity hash mismatch")
	}
}
