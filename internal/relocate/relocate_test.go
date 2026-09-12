package relocate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/wsl"
)

// fakeWSL implements wsl.WSL. Export writes a stand-in tarball and Import
// creates the disk WSL would create, so the checksum and the post-import
// verification both run for real.
type fakeWSL struct {
	imported, unregistered bool
	importErr              error
	// importSkipsDisk makes Import "succeed" without producing a disk, the
	// shape that must still be caught as an orphan.
	importSkipsDisk bool
}

func (f *fakeWSL) Status(context.Context) (wsl.Status, error) { return wsl.Status{}, nil }
func (f *fakeWSL) Export(_ context.Context, _, path string) error {
	return os.WriteFile(path, []byte("fake distro filesystem tarball"), 0o644)
}

func (f *fakeWSL) Import(_ context.Context, _, installDir, _ string) error {
	f.imported = true
	if f.importErr != nil {
		return f.importErr
	}
	if f.importSkipsDisk {
		return nil
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(installDir, DiskName), []byte("disk"), 0o644)
}

func (f *fakeWSL) Unregister(context.Context, string) error { f.unregistered = true; return nil }
func (f *fakeWSL) Terminate(context.Context, string) error  { return nil }
func (f *fakeWSL) List(context.Context) ([]wsl.Distro, error) {
	return nil, nil
}
func (f *fakeWSL) Exec(context.Context, string, string, ...string) (string, error) { return "", nil }
func (f *fakeWSL) Start(context.Context, string, string, ...string) (func(), error) {
	return func() {}, nil
}

// fakeVolume reports whatever the test needs.
type fakeVolume struct {
	free, size uint64
	freeErr    error
}

func (v fakeVolume) Free(string) (uint64, error) {
	return v.free, v.freeErr
}
func (v fakeVolume) SizeOnDisk(string) (uint64, error) { return v.size, nil }

// harness builds a runner over temp dirs with a populated source data dir.
type harness struct {
	r          *Runner
	w          *fakeWSL
	from, to   string
	stopped    bool
	started    bool
	committed  string
	commitFail error
}

func newHarness(t *testing.T, vol fakeVolume) *harness {
	t.Helper()
	h := &harness{
		w:    &fakeWSL{},
		from: filepath.Join(t.TempDir(), "distro"),
		to:   filepath.Join(t.TempDir(), "engine"),
	}
	if err := os.MkdirAll(h.from, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.from, DiskName), []byte("old disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.r = &Runner{
		WSL:    h.w,
		Volume: vol,
		Stop:   func(context.Context) error { h.stopped = true; return nil },
		Start:  func(context.Context) error { h.started = true; return nil },
		Commit: func(dir string) error { h.committed = dir; return h.commitFail },
	}
	return h
}

func (h *harness) opts() Options {
	return Options{Distro: "skrog-engine", From: h.from, To: h.to}
}

// roomy is a volume with plenty of space for a small disk.
var roomy = fakeVolume{free: 1 << 30, size: 1 << 20}

func TestPlanRejectsBadTargets(t *testing.T) {
	h := newHarness(t, roomy)

	t.Run("empty", func(t *testing.T) {
		o := h.opts()
		o.To = ""
		if _, err := h.r.Plan(o); err == nil {
			t.Fatal("an empty target must be refused")
		}
	})

	t.Run("relative", func(t *testing.T) {
		o := h.opts()
		o.To = filepath.Join("relative", "path")
		if _, err := h.r.Plan(o); err == nil {
			t.Fatal("a relative target must be refused")
		}
	})

	t.Run("same dir", func(t *testing.T) {
		o := h.opts()
		o.To = h.from
		var want *ErrSameDir
		if _, err := h.r.Plan(o); !errors.As(err, &want) {
			t.Fatalf("got %v, want ErrSameDir", err)
		}
	})

	t.Run("same dir differing only in case", func(t *testing.T) {
		o := h.opts()
		o.To = strings.ToUpper(h.from)
		var want *ErrSameDir
		if _, err := h.r.Plan(o); !errors.As(err, &want) {
			t.Fatalf("got %v, want ErrSameDir — Windows paths are case-insensitive", err)
		}
	})

	t.Run("nested inside the source", func(t *testing.T) {
		o := h.opts()
		o.To = filepath.Join(h.from, "inner")
		var want *ErrNested
		if _, err := h.r.Plan(o); !errors.As(err, &want) {
			t.Fatalf("got %v, want ErrNested — unregister would delete the target", err)
		}
	})

	t.Run("target already holds a disk", func(t *testing.T) {
		o := h.opts()
		if err := os.MkdirAll(o.To, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o.To, DiskName), []byte("someone else"), 0o644); err != nil {
			t.Fatal(err)
		}
		var want *ErrTargetNotEmpty
		if _, err := h.r.Plan(o); !errors.As(err, &want) {
			t.Fatalf("got %v, want ErrTargetNotEmpty", err)
		}
	})
}

