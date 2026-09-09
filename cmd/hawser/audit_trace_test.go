package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zcsizmadia/hawser/internal/audit"
)

func TestSummarizeTraceGroupsAndDeduplicates(t *testing.T) {
	events := []audit.Event{
		{Action: "image-pull", Image: "alpine:3.20"},
		{Action: "container-create", Image: "alpine:3.20", Name: "web"},
		{Action: "container-start", Container: "web"},
		{Action: "exec-create", Container: "web"},
		{Action: "exec-start", Container: "web"},
		{Action: "image-build"},
		{Action: "image-pull", Image: "postgres:16"},
		{Action: "container-create", Image: "postgres:16", Name: "db"},
	}
	s := summarizeTrace([]string{"act", "-j", "build"}, 7, 1500*time.Millisecond, events)

	if s.ExitCode != 7 || s.Millis != 1500 || s.Events != len(events) {
		t.Errorf("passthrough fields wrong: %+v", s)
	}
	want := map[string]int{
		"image-pull": 2, "container-create": 2, "container-start": 1,
		"exec-create": 1, "exec-start": 1, "image-build": 1,
	}
	for k, n := range want {
		if s.Actions[k] != n {
			t.Errorf("actions[%s] = %d, want %d", k, s.Actions[k], n)
		}
	}
	// Images deduplicated (alpine appears in a pull and a create) and sorted.
	if len(s.Images) != 2 || s.Images[0] != "alpine:3.20" || s.Images[1] != "postgres:16" {
		t.Errorf("images = %v", s.Images)
	}
	// Containers: names from create; the exec/start on "web" must not duplicate it.
	if len(s.Containers) != 2 || s.Containers[0] != "db" || s.Containers[1] != "web" {
		t.Errorf("containers = %v", s.Containers)
	}
}

func TestSummarizeTraceEmptyIsArraysNotNil(t *testing.T) {
	s := summarizeTrace([]string{"true"}, 0, 0, nil)
	if s.Images == nil || s.Containers == nil || s.Actions == nil {
		t.Fatalf("empty summary must have non-nil collections: %+v", s)
	}
	if s.Events != 0 {
		t.Errorf("events = %d", s.Events)
	}
}

func TestEventsSinceReadsOnlyAppendedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	before := `{"time":"t","action":"image-pull","method":"POST","path":"/p","image":"old"}` + "\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	offset := logSize(path)

	after := `{"time":"t","action":"container-create","method":"POST","path":"/c","name":"web"}` + "\n" +
		`not json at all` + "\n" +
		`{"time":"t","action":"exec-start","method":"POST","path":"/e","container":"web"}` + "\n"
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(after)
	f.Close()

	events, note, err := eventsSince(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Errorf("unexpected note %q", note)
	}
	// The pre-existing record is excluded; the garbage line is skipped.
	if len(events) != 2 || events[0].Action != "container-create" || events[1].Action != "exec-start" {
		t.Errorf("events = %+v", events)
	}
}

func TestEventsSinceHandlesRotationAndMissingLog(t *testing.T) {
	// Rotated mid-run: the file is now smaller than the recorded offset, so the
	// whole current file is used and the note says so.
	path := filepath.Join(t.TempDir(), "audit.log")
	os.WriteFile(path, []byte(`{"action":"image-pull"}`+"\n"), 0o644)
	events, note, err := eventsSince(path, 10_000)
	if err != nil || len(events) != 1 || note == "" {
		t.Errorf("rotation fallback: events=%d note=%q err=%v", len(events), note, err)
	}

	// No log at all: nothing recorded, with an explanatory note, not an error.
	events, note, err = eventsSince(filepath.Join(t.TempDir(), "missing.log"), 0)
	if err != nil || len(events) != 0 || note == "" {
		t.Errorf("missing log: events=%d note=%q err=%v", len(events), note, err)
	}
}
