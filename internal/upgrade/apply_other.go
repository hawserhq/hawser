//go:build !windows

package upgrade

import (
	"errors"
	"fmt"
	"strings"
)

// The app upgrade is a Windows feature; these keep the package building on the
// Linux runner that vets it.

var Binaries = []string{"skrog.exe", "skrogw.exe", "skrogtray.exe"}

type SwapResult struct {
	Replaced []string
}

func SwapBinaries(string, string) (SwapResult, error) {
	return SwapResult{}, errors.New("applying an app upgrade is only supported on Windows")
}

func CleanOld(string) []string { return nil }

func AssetName(version, arch string) string {
	return fmt.Sprintf("skrog_%s_windows_%s.zip", strings.TrimPrefix(version, "v"), arch)
}
