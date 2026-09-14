package wslc

import "strings"

// PoliciesKey is where WSL reads its Group Policy configuration:
// HKLM\Software\Policies\WSL, from `ROOT_POLICIES_KEY L"\\WSL"` in
// src/windows/inc/wslpolicies.h.
const PoliciesKey = `Software\Policies\WSL`

// RegistryAllowlistSubkey holds the allowed container-image registries, one per
// string value. The value NAMES are ignored — Group Policy list editors
// generate them — and the DATA is the registry server.
const RegistryAllowlistSubkey = "WSLContainerRegistryAllowlist"

// Value names under PoliciesKey that Skrog cares about.
const (
	AllowContainersValue = "AllowWSLContainer"
	AllowPrivilegedValue = "AllowWSLContainerPrivileged"
)

// Policies is the WSL container policy an administrator has deployed.
//
// Skrog reads the same keys WSL itself reads rather than inventing its own,
// because the point is that an existing Intune or GPO deployment keeps meaning
// what it meant. A raw docker.sock relay bypasses the enforcement in
// wslcsession, so Skrog has to stand in for it (#322).
type Policies struct {
	// ContainersAllowed is AllowWSLContainer. Absent means allowed.
	//
	// Skrog does not need to enforce this one: WSL applies it when a session is
	// created (WSLCSessionManagerFactory), so a machine where containers are
	// forbidden has no session to attach to and the backend cannot start. It is
	// read so `doctor` can say why.
	ContainersAllowed bool

	// PrivilegedAllowed is AllowWSLContainerPrivileged. Absent means allowed.
	//
	// Note this key is declared in wslpolicies.h but is not enforced anywhere
	// in the published WSL source — so honouring it is forward-looking rather
	// than parity. Skrog enforces it anyway: an administrator who set it meant
	// something by it, and denying more than WSL does is the safe direction.
	PrivilegedAllowed bool

	// RegistryAllowlist is WSLContainerRegistryAllowlist. Empty means no
	// restriction.
	RegistryAllowlist []string
}

// Restrictive reports whether any policy actually constrains anything, which is
// what decides between "enforce" and "there is nothing to enforce".
func (p Policies) Restrictive() bool {
	return !p.ContainersAllowed || !p.PrivilegedAllowed || len(p.RegistryAllowlist) > 0
}

// RegistryAllowed applies WSLContainerRegistryAllowlist to one registry server,
// matching wsl::windows::policies::IsRegistryAllowed exactly:
//
//   - an absent or empty allowlist allows everything;
//   - an empty server is allowed (the caller could not attribute it, and WSL
//     does not guess);
//   - otherwise the server must match an entry case-insensitively, in full.
//
// Matching is on the SERVER, not the repository: an entry of "contoso.azurecr.io"
// permits every image on that registry and nothing else.
func (p Policies) RegistryAllowed(server string) bool {
	if len(p.RegistryAllowlist) == 0 {
		return true
	}
	if server == "" {
		return true
	}
	for _, entry := range p.RegistryAllowlist {
		if strings.EqualFold(entry, server) {
			return true
		}
	}
	return false
}

// HasRegistryAllowlist reports whether the allowlist is in force.
//
// WSL uses the equivalent check to refuse operations it cannot attribute to a
// registry — `wslc image build` does exactly this, because a Dockerfile can
// pull from anywhere. Skrog follows that precedent rather than letting an
// unattributable operation through (#322's fail-closed posture).
func (p Policies) HasRegistryAllowlist() bool { return len(p.RegistryAllowlist) > 0 }
