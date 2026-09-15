//go:build windows

package wslc

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// ReadPolicies loads the deployed WSL container policy.
//
// A missing policies key is not an error: it is the ordinary state of a machine
// with no policy deployed, and WSL treats it the same way — wslpolicies.h
// returns an empty handle rather than failing "to make it easier to check for
// policies without having a special code path for this case".
func ReadPolicies() (Policies, error) {
	p := Policies{ContainersAllowed: true, PrivilegedAllowed: true}

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, PoliciesKey, registry.READ)
	if err != nil {
		if err == registry.ErrNotExist {
			return p, nil
		}
		return p, fmt.Errorf("opening %s: %w", PoliciesKey, err)
	}
	defer key.Close()

	p.ContainersAllowed = policyAllows(key, AllowContainersValue)
	p.PrivilegedAllowed = policyAllows(key, AllowPrivilegedValue)

	allowlist, err := readRegistryAllowlist(key)
	if err != nil {
		return p, err
	}
	p.RegistryAllowlist = allowlist
	return p, nil
}

// policyAllows reads a DWORD policy where absent means allowed and zero means
// denied, matching GetPolicyValue's optional-DWORD handling.
func policyAllows(key registry.Key, name string) bool {
	v, _, err := key.GetIntegerValue(name)
	if err != nil {
		// Absent, or the wrong type: WSL treats both as "not configured".
		return true
	}
	return v != 0
}

// readRegistryAllowlist enumerates the sub-key's string values.
//
// The value DATA is the registry server; names are ignored because Group Policy
// list editors generate them. Empty values are skipped for the reason WSL skips
// them: "so a stray blank list item in the GP editor doesn't make the allowlist
// non-empty (which would otherwise deny every registry)".
func readRegistryAllowlist(policies registry.Key) ([]string, error) {
	sub, err := registry.OpenKey(policies, RegistryAllowlistSubkey, registry.READ|registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s\\%s: %w", PoliciesKey, RegistryAllowlistSubkey, err)
	}
	defer sub.Close()

	names, err := sub.ReadValueNames(-1)
	if err != nil {
		return nil, fmt.Errorf("enumerating %s: %w", RegistryAllowlistSubkey, err)
	}

	var entries []string
	for _, n := range names {
		v, _, err := sub.GetStringValue(n)
		if err != nil || v == "" {
			continue
		}
		entries = append(entries, v)
	}
	return entries, nil
}
