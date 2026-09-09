package prewarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseList(t *testing.T) {
	in := `
# pinned for the build fleet
mcr.microsoft.com/devcontainers/base:ubuntu   # dev containers
node:20@sha256:0000000000000000000000000000000000000000000000000000000000000000

alpine:3.20
alpine:3.20                                  # duplicate, dropped
`
	refs, err := ParseList(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"mcr.microsoft.com/devcontainers/base:ubuntu",
		"node:20@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"alpine:3.20",
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("refs[%d] = %q, want %q", i, refs[i], want[i])
		}
	}
}

func TestParseListRejectsMalformedLines(t *testing.T) {
	for _, in := range []string{"alpine latest\n", "--rm alpine\n"} {
		if _, err := ParseList(strings.NewReader(in)); err == nil {
			t.Errorf("ParseList(%q) should fail", in)
		}
	}
	// Empty input is an empty list, not an error; the CLI decides what that means.
	if refs, err := ParseList(strings.NewReader("# nothing\n\n")); err != nil || len(refs) != 0 {
		t.Errorf("empty list = %v, %v", refs, err)
	}
}

// fakePuller records the maximum number of concurrent pulls and fails refs
// containing "bad".
type fakePuller struct {
	mu       sync.Mutex
	inflight int32
	maxSeen  int32
}

func (f *fakePuller) Pull(ctx context.Context, ref string) error {
	n := atomic.AddInt32(&f.inflight, 1)
	f.mu.Lock()
	if n > f.maxSeen {
		f.maxSeen = n
	}
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt32(&f.inflight, -1)
	if strings.Contains(ref, "bad") {
		return errors.New("manifest unknown")
	}
	return nil
}

func TestRunBoundsConcurrencyAndKeepsOrder(t *testing.T) {
	refs := []string{"a", "bad-b", "c", "d", "bad-e", "f"}
	p := &fakePuller{}
	res := Run(context.Background(), refs, 2, p)

	if p.maxSeen > 2 {
		t.Errorf("concurrency bound violated: %d in flight", p.maxSeen)
	}
	if len(res.Images) != len(refs) {
		t.Fatalf("got %d results", len(res.Images))
	}
	for i, img := range res.Images {
		if img.Ref != refs[i] {
			t.Errorf("results out of order: [%d] = %q, want %q", i, img.Ref, refs[i])
		}
		wantOK := !strings.Contains(refs[i], "bad")
		if img.OK != wantOK {
			t.Errorf("%s ok = %v, want %v", img.Ref, img.OK, wantOK)
		}
		if !img.OK && img.Err == "" {
			t.Errorf("%s failed without an error message", img.Ref)
		}
	}
	// A failure never stops the others: every ref was attempted.
	if res.Pulled != 4 || res.Failed != 2 {
		t.Errorf("pulled/failed = %d/%d, want 4/2", res.Pulled, res.Failed)
	}
	if res.Millis <= 0 {
		t.Error("total time should be recorded")
	}
}

func TestRunDefaultsConcurrency(t *testing.T) {
	p := &fakePuller{}
	Run(context.Background(), []string{"a", "b", "c", "d", "e"}, 0, p)
	if p.maxSeen > 3 {
		t.Errorf("default concurrency should be 3, saw %d in flight", p.maxSeen)
	}
}
