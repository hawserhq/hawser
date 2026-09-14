package supervise_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/supervise"
)

// #314: every writer in this package commits with tmp-then-rename, and on
// Windows a rename cannot replace a file another process has open. Only
// endpoint.json had a retry; the rest lost writes to a race that CI eventually
// hit -- `skrog stop` failing with "Access is denied" while something read the
// desired state.
//
// These drive the exported writers rather than the unexported commit(), because
// the bug was never in the helper: it was in which callers had one.

// holdBriefly opens path the way a reader does and releases it after d, so a
// writer that retries succeeds and a writer that does not, fails.
func holdBriefly(t *testing.T, path string, d time.Duration) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	go func() {
		time.Sleep(d)
		f.Close()
	}()
}

func TestWriteDesiredSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteDesired(dir, supervise.DesiredRunning); err != nil {
		t.Fatal(err)
	}
	holdBriefly(t, filepath.Join(dir, "desired-state"), 60*time.Millisecond)

	// This is `skrog stop`. It must not fail because status happened to be
	// reading: the recorded intent is what stops the health loop restarting
	// the engine the user just stopped.
	if err := supervise.WriteDesired(dir, supervise.DesiredStopped); err != nil {
		t.Fatalf("WriteDesired gave up while a reader held the file: %v", err)
	}
	if got := supervise.ReadDesired(dir); got != supervise.DesiredStopped {
		t.Errorf("ReadDesired = %q, want stopped", got)
	}
}

func TestWriteEngineStateSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteEngineState(dir, supervise.EngineIdle); err != nil {
		t.Fatal(err)
	}
	holdBriefly(t, filepath.Join(dir, "engine-state"), 60*time.Millisecond)

	if err := supervise.WriteEngineState(dir, supervise.EngineIdle); err != nil {
		t.Fatalf("WriteEngineState gave up while a reader held the file: %v", err)
	}
	if got := supervise.ReadEngineState(dir); got != supervise.EngineIdle {
		t.Errorf("ReadEngineState = %q, want idle", got)
	}
}

// Writing active DELETES the file, and that delete is the supervisor's wake-up
// poke. Losing it leaves an idle engine nothing wakes.
func TestClearingEngineStateSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteEngineState(dir, supervise.EngineIdle); err != nil {
		t.Fatal(err)
	}
	holdBriefly(t, filepath.Join(dir, "engine-state"), 60*time.Millisecond)

	if err := supervise.WriteEngineState(dir, supervise.EngineActive); err != nil {
		t.Fatalf("clearing the idle marker gave up while a reader held it: %v", err)
	}
	if got := supervise.ReadEngineState(dir); got != supervise.EngineActive {
		t.Errorf("ReadEngineState = %q, want active", got)
	}
}

// The supervisor polls the restart request every tick, so this delete races a
// reader more often than any other in the package. A request that survives
// being cleared shuts the next supervisor down on sight.
func TestClearRestartSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.RequestRestart(dir); err != nil {
		t.Fatal(err)
	}
	holdBriefly(t, filepath.Join(dir, "restart-request"), 60*time.Millisecond)

	if err := supervise.ClearRestart(dir); err != nil {
		t.Fatalf("ClearRestart gave up while a reader held the file: %v", err)
	}
	if supervise.RestartRequested(dir) {
		t.Error("the restart request survived ClearRestart")
	}
}

func TestWriteStatsSurvivesAConcurrentReader(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteStats(dir, supervise.Stats{}); err != nil {
		t.Fatal(err)
	}
	holdBriefly(t, filepath.Join(dir, "supervisor-stats.json"), 60*time.Millisecond)

	if err := supervise.WriteStats(dir, supervise.Stats{}); err != nil {
		t.Fatalf("WriteStats gave up while a reader held the file: %v", err)
	}
	if _, ok, err := supervise.ReadStats(dir); err != nil || !ok {
		t.Errorf("ReadStats after the retry: ok=%v err=%v", ok, err)
	}
}

// A commit that never lands must not leave its temp file in the state
// directory, where a support bundle collects it and it looks like state.
func TestAFailedCommitLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := supervise.WriteDesired(dir, supervise.DesiredRunning); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "desired-state"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() // held for the whole attempt, so every retry fails

	if err := supervise.WriteDesired(dir, supervise.DesiredStopped); err == nil {
		t.Skip("this platform replaced a file held open; nothing to assert")
	}
	if _, err := os.Stat(filepath.Join(dir, "desired-state.tmp")); err == nil {
		t.Error("desired-state.tmp was left behind after the commit gave up")
	}
}
