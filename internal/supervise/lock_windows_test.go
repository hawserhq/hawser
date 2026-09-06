//go:build windows

package supervise

import (
	"strings"
	"sync"
	"testing"
)

func TestLockPathNormalizesPathForm(t *testing.T) {
	// #71: the same directory in any spelling must map to one lock, or
	// status lies about a running supervisor and a duplicate can start.
	base := t.TempDir()
	fwd := strings.ReplaceAll(base, `\`, `/`)
	trailing := base + `\`

	want := lockPath(base)
	for _, variant := range []string{fwd, trailing} {
		if got := lockPath(variant); got != want {
			t.Errorf("lockPath(%q) = %s, want %s (same dir, different spelling)", variant, got, want)
		}
	}
	if lockPath(base) == lockPath(base+`2`) {
		t.Error("different directories collided on one lock path")
	}
}

func TestAcquireHeldRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if Held(dir) {
		t.Fatal("Held true before any Acquire")
	}
	l, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !Held(dir) {
		t.Error("Held false while the lock is acquired")
	}
	// The probe spells the path differently and must still see it (#71).
	if !Held(strings.ReplaceAll(dir, `\`, `/`)) {
		t.Error("Held false for the same dir with forward slashes")
	}
	// Case-folding is the filesystem's job on Windows; the lock must agree.
	if !Held(strings.ToUpper(dir)) {
		t.Error("Held false for the same dir upper-cased")
	}
	if _, err := Acquire(dir); err == nil {
		t.Error("second Acquire succeeded; single-instance broken")
	}
	l.Close()
	if Held(dir) {
		t.Error("Held true after release")
	}
	// And the claim is re-acquirable after release.
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("re-Acquire after release: %v", err)
	}
	l2.Close()
}

func TestHeldProbeNeverEvictsAnAcquirer(t *testing.T) {
	// #71 second half: the old mutex probe briefly owned the lock, so a
	// concurrent Acquire could spuriously fail "already running". Hammer
	// probes against repeated Acquire/Release cycles: every Acquire must
	// succeed, because a zero-access probe participates in no sharing checks.
	dir := t.TempDir()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					Held(dir)
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		l, err := Acquire(dir)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Acquire #%d spuriously failed against Held probes: %v", i, err)
		}
		l.Close()
	}
	close(stop)
	wg.Wait()
}
