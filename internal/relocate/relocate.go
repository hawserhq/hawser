// Package relocate moves the engine's data directory — the VHDX and every
// image, container and volume in it — to another drive (#64). "Move it off C:"
// is a top-3 request, and the only housekeeping lever that requires the
// engine's entire state to survive a round trip.
//
// The mechanics are snapshot.Save followed by snapshot.Restore with the data
// dir pointed at the new location, rather than a second implementation of the
// same dance: that path already verifies the archive's checksum BEFORE it
// unregisters anything, which is the property that matters here. `wsl
// --unregister` deletes the VHDX, so between unregister and a completed import
// the archive is the only copy of the user's data — it is therefore written
// first, checksummed, and removed only once the new distro is in place.
//
// The archive is staged on the TARGET volume, not in the state dir. The reason
// to relocate is usually that the current drive is full, so staging there would
// fail exactly when it is needed most.
package relocate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wslkit/skrog/internal/snapshot"
	"github.com/wslkit/skrog/internal/wsl"
)

// DiskName is the file WSL creates for a distro's filesystem.
const DiskName = "ext4.vhdx"

// stagingDir is the transient directory created under the target to hold the
// archive. Removed on success; left in place, with the archive, on a failure
// that needs manual recovery.
const stagingDir = ".skrog-relocate"

// ErrSameDir is returned when the target resolves to the current data dir.
type ErrSameDir struct{ Path string }

func (e *ErrSameDir) Error() string {
	return fmt.Sprintf("the engine data already lives in %s", e.Path)
}

// ErrNested is returned when the target sits inside the current data dir,
// which `wsl --unregister` would delete out from under the import.
type ErrNested struct{ From, To string }

func (e *ErrNested) Error() string {
	return fmt.Sprintf("%s is inside the current data dir %s; pick a location outside it", e.To, e.From)
}

// ErrTargetNotEmpty is returned when the target already holds a distro disk.
// Overwriting one would destroy whatever engine it belongs to.
type ErrTargetNotEmpty struct{ Path string }

func (e *ErrTargetNotEmpty) Error() string {
	return fmt.Sprintf("%s already contains a %s; refusing to overwrite it", e.Path, DiskName)
}

// ErrNotEnoughSpace is returned before anything is touched. Need is the peak
// requirement — the archive and the imported disk exist at the same time.
type ErrNotEnoughSpace struct {
	Path       string
	Need, Free uint64
}

func (e *ErrNotEnoughSpace) Error() string {
	return fmt.Sprintf("%s has %s free, and the move needs about %s at its peak (the archive and the new disk exist at the same time)",
		e.Path, HumanBytes(e.Free), HumanBytes(e.Need))
}

// ErrOrphaned is the one failure worth its own type: the old distro is already
// unregistered — so its VHDX is gone — and the import did not finish. The data
// is intact in the archive, and the message is the recovery procedure.
type ErrOrphaned struct {
	Distro  string
	Archive string
	Dir     string
	Err     error
}

func (e *ErrOrphaned) Error() string {
	return fmt.Sprintf("the engine distro was removed but the import into %s failed: %v\n"+
		"  Your data is intact in %s.\n"+
		"  Recover with:  wsl --import %s %s %s",
		e.Dir, e.Err, e.Archive, e.Distro, e.Dir, e.Archive)
}

func (e *ErrOrphaned) Unwrap() error { return e.Err }

// Volume is the seam onto the filesystem facts a relocation needs, so the
// planning half is testable without a real disk.
type Volume interface {
	// Free reports bytes available to this user on the volume that would hold
	// path. The path need not exist yet.
	Free(path string) (uint64, error)
	// SizeOnDisk reports how many bytes a file actually occupies.
	SizeOnDisk(path string) (uint64, error)
}

// Options describes one relocation.
type Options struct {
	// Distro is the engine distro to move.
	Distro string
	// From is the current data dir, To the requested one.
	From, To string
	// DryRun plans and validates but changes nothing.
	DryRun bool
	// Restart brings the engine back afterwards.
	Restart bool
	// KeepArchive leaves the staged tarball in place instead of deleting it
	// once the import is verified — for anyone who wants a second copy before
	// trusting the new location.
	KeepArchive bool
}

