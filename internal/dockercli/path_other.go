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

// Scope mirrors the Windows type so callers compile off Windows; there is no
// machine/user PATH split to report, so everything is unknown.
type Scope int

const (
	ScopeUnknown Scope = iota
	ScopeUser
	ScopeMachine
)

func PathScopeOf(string) Scope { return ScopeUnknown }
