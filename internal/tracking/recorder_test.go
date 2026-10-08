package tracking

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderCreatesPerAircraftJSONLog(t *testing.T) {
	root := t.TempDir()
	recorder, err := NewRecorder(root)
	if err != nil {
		t.Fatal(err)
	}
	received := time.Date(2026, 10, 8, 18, 30, 0, 0, time.UTC)
	entry := Entry{ReceivedAt: received, ICAO: "4cafd6", RawFrame: "8D4CAFD6", PositionDecision: "accepted"}
	if err := recorder.Record(entry); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "4CAFD6", "2026-10-08.jsonl")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var decoded Entry
	if err := json.NewDecoder(bufio.NewReader(file)).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ICAO != entry.ICAO || decoded.RawFrame != entry.RawFrame {
		t.Fatalf("unexpected entry: %+v", decoded)
	}
}

func TestRecorderRejectsUnsafeICAOFolder(t *testing.T) {
	recorder, err := NewRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	if err := recorder.Record(Entry{ReceivedAt: time.Now(), ICAO: "../BAD"}); err == nil {
		t.Fatal("expected invalid ICAO address to be rejected")
	}
}
