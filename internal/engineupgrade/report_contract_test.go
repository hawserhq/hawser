package engineupgrade_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/engineupgrade"
)

// The report has to let the caller tell three outcomes apart, because the
// caller writes the install manifest from them and the manifest must describe
// the engine that is actually in the distro (#241):
//
//	swapped + rolled back   -> the previous engine is installed
//	swapped + NOT rolled back -> the TARGET's binaries are installed
//	not swapped             -> nothing changed
//
// The middle one is the case that was mishandled: `skrog engine rollback` read
// a PreviousEngineRef that had never been updated, so it went two versions
// down or reported nothing to roll back to.
func TestReportDistinguishesAFailedHealFromASuccessfulOne(t *testing.T) {
	dir := t.TempDir()
	src := makeRootfs(t, dir, map[string]string{"dockerd": "x"})

	newRunner := func(restore func(context.Context, string) error) *engineupgrade.Runner {
		return &engineupgrade.Runner{
			WSL:     &fakeDistro{},
			Fetcher: &fakeFetcher{tarball: src},
			Stop:    func(context.Context) error { return nil },
			Start:   func(context.Context) error { return errors.New("boom") },
			Healthy: func(context.Context) bool { return false },
			Restore: restore,
		}
	}

	t.Run("heal succeeded", func(t *testing.T) {
		rep, err := newRunner(func(context.Context, string) error { return nil }).
			Run(context.Background(), opts(t, dir))
		if err == nil {
			t.Fatal("expected the upgrade to fail")
		}
		if !rep.RolledBack {
			t.Error("RolledBack is false after a successful restore; the caller would record the target as installed")
		}
	})

	t.Run("heal failed", func(t *testing.T) {
		rep, err := newRunner(func(context.Context, string) error { return errors.New("restore boom") }).
			Run(context.Background(), opts(t, dir))
		if err == nil {
			t.Fatal("expected the upgrade to fail")
		}
		if rep.RolledBack {
			t.Error("RolledBack is true even though the restore failed")
		}
		if len(rep.Replaced) == 0 {
			t.Error("Replaced is empty, so the caller cannot tell that the target's binaries are in the distro")
		}
	})
}

// Step 4 is labelled "Confirm the swap took, rather than trusting it". It used
// to drop the probe's error and compare against nothing, producing the step
// "engine is running " with a trailing space and a success report (#241).
func TestVersionProbeFailureIsReportedRatherThanBlank(t *testing.T) {
	dir := t.TempDir()
	src := makeRootfs(t, dir, map[string]string{"dockerd": "x"})
	r := &engineupgrade.Runner{
		WSL:     &fakeDistro{versionErr: errors.New("dockerd: not found")},
		Fetcher: &fakeFetcher{tarball: src},
		Stop:    func(context.Context) error { return nil },
		Start:   func(context.Context) error { return nil },
		Healthy: func(context.Context) bool { return true },
		Restore: func(context.Context, string) error { return nil },
	}
	rep, err := r.Run(context.Background(), opts(t, dir))
	if err != nil {
		t.Fatalf("a version probe failure must not fail the upgrade: %v", err)
	}
	joined := strings.Join(rep.Steps, "\n")
	if strings.Contains(joined, "engine is running \n") || strings.HasSuffix(joined, "engine is running ") {
		t.Errorf("the step reports a blank version:\n%s", joined)
	}
	if !strings.Contains(joined, "could not be read") {
		t.Errorf("a failed version probe is not reported:\n%s", joined)
	}
}
