// Package vhdx reclaims space from the engine's virtual disk (#64).
//
// A WSL2 distro lives in an ext4.vhdx that only ever grows: delete 20 GB of
// images inside the engine and the file on C: stays exactly the same size. The
// space is free to ext4 and still occupied as far as Windows is concerned. Two
// separate things have to happen to get it back:
//
//  1. the guest tells the disk which blocks it no longer uses -- fstrim;
//  2. the disk stops reserving those blocks -- CompactVirtualDisk.
//
// Doing only the first leaves the file the same size. Doing only the second
// reclaims almost nothing, because the disk was never told anything was free.
//
// The mechanics here follow the research in hawserhq/wsldisk (docs/COMPACT.md
// and RESEARCH.md), which established the two facts this package depends on:
// CompactVirtualDisk on an *unattached* disk needs no administrator rights --
// so this works on Windows Home, where Optimize-VHD does not exist -- and the
// V2 open parameters must be paired with VIRTUAL_DISK_ACCESS_NONE, because the
// V1 shape accepts masks that open successfully and then fail at the
// compaction, i.e. after the user has been told their disk is about to shrink.
package vhdx

import "errors"

// ErrInUse reports that the disk file is open elsewhere, so it cannot be
// compacted. The WSL utility VM keeps every attached disk open for as long as
// any distro is running -- Docker Desktop's distros included -- and releases
// them about a minute after the last one stops (measured at ~66s against WSL
// 2.7.8.0, whose vmIdleTimeout defaults to 60s).
var ErrInUse = errors.New("vhdx: the disk is open in another process")

// ErrUnsupported reports that compaction is not available on this platform.
var ErrUnsupported = errors.New("vhdx: compaction is only supported on Windows")

// Result is what one compaction changed, in bytes on disk.
type Result struct {
	// Before and After are the file's size on disk.
	Before uint64 `json:"beforeBytes"`
	After  uint64 `json:"afterBytes"`
}

// Reclaimed is how much the file shrank. It is 0 rather than negative when a
// disk grew, which a caller should treat as a failed verification.
func (r Result) Reclaimed() uint64 {
	if r.After >= r.Before {
		return 0
	}
	return r.Before - r.After
}

// Grew reports whether the file ended up larger than it started, which means
// something wrote to the disk during the compaction.
func (r Result) Grew() bool { return r.After > r.Before }
