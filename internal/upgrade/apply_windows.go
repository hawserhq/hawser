//go:build windows

package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Binaries are the files an app upgrade replaces, in the install directory.
//
// skrog.exe is the one that matters: skrogw re-spawns the supervisor by path on
// every loop iteration, so replacing it means the next supervisor restart is
// already the new build. The other two only take effect when their own process
// next starts -- see SwapResult.
var Binaries = []string{"skrog.exe", "skrogw.exe", "skrogtray.exe"}

// oldSuffix marks a binary renamed out of the way. A running image cannot be
// overwritten on Windows, but it CAN be renamed: MoveFile succeeds on a mapped
// executable so long as the target is on the same volume. That is the whole
// trick, and the reason "a running .exe cannot replace itself" -- which this
// command used to say -- is not true (#309).
const oldSuffix = ".old"

// SwapResult says what actually changed.
//
// It deliberately does NOT report which processes are still executing the old
// image. The obvious way to find that -- match a running process by image name
// -- is wrong in two ways that a first version of this shipped: it matches a
// skrogw.exe belonging to a *different* install directory, and skrog.exe is
// always "running" because it is the process doing the upgrading. Determining
// it properly means enumerating processes and comparing full image paths, for
// an advisory sentence whose content is fixed anyway: skrog.exe is live
// immediately via the supervisor recycle, the other two at their next start.
// So the caller states that rule rather than probing for it.
type SwapResult struct {
	// Replaced is every binary now new on disk.
	Replaced []string
}

// SwapBinaries replaces the binaries in dir with the ones in staged.
//
// Verification is the caller's job and must already have happened: this
// function moves files, and a swap that begins with an unverified download is
// not made safer by anything it does afterwards.
//
// The order matters. Every rename happens first, and only then does anything
// get written; if a rename fails, the ones already done are put back and the
// directory is left as it was. A half-applied upgrade -- a new skrog.exe beside
// an old skrogw.exe from a different release -- is worse than no upgrade.
func SwapBinaries(dir, staged string) (SwapResult, error) {
	var res SwapResult
	var renamed []string

	undo := func() {
		for _, name := range renamed {
			_ = os.Rename(filepath.Join(dir, name+oldSuffix), filepath.Join(dir, name))
		}
	}

	for _, name := range Binaries {
		src := filepath.Join(staged, name)
		if _, err := os.Stat(src); err != nil {
			// Not every release has to carry every binary; a missing one is
			// left alone rather than deleted.
			continue
		}
		cur := filepath.Join(dir, name)
		if _, err := os.Stat(cur); err != nil {
			continue
		}
		// A leftover .old from a previous upgrade is simply overwritten: Go's
		// os.Rename is MoveFileEx(MOVEFILE_REPLACE_EXISTING) on Windows.
		//
		// Unless something still has it open -- an old skrogw or tray that has
		// not exited since the last upgrade -- in which case this rename fails
		// and the swap rolls back untouched. That is the right answer: the
		// previous upgrade has not finished taking effect yet, so stacking
		// another on top of it would leave two generations of .old and no way
		// to tell which is which. CleanOld on the next run clears the way.
		if err := os.Rename(cur, cur+oldSuffix); err != nil {
			undo()
			return SwapResult{}, fmt.Errorf("moving %s aside: %w", name, err)
		}
		renamed = append(renamed, name)
	}

	for _, name := range renamed {
		if err := copyFile(filepath.Join(staged, name), filepath.Join(dir, name)); err != nil {
			undo()
			return SwapResult{}, fmt.Errorf("installing %s: %w", name, err)
		}
		res.Replaced = append(res.Replaced, name)
	}
	return res, nil
}

// CleanOld deletes binaries left behind by a previous swap. Called on the next
// run rather than at the end of the swap, because at that moment the processes
// holding them are exactly the ones that have not exited yet.
//
// Failure is not an error: a file still held is one to try again next time, not
// a reason to fail the command the user actually asked for.
func CleanOld(dir string) []string {
	var removed []string
	for _, name := range Binaries {
		p := filepath.Join(dir, name+oldSuffix)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if os.Remove(p) == nil {
			removed = append(removed, name+oldSuffix)
		}
	}
	return removed
}

// AssetName is the release asset for an architecture, matching what the
// release workflow publishes and what install.ps1 downloads.
func AssetName(version, arch string) string {
	return fmt.Sprintf("skrog_%s_windows_%s.zip", strings.TrimPrefix(version, "v"), arch)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
