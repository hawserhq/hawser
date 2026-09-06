//go:build !windows

package doctor

// diskInfo is Windows-only in practice; on other platforms (tests on CI Linux,
// the guest build) it reports unavailable rather than failing to compile.
func diskInfo(path string) DiskInfo {
	return DiskInfo{Path: path, Err: "disk space check is Windows-only"}
}
