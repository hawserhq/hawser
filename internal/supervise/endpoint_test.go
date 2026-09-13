package supervise_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/supervise"
)

// These pin the contract `skrog status` leans on (#273): a record written by
// the supervisor comes back verbatim, and anything that is not a usable record
// reads as "nothing to report" rather than as a pipe that might not exist.

func TestEndpointRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := supervise.Endpoint{
		Pipe:   `\.\pipe\skrog_engine`,
		Reason: `\.\pipe\docker_engine is already served by another engine (likely Docker Desktop)`,
	}
	if err := supervise.WriteEndpoint(dir, want); err != nil {
		t.Fatalf("WriteEndpoint: %v", err)
	}
	got, ok := supervise.ReadEndpoint(dir)
	if !ok {
		t.Fatal("ReadEndpoint says there is no record, just after writing one")
	}
	if got != want {
		t.Errorf("ReadEndpoint = %+v, want %+v", got, want)
	}
}

func TestEndpointWriteCreatesTheStateDir(t *testing.T) {
	// The supervisor binds before anything else has necessarily created the
	// directory; a missing one must not cost the record.
	dir := filepath.Join(t.TempDir(), "not", "yet")
	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\x`}); err != nil {
		t.Fatalf("WriteEndpoint into a missing state dir: %v", err)
	}
	if _, ok := supervise.ReadEndpoint(dir); !ok {
		t.Error("no record after writing into a state dir that had to be created")
	}
}

func TestReadEndpointReportsNothingWhenThereIsNothing(t *testing.T) {
	if e, ok := supervise.ReadEndpoint(t.TempDir()); ok {
		t.Errorf("ReadEndpoint invented a record from an empty state dir: %+v", e)
	}
}

func TestReadEndpointRejectsUnusableRecords(t *testing.T) {
	// Both of these would otherwise reach `skrog status` and be printed as
	// fact. An unparseable file is a truncated write or a hand-edit; a record
	// with no pipe names nothing, so it answers nothing.
	for _, tc := range []struct {
		name, body string
	}{
		{"truncated json", `{"pipe":"\\.\pipe\skro`},
		{"not json at all", "docker_engine\n"},
		{"empty file", ""},
		{"no pipe in it", `{"reason":"default pipe is free"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "endpoint.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if e, ok := supervise.ReadEndpoint(dir); ok {
				t.Errorf("ReadEndpoint accepted %s and returned %+v", tc.name, e)
			}
		})
	}
}

func TestClearEndpointRemovesTheRecordAndToleratesAbsence(t *testing.T) {
	dir := t.TempDir()
	// Clearing what was never written is the normal path on a supervisor that
	// failed to bind, so it must not be an error.
	if err := supervise.ClearEndpoint(dir); err != nil {
		t.Errorf("ClearEndpoint on an empty state dir: %v", err)
	}
	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\x`}); err != nil {
		t.Fatal(err)
	}
	if err := supervise.ClearEndpoint(dir); err != nil {
		t.Fatalf("ClearEndpoint: %v", err)
	}
	if e, ok := supervise.ReadEndpoint(dir); ok {
		t.Errorf("record survived ClearEndpoint: %+v", e)
	}
}

func TestWriteEndpointLeavesNoTempFileBehind(t *testing.T) {
	// The write is rename-based so a reader never sees half a record. The
	// temp file is an implementation detail that must not become litter in
	// the state dir a support bundle collects.
	dir := t.TempDir()
	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\x`}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "endpoint.json" {
			t.Errorf("unexpected file left in the state dir: %s", e.Name())
		}
	}
}

// #288: on Windows a reader holding endpoint.json blocks the rename that
// commits a new one, and blocks the delete that clears it. os.Rename is
// MoveFileEx(REPLACE_EXISTING) and os.ReadFile opens without FILE_SHARE_DELETE.
// A single attempt could lose the record for the whole life of a supervisor.
func TestWriteEndpointSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\first`}); err != nil {
		t.Fatal(err)
	}

	// Hold the file open the way a reader does, then release it while the
	// write is retrying.
	f, err := os.Open(filepath.Join(dir, "endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(60 * time.Millisecond)
		f.Close()
		close(released)
	}()

	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\second`}); err != nil {
		t.Fatalf("WriteEndpoint gave up while a reader held the file: %v", err)
	}
	<-released

	got, ok := supervise.ReadEndpoint(dir)
	if !ok || got.Pipe != `\.\pipe\second` {
		t.Errorf("record = %+v (ok=%v), want the second pipe", got, ok)
	}
}

func TestClearEndpointSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\x`}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		f.Close()
	}()

	if err := supervise.ClearEndpoint(dir); err != nil {
		t.Fatalf("ClearEndpoint gave up while a reader held the file: %v", err)
	}
	if _, ok := supervise.ReadEndpoint(dir); ok {
		t.Error("record survived ClearEndpoint")
	}
}

// A commit that never lands must not leave the temp file behind, where a
// support bundle would collect it looking like a record.
func TestWriteEndpointLeavesNoTempFileWhenItGivesUp(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\x`}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() // held for the whole attempt, so every retry fails

	if err := supervise.WriteEndpoint(dir, supervise.Endpoint{Pipe: `\.\pipe\y`}); err == nil {
		t.Skip("this platform allows replacing an open file; nothing to assert")
	}
	if _, err := os.Stat(filepath.Join(dir, "endpoint.json.tmp")); err == nil {
		t.Error("endpoint.json.tmp left behind after the commit gave up")
	}
}
