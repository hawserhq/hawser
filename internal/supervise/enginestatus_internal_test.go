package supervise

import "testing"

// In-package: lastUp is the reconciler's own field, and the point of the test
// is that EngineStatus reports what the last tick observed.
func TestEngineStatusPublishesWhatTheReconcilerSaw(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{Config: Config{StateDir: dir}}

	if got := s.EngineStatus(); got != "stopped" {
		t.Errorf("before any tick: %q, want stopped", got)
	}

	s.lastUp.Store(true)
	if got := s.EngineStatus(); got != "running" {
		t.Errorf("after seeing the engine up: %q, want running", got)
	}

	// Down, with the engine-state file saying we put it down deliberately:
	// that is "idle", not "stopped" — the distinction the dot exists to show.
	s.lastUp.Store(false)
	if err := WriteEngineState(dir, EngineIdle); err != nil {
		t.Fatal(err)
	}
	if got := s.EngineStatus(); got != "idle" {
		t.Errorf("idle-stopped engine: %q, want idle", got)
	}
}
