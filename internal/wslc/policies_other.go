//go:build !windows

package wslc

// ReadPolicies has nothing to read off Windows. The wslc backend only runs
// there; this exists so the package builds for tooling and tests elsewhere.
func ReadPolicies() (Policies, error) {
	return Policies{ContainersAllowed: true, PrivilegedAllowed: true}, nil
}