// Report is what happened, and the pinned shape behind --json.
type Report struct {
	Distro      string   `json:"distro"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	MovedBytes  int64    `json:"movedBytes"`
	SHA256      string   `json:"sha256,omitempty"`
	NeedBytes   uint64   `json:"needBytes"`
	FreeBytes   uint64   `json:"freeBytes"`
	Steps       []string `json:"steps"`
	DryRun      bool     `json:"dryRun"`
	Restarted   bool     `json:"restarted"`
	ArchiveKept string   `json:"archiveKept,omitempty"`
}

// Runner performs relocations. Engine lifecycle and manifest persistence stay
// with the caller, which owns the supervisor and the install record.
type Runner struct {
	WSL    wsl.WSL
	Volume Volume
	// Stop and Start bracket the move. Stop must also record the desired state,
	// or a running supervisor restarts the engine mid-move.
	Stop  func(context.Context) error
	Start func(context.Context) error
	// Commit records the new data dir once the import is verified. A failure
	// here leaves a working engine in the new location with a stale manifest,
	// so it is reported but does not roll the move back.
	Commit func(newDataDir string) error
	Logger *slog.Logger
}

func (r *Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Plan validates the request and works out what the move needs, without
// touching anything. Run calls it first; --dry-run stops after it.
func (r *Runner) Plan(opts Options) (Report, error) {
	rep := Report{Distro: opts.Distro, From: opts.From, To: opts.To, DryRun: opts.DryRun}

	if opts.To == "" {
		return rep, fmt.Errorf("relocate: no target directory")
	}
	if !filepath.IsAbs(opts.To) {
		return rep, fmt.Errorf("relocate: %s is not an absolute path", opts.To)
	}
	from := filepath.Clean(opts.From)
	to := filepath.Clean(opts.To)
	rep.From, rep.To = from, to

	if sameDir(from, to) {
		return rep, &ErrSameDir{Path: to}
	}
	if within(to, from) {
		return rep, &ErrNested{From: from, To: to}
	}
	if _, err := os.Stat(filepath.Join(to, DiskName)); err == nil {
		return rep, &ErrTargetNotEmpty{Path: to}
	}

	size, err := r.Volume.SizeOnDisk(filepath.Join(from, DiskName))
	if err != nil {
		return rep, fmt.Errorf("relocate: measuring the current disk: %w", err)
	}
	// Peak usage on the target is the archive plus the freshly imported disk.
	// Both are bounded by the used bytes in the current one, and its size on
	// disk is the only figure available before the export runs — so this is
	// deliberately generous rather than precise.
	rep.NeedBytes = 2 * size
	free, err := r.Volume.Free(to)
	if err != nil {
		return rep, fmt.Errorf("relocate: checking free space on %s: %w", to, err)
	}
	rep.FreeBytes = free
	if free < rep.NeedBytes {
		return rep, &ErrNotEnoughSpace{Path: to, Need: rep.NeedBytes, Free: free}
	}

	rep.Steps = []string{
		"stop the engine",
		fmt.Sprintf("export %s to a checksummed archive under %s", opts.Distro, filepath.Join(to, stagingDir)),
		fmt.Sprintf("unregister %s (this deletes the old %s)", opts.Distro, DiskName),
		fmt.Sprintf("import it into %s", to),
		"record the new data dir in the install manifest",
	}
	if !opts.KeepArchive {
		rep.Steps = append(rep.Steps, "delete the archive")
	}
	if opts.Restart {
		rep.Steps = append(rep.Steps, "start the engine")
	}
	return rep, nil
}

// Run performs the relocation.
func (r *Runner) Run(ctx context.Context, opts Options) (Report, error) {
	rep, err := r.Plan(opts)
	if err != nil || opts.DryRun {
		return rep, err
	}

	if err := r.Stop(ctx); err != nil {
		return rep, fmt.Errorf("stopping the engine: %w", err)
	}

	// The Manager's state dir is the staging root, which is what puts the
	// archive on the target volume; its DataDir is where the import lands.
	staging := filepath.Join(rep.To, stagingDir)
	mgr := &snapshot.Manager{
		WSL:      r.WSL,
		Distro:   opts.Distro,
		StateDir: staging,
		DataDir:  rep.To,
		Logger:   r.Logger,
	}
	name := "relocate-" + time.Now().UTC().Format("20060102-150405")

	r.log().Info("exporting the engine before the move", "distro", opts.Distro, "to", rep.To)
	meta, err := mgr.Save(ctx, name, "")
	if err != nil {
		return rep, fmt.Errorf("exporting the engine: %w", err)
	}
	rep.MovedBytes, rep.SHA256 = meta.SizeBytes, meta.SHA256

	// Restore verifies the checksum before it unregisters anything, so a
	// corrupt archive fails with the old engine still registered.
	r.log().Info("importing the engine at its new location", "dir", rep.To)
	if err := mgr.Restore(ctx, name); err != nil {
		// The archive is never cleaned up on this path: if the unregister
		// happened and the import did not, it is the only copy of the data.
		return rep, &ErrOrphaned{Distro: opts.Distro, Archive: mgr.ArchivePath(name), Dir: rep.To, Err: err}
	}
	if _, err := os.Stat(filepath.Join(rep.To, DiskName)); err != nil {
		return rep, &ErrOrphaned{Distro: opts.Distro, Archive: mgr.ArchivePath(name), Dir: rep.To,
			Err: fmt.Errorf("no %s appeared at the target", DiskName)}
	}

	if r.Commit != nil {
		if err := r.Commit(rep.To); err != nil {
			// The engine is fine and in the right place; only the record is
			// stale. Say so precisely rather than implying the move failed.
			return rep, fmt.Errorf("the engine moved to %s, but recording it in the manifest failed: %w", rep.To, err)
		}
	}

	if opts.KeepArchive {
		rep.ArchiveKept = mgr.ArchivePath(name)
	} else {
		if err := mgr.Delete(name); err != nil {
			r.log().Warn("could not remove the staged archive", "err", err)
			rep.ArchiveKept = mgr.ArchivePath(name)
		}
		// Both removals are best-effort and only succeed while empty, so a
		// leftover file — something else living here — is never discarded.
		os.Remove(filepath.Join(staging, "snapshots"))
		os.Remove(staging)
	}

	// The old directory keeps whatever else lived beside the VHDX; remove it
	// only when the move left it empty, so nothing of the user's is discarded.
	if entries, err := os.ReadDir(rep.From); err == nil && len(entries) == 0 {
		os.Remove(rep.From)
	}

	if opts.Restart {
		if err := r.Start(ctx); err != nil {
			return rep, fmt.Errorf("the engine moved to %s, but starting it failed: %w", rep.To, err)
		}
		rep.Restarted = true
	}
	return rep, nil
}

// sameDir compares two cleaned paths the way Windows resolves them.
func sameDir(a, b string) bool { return strings.EqualFold(a, b) }

// within reports whether child is inside parent, or is parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// HumanBytes renders a size for messages that are read by people rather than
// parsed. Exported so the command layer prints the same units the errors do.
func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
