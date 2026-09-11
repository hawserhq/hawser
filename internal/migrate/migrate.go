// Package migrate moves images and volumes from Docker Desktop into the
// Skrog engine (#43): "try Skrog" as a reversible experiment, not a fresh
// start.
//
// The approach is deliberately the safe-but-slower one. Images move by
// `docker save | docker load` and volumes by streaming a tar, both engine to
// engine over the docker API — never by copying VHDX files. That cannot
// corrupt the source, and if a transfer is interrupted Docker Desktop is
// exactly as it was: this package issues no destructive command against the
// source, ever. Build cache is out of scope for v0.2 — it is not portable
// through save/load, and pretending to move it would be worse than saying so.
package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Image is a source image considered for migration.
type Image struct {
	// Ref is a repo:tag reference used to save the image. An image with
	// several tags contributes several refs; a dangling image (<none>) has no
	// usable ref and is skipped.
	Ref  string
	ID   string
	Size int64
}

// Volume is a named source volume.
type Volume struct {
	Name string
	// Size is the on-disk size in bytes, or -1 when the source could not
	// report it (older engines); planning still lists it, sized "unknown".
	Size int64
}

// Engine enumerates a docker engine's migratable state. Both the source
// (Docker Desktop) and destination (Skrog) are Engines.
type Engine interface {
	Images(ctx context.Context) ([]Image, error)
	Volumes(ctx context.Context) ([]Volume, error)
}

// Transfer performs the actual byte movement between the two engines, and
// answers what the destination already has. Separated from Engine so the
// orchestration — what to move, what to skip, dry-run — is testable without
// docker.
type Transfer interface {
	// HasImage reports whether the destination already has this exact ref.
	HasImage(ctx context.Context, ref string) (bool, error)
	// HasVolume reports whether the destination already has this volume.
	HasVolume(ctx context.Context, name string) (bool, error)
	// MoveImages streams the given refs source->destination in one
	// save|load, the batching docker itself is most efficient at.
	MoveImages(ctx context.Context, refs []string) error
	// MoveVolume creates the volume on the destination and streams its
	// contents as a tar. Must not modify the source volume.
	MoveVolume(ctx context.Context, name string) error
}

// Plan is what a migration would do, resolved but not yet executed.
type Plan struct {
	Images       []Image
	Volumes      []Volume
	SkippedImgs  []Image  // already on the destination
	SkippedVols  []Volume // already on the destination
	Unreferenced int      // dangling source images, not migratable
}

// TotalBytes is the size of everything that would actually move.
func (p Plan) TotalBytes() int64 {
	var n int64
	for _, i := range p.Images {
		n += i.Size
	}
	for _, v := range p.Volumes {
		if v.Size > 0 {
			n += v.Size
		}
	}
	return n
}

// Empty reports that there is nothing to move.
func (p Plan) Empty() bool { return len(p.Images) == 0 && len(p.Volumes) == 0 }

// Migrator plans and runs migrations.
type Migrator struct {
	Source   Engine
	Dest     Engine
	Transfer Transfer
	Logger   *slog.Logger

	// Only, when non-empty, restricts the migration to images and volumes
	// whose ref or name contains one of these substrings. Empty migrates
	// everything. Lets a user move just their postgres image, and keeps the
	// acceptance test from copying a developer's whole Desktop.
	Only []string
}

// selected reports whether name passes the Only filter.
func (m *Migrator) selected(name string) bool {
	if len(m.Only) == 0 {
		return true
	}
	for _, sub := range m.Only {
		if strings.Contains(name, sub) {
			return true
		}
	}
	return false
}

func (m *Migrator) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// Plan enumerates both engines and works out what to move and what the
// destination already has. Read-only against both.
func (m *Migrator) Plan(ctx context.Context) (Plan, error) {
	srcImages, err := m.Source.Images(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("listing source images: %w", err)
	}
	srcVols, err := m.Source.Volumes(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("listing source volumes: %w", err)
	}

	var p Plan
	for _, img := range srcImages {
		// A dangling image has no ref to save under; count it so the summary
		// can explain the difference from `docker images`, but do not move it.
		if img.Ref == "" || strings.Contains(img.Ref, "<none>") {
			p.Unreferenced++
			continue
		}
		if !m.selected(img.Ref) {
			continue
		}
		has, err := m.Transfer.HasImage(ctx, img.Ref)
		if err != nil {
			return Plan{}, fmt.Errorf("checking destination for %s: %w", img.Ref, err)
		}
		if has {
			p.SkippedImgs = append(p.SkippedImgs, img)
			continue
		}
		p.Images = append(p.Images, img)
	}

	for _, vol := range srcVols {
		if !m.selected(vol.Name) {
			continue
		}
		has, err := m.Transfer.HasVolume(ctx, vol.Name)
		if err != nil {
			return Plan{}, fmt.Errorf("checking destination for volume %s: %w", vol.Name, err)
		}
		if has {
			p.SkippedVols = append(p.SkippedVols, vol)
			continue
		}
		p.Volumes = append(p.Volumes, vol)
	}

	// Stable, largest-first: the big movers are what a user watching progress
	// cares about, and deterministic order makes dry-run diffable.
	sort.SliceStable(p.Images, func(i, j int) bool { return p.Images[i].Size > p.Images[j].Size })
	sort.SliceStable(p.Volumes, func(i, j int) bool { return p.Volumes[i].Size > p.Volumes[j].Size })
	return p, nil
}

// Result records what actually moved.
type Result struct {
	ImagesMoved  int
	VolumesMoved int
	Errors       []error
}

// Run executes a plan. Volumes and images are independent, so one failure
// does not abort the rest: a partial migration is re-runnable (already-moved
// items are skipped next time), which beats an all-or-nothing that strands the
// user halfway with no way forward. The source is never modified.
func (m *Migrator) Run(ctx context.Context, p Plan) Result {
	var r Result

	if len(p.Images) > 0 {
		refs := make([]string, len(p.Images))
		for i, img := range p.Images {
			refs[i] = img.Ref
		}
		// One save|load for all images: shared layers cross the wire once.
		m.log().Info("migrating images", "count", len(refs))
		if err := m.Transfer.MoveImages(ctx, refs); err != nil {
			r.Errors = append(r.Errors, fmt.Errorf("moving images: %w", err))
		} else {
			r.ImagesMoved = len(refs)
		}
	}

	for _, vol := range p.Volumes {
		m.log().Info("migrating volume", "name", vol.Name, "size", ByteSize(vol.Size))
		if err := m.Transfer.MoveVolume(ctx, vol.Name); err != nil {
			r.Errors = append(r.Errors, fmt.Errorf("moving volume %s: %w", vol.Name, err))
			continue
		}
		r.VolumesMoved++
	}
	return r
}

// ByteSize formats a size for humans; "unknown" for the negative sentinel.
func ByteSize(n int64) string {
	if n < 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
