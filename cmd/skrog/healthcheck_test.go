package main

import "testing"

// readiness is the whole contract of `skrog healthcheck`: pin every case.
func TestReadiness(t *testing.T) {
	cases := []struct {
		name       string
		supervisor bool
		engine     string
		wantReady  bool
	}{
		{"running engine, supervisor up", true, "running", true},
		{"idle engine wakes on demand", true, "idle", true},
		{"engine stopped", true, "stopped", false},
		{"no supervisor, engine running", false, "running", false},
		{"no supervisor, engine idle", false, "idle", false},
		{"nothing up", false, "stopped", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ready, reason := readiness(tc.supervisor, tc.engine)
			if ready != tc.wantReady {
				t.Fatalf("ready = %v, want %v", ready, tc.wantReady)
			}
			if reason == "" {
				t.Error("reason must always explain the verdict")
			}
		})
	}
}
