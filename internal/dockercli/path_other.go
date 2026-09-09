//go:build !windows

package dockercli

import "errors"

// errNotWindows is returned by the PATH helpers off Windows: the CLI bundle is a
// Windows feature, but the package must still compile on the Linux CI runner
// that vets the codebase.
var errNotWindows = errors.New("user PATH management is only supported on Windows")

func AddToUserPath(string) (bool, error)      { return false, errNotWindows }
func RemoveFromUserPath(string) (bool, error) { return false, errNotWindows }
func UserPathContains(string) (bool, error)   { return false, errNotWindows }
