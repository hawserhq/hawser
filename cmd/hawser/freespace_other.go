//go:build !windows

package main

import "errors"

// freeSpace is Windows-only, like the VHDX it measures.
func freeSpace(dir string) (uint64, error) { return 0, errors.New("not supported on this platform") }
