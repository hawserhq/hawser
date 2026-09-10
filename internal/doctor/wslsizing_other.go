//go:build !windows

package doctor

// hostRAM is Windows-only: the sizing it informs is a WSL2 concept.
func hostRAM() uint64 { return 0 }
