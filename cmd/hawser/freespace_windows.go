//go:build windows

package main

import "golang.org/x/sys/windows"

// freeSpace is free space on the volume holding dir, for `status --stats`
// (#179). doctor asks the same question through its own facts; this is the one
// call, without the diagnostic wrapper.
func freeSpace(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return freeToCaller, nil
}
