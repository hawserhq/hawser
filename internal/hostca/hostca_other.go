//go:build !windows

package hostca

import (
	"context"
	"fmt"
)

// HostRootCAs is Windows-only; elsewhere (CI Linux, the guest build) it reports
// unavailable rather than failing to compile.
func HostRootCAs(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("reading the host root CA store is only supported on Windows")
}