func TestPlanRefusesWhenSpaceIsShort(t *testing.T) {
	// 10 MB disk needs ~20 MB at peak; offer 15 MB.
	h := newHarness(t, fakeVolume{free: 15 << 20, size: 10 << 20})
	var short *ErrNotEnoughSpace
	rep, err := h.r.Plan(h.opts())
	if !errors.As(err, &short) {
		t.Fatalf("got %v, want ErrNotEnoughSpace", err)
	}
	if short.Need != 20<<20 {
		t.Errorf("Need = %d, want twice the disk size (archive + imported disk)", short.Need)
	}
	if rep.FreeBytes != 15<<20 {
		t.Errorf("the report should carry the free figure, got %d", rep.FreeBytes)
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	h := newHarness(t, roomy)
	o := h.opts()
	o.DryRun = true
	rep, err := h.r.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if h.stopped || h.w.imported || h.w.unregistered {
		t.Error("a dry run must not stop the engine or touch the distro")
	}
	if len(rep.Steps) == 0 {
		t.Error("a dry run owes the user the plan it would carry out")
	}
	if _, err := os.Stat(filepath.Join(h.from, DiskName)); err != nil {
		t.Error("the original disk must still be there")
	}
}

func TestRunMovesTheEngineAndRecordsIt(t *testing.T) {
	h := newHarness(t, roomy)
	o := h.opts()
	o.Restart = true

	rep, err := h.r.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if !h.stopped {
		t.Error("the engine must be stopped before the export")
	}
	if !h.w.unregistered || !h.w.imported {
		t.Error("the distro must be unregistered and re-imported")
	}
	if h.committed != rep.To {
		t.Errorf("manifest recorded %q, want %q", h.committed, rep.To)
	}
	if !h.started || !rep.Restarted {
		t.Error("--restart must bring the engine back")
	}
	if _, err := os.Stat(filepath.Join(h.to, DiskName)); err != nil {
		t.Errorf("no disk at the new location: %v", err)
	}
	if rep.SHA256 == "" || rep.MovedBytes == 0 {
		t.Error("the report should carry the archive checksum and size")
	}
	// The staging directory and its archive are transient.
	if _, err := os.Stat(filepath.Join(h.to, stagingDir)); !os.IsNotExist(err) {
		t.Errorf("the staging dir should be gone, stat err = %v", err)
	}
	if rep.ArchiveKept != "" {
		t.Errorf("archive should have been deleted, report names %q", rep.ArchiveKept)
	}
}

func TestRunKeepsTheArchiveWhenAsked(t *testing.T) {
	h := newHarness(t, roomy)
	o := h.opts()
	o.KeepArchive = true

	rep, err := h.r.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if rep.ArchiveKept == "" {
		t.Fatal("--keep-archive must report where the archive is")
	}
	if _, err := os.Stat(rep.ArchiveKept); err != nil {
		t.Errorf("the kept archive is not there: %v", err)
	}
}

func TestFailedImportReportsHowToRecover(t *testing.T) {
	h := newHarness(t, roomy)
	h.w.importErr = errors.New("wsl: access denied")

	rep, err := h.r.Run(context.Background(), h.opts())
	var orphan *ErrOrphaned
	if !errors.As(err, &orphan) {
		t.Fatalf("got %v, want ErrOrphaned", err)
	}
	// The whole point: the data is still somewhere, and the message says where.
	if _, statErr := os.Stat(orphan.Archive); statErr != nil {
		t.Fatalf("the archive must survive a failed import: %v", statErr)
	}
	if !strings.Contains(orphan.Error(), "wsl --import") {
		t.Error("the error must spell out the recovery command")
	}
	if h.committed != "" {
		t.Error("a failed move must not record the new data dir")
	}
	if rep.Restarted {
		t.Error("a failed move must not claim it restarted the engine")
	}
}

func TestImportThatProducesNoDiskIsAnOrphan(t *testing.T) {
	h := newHarness(t, roomy)
	h.w.importSkipsDisk = true

	_, err := h.r.Run(context.Background(), h.opts())
	var orphan *ErrOrphaned
	if !errors.As(err, &orphan) {
		t.Fatalf("got %v, want ErrOrphaned — a silent import that made no disk is still a loss", err)
	}
	if _, statErr := os.Stat(orphan.Archive); statErr != nil {
		t.Errorf("the archive must survive: %v", statErr)
	}
}

func TestCommitFailureSaysTheEngineStillMoved(t *testing.T) {
	h := newHarness(t, roomy)
	h.commitFail = errors.New("disk full")

	_, err := h.r.Run(context.Background(), h.opts())
	if err == nil {
		t.Fatal("a failed commit must be reported")
	}
	if !strings.Contains(err.Error(), "moved to") {
		t.Errorf("the message must make clear the move itself succeeded, got: %v", err)
	}
}

func TestNonEmptyOldDirIsLeftAlone(t *testing.T) {
	h := newHarness(t, roomy)
	keep := filepath.Join(h.from, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake never deletes the old disk, so the dir stays non-empty anyway;
	// what matters is that a user file is never removed.
	if _, err := h.r.Run(context.Background(), h.opts()); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a file beside the disk must not be deleted: %v", err)
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		child, parent string
		want          bool
	}{
		{`C:\a\b`, `C:\a`, true},
		{`C:\a`, `C:\a`, true},
		{`C:\a`, `C:\a\b`, false},
		{`C:\b`, `C:\a`, false},
		{`D:\a`, `C:\a`, false},
	}
	for _, c := range cases {
		if got := within(c.child, c.parent); got != c.want {
			t.Errorf("within(%q, %q) = %v, want %v", c.child, c.parent, got, c.want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:             "512 B",
		1 << 10:         "1.0 KiB",
		3 << 20:         "3.0 MiB",
		uint64(1) << 30: "1.0 GiB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// The archive must survive a failed --restart.
//
// Starting the engine is the only step that exercises the relocated disk. It
// used to run after the archive had already been deleted and the old VHDX
// destroyed by the unregister, so the one check worth having happened when
// nothing could be done about the answer (#242).
func TestRunKeepsTheArchiveWhenTheRestartFails(t *testing.T) {
	h := newHarness(t, roomy)
	h.r.Start = func(context.Context) error { return errors.New("dockerd: cannot start on this volume") }
	o := h.opts()
	o.Restart = true

	rep, err := h.r.Run(context.Background(), o)
	if err == nil {
		t.Fatal("Run succeeded despite the engine failing to start at the new location")
	}

	var orph *ErrOrphaned
	if !errors.As(err, &orph) {
		t.Fatalf("error is %T, want *ErrOrphaned so the user is told where the data is: %v", err, err)
	}
	archive := orph.Archive
	if archive == "" {
		archive = rep.ArchiveKept
	}
	if archive == "" {
		t.Fatal("the failure names no archive")
	}
	if _, statErr := os.Stat(archive); statErr != nil {
		t.Errorf("the archive was deleted before the engine was proven to start: %v", statErr)
	}
	if !strings.Contains(err.Error(), "wsl --import") {
		t.Errorf("the message is not a recovery procedure:\n%s", err)
	}
}
