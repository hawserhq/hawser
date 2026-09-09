package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		method, path, query string
		wantAction          string
		wantImage           string
		wantName            string
		wantContainer       string
	}{
		{"POST", "/v1.44/images/create", "fromImage=nginx&tag=latest", "image-pull", "nginx:latest", "", ""},
		{"POST", "/images/create", "fromImage=alpine", "image-pull", "alpine", "", ""},
		{"POST", "/v1.44/build", "t=myapp", "image-build", "", "", ""},
		{"POST", "/v1.44/images/registry.io/team/app/push", "", "image-push", "registry.io/team/app", "", ""},
		{"POST", "/v1.44/containers/create", "name=web", "container-create", "", "web", ""},
		{"POST", "/v1.44/containers/abc123/start", "", "container-start", "", "", "abc123"},
		{"POST", "/containers/abc123/stop", "t=10", "container-stop", "", "", "abc123"},
		{"POST", "/v1.44/containers/abc123/exec", "", "exec-create", "", "", "abc123"},
		{"POST", "/v1.44/exec/def456/start", "", "exec-start", "", "", ""},
		{"POST", "/v1.44/volumes/create", "", "volume-create", "", "", ""},
		{"DELETE", "/v1.44/containers/abc123", "force=1", "container-remove", "", "", "abc123"},
		// Not container-affecting: dropped.
		{"GET", "/v1.44/containers/json", "all=1", "", "", "", ""},
		{"GET", "/_ping", "", "", "", "", ""},
		{"GET", "/v1.44/version", "", "", "", "", ""},
		{"POST", "/v1.44/containers/abc123/wait", "", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			action, ev := Classify(tc.method, tc.path, tc.query)
			if action != tc.wantAction {
				t.Fatalf("action = %q, want %q", action, tc.wantAction)
			}
			if ev.Image != tc.wantImage || ev.Name != tc.wantName || ev.Container != tc.wantContainer {
				t.Fatalf("fields = image %q name %q container %q; want %q/%q/%q",
					ev.Image, ev.Name, ev.Container, tc.wantImage, tc.wantName, tc.wantContainer)
			}
		})
	}
}

func TestObserveWritesOneLinePerAffectingCall(t *testing.T) {
	var buf bytes.Buffer
	fixed := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l := New(&buf)
	l.now = func() time.Time { return fixed }

	start := fixed.Add(-812 * time.Millisecond)
	l.Observe(start, "POST", "/v1.44/images/create", "fromImage=nginx&tag=latest", 200, nil)
	l.Observe(start, "GET", "/v1.44/containers/json", "", 200, nil) // dropped

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one recorded line, got %d:\n%s", len(lines), buf.String())
	}
	var ev Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Action != "image-pull" || ev.Image != "nginx:latest" || ev.Status != 200 || ev.Millis != 812 {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Time != "2026-09-09T12:00:00.000Z" {
		t.Fatalf("time = %q", ev.Time)
	}
}

func TestObserveRecordsError(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Observe(time.Now(), "POST", "/v1.44/containers/create", "name=web", 400,
		errBind{})
	if !strings.Contains(buf.String(), `"error"`) || !strings.Contains(buf.String(), "bad bind") {
		t.Fatalf("error not recorded: %s", buf.String())
	}
}

type errBind struct{}

func (errBind) Error() string { return "bad bind" }
