//go:build windows

package relocate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hawserhq/hawser/internal/vhdx"
	"golang.org/x/sys/windows"
)

// RealVolume is the production Volume: the actual disk.
type RealVolume struct{}

// Free reports bytes available to this user on the volume that would hold
// path. The target of a relocation usually does not exist yet, so this walks up
// to the nearest existing ancestor — still the same volume — before asking.
func (RealVolume) Free(path string) (uint64, error) {
	dir := existingAncestor(path)
	if dir == "" {
		return 0, fmt.Errorf("no existing directory above %s", path)
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	// freeToCaller, not totalFree: a quota-limited volume can have space the
	// user cannot actually use, and the move would fail halfway through it.
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return freeToCaller, nil
}

// SizeOnDisk reports the bytes the file actually occupies, which for a VHDX is
// what the volume has handed it rather than its virtual size.
func (RealVolume) SizeOnDisk(path string) (uint64, error) { return vhdx.SizeOnDisk(path) }

// existingAncestor returns path if it exists, else the closest parent that does.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}
