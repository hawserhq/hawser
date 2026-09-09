package prune

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"0B": 0, "512B": 512, "1kB": 1000, "1.5MB": 1_500_000, "1.234GB": 1_234_000_000,
		"2TB": 2_000_000_000_000, "1KiB": 1024, "2GiB": 2 << 30, " 3 MB ": 3_000_000, "7": 7,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "GB", "1.2.3GB", "5 parsecs"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}

func TestParseReclaimed(t *testing.T) {
	system := "Deleted Containers:\nabc\n\nDeleted Images:\nuntagged: x\n\nTotal reclaimed space: 1.234GB"
	if got := ParseReclaimed(system); got != 1_234_000_000 {
		t.Errorf("system prune = %d", got)
	}
	builder := "ID\tRECLAIMABLE\tSIZE\nabc\ttrue\t512MB\nTotal:\t512MB"
	if got := ParseReclaimed(builder); got != 512_000_000 {
		t.Errorf("builder prune = %d", got)
	}
	if got := ParseReclaimed("nothing to do"); got != 0 {
		t.Errorf("no total line = %d, want 0", got)
	}
}

func TestPlan(t *testing.T) {
	steps := Plan(Options{})
	if len(steps) != 2 || steps[0].Name != "containers" || steps[1].Name != "images" {
		t.Fatalf("default plan = %+v", steps)
	}
	if strings.Join(steps[1].Args, " ") != "image prune -f" {
		t.Errorf("default image prune must be dangling-only: %v", steps[1].Args)
	}

	steps = Plan(Options{All: true, Until: 168 * time.Hour, BuildCache: true, Volumes: true})
	names := []string{}
	for _, s := range steps {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "containers,images,volumes,build-cache" {
		t.Errorf("full plan order = %v", names)
	}
	img := strings.Join(steps[1].Args, " ")
	if !strings.Contains(img, " -a") || !strings.Contains(img, "until=168h") {
		t.Errorf("image step should carry -a and the until filter: %s", img)
	}
	if strings.Contains(strings.Join(steps[2].Args, " "), "until") {
		t.Error("volume prune has no until filter")
	}
}

func TestDurationArg(t *testing.T) {
	cases := map[time.Duration]string{168 * time.Hour: "168h", 90 * time.Minute: "90m", 90 * time.Second: "1m30s"}
	for d, want := range cases {
		if got := durationArg(d); got != want {
			t.Errorf("durationArg(%v) = %q, want %q", d, got, want)
		}
	}
}

// fakeRunner replays outputs keyed by the first two args ("image prune").
type fakeRunner struct {
	replies map[string]string
	fail    map[string]bool
	calls   []string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	key := args[0] + " " + args[1]
	f.calls = append(f.calls, key)
	if f.fail[key] {
		return "Error response from daemon: boom", errors.New("boom")
	}
	return f.replies[key], nil
}

func TestRunSumsAndKeepsGoingOnFailure(t *testing.T) {
	r := &fakeRunner{
		replies: map[string]string{
			"container prune": "Total reclaimed space: 100MB",
			"image prune":     "Total reclaimed space: 1GB",
			"builder prune":   "Total:\t50MB",
		},
		fail: map[string]bool{"volume prune": true},
	}
	res := Run(context.Background(), r, Options{BuildCache: true, Volumes: true})

	if len(res.Steps) != 4 {
		t.Fatalf("steps = %d", len(res.Steps))
	}
	if res.Failed != 1 || res.Steps[2].Err == "" {
		t.Errorf("volume failure not recorded: %+v", res.Steps[2])
	}
	// The failure did not stop build-cache, and the total excludes the failed step.
	if res.Steps[3].ReclaimedBytes != 50_000_000 {
		t.Errorf("build-cache step after a failure = %+v", res.Steps[3])
	}
	if res.ReclaimedBytes != 1_150_000_000 {
		t.Errorf("total reclaimed = %d", res.ReclaimedBytes)
	}
}
