package doctor

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// diskInfo reports free/total space on the volume that would hold path. The
// path itself need not exist yet (a fresh machine has no distro dir), so it
// walks up to the nearest existing ancestor — same volume — before asking.
func diskInfo(path string) DiskInfo {
	info := DiskInfo{Path: path}
	if path == "" {
		info.Err = "no path"
		return info
	}

	dir := existingAncestor(path)
	if dir == "" {
		info.Err = "no existing ancestor directory"
		return info
	}

	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		info.Err = err.Error()
		return info
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		info.Err = err.Error()
		return info
	}
	info.FreeBytes = freeToCaller
	info.TotalBytes = total
	return info
}

// existingAncestor returns path if it exists, else the closest parent that does.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "" // reached the root and it does not stat
		}
		path = parent
	}
}
