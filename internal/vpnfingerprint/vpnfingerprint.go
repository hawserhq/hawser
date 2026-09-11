// Package vpnfingerprint recognizes VPN clients by their virtual network
// adapter and maps each to the connectivity settings that keep container egress
// working through the tunnel (#63).
//
// The split-tunnel and MTU quirks of corporate VPNs are the second big "works
// at home, breaks at work" failure after CA trust (#62): a GlobalProtect or
// AnyConnect tunnel clamps the path MTU or hijacks DNS, and the engine's pulls
// hang or its containers cannot resolve names. This package is the data — which
// adapter means which VPN, and the known-good MTU / DNS / .wslconfig settings —
// as a pure function of the adapters present, so doctor can name the VPN and
// recommend the fix without a live tunnel to test against.
//
// Matching keys off the adapter DESCRIPTION, not its connection name: the
// description is what the vendor's driver sets (e.g. "PANGP Virtual Ethernet
// Adapter") and is stable, whereas the connection name ("Ethernet 4") is
// user-renameable.
package vpnfingerprint

import "strings"

// Adapter is one network adapter as read from the host: its connection Name,
// vendor-set Description, and whether it is currently Up. Detect is pure over a
// slice of these, so gathering (the OS call) stays out of the matching logic.
type Adapter struct {
	Name        string
	Description string
	Up          bool
}

// Fingerprint is a known VPN client and the settings that make container egress
// work through its tunnel. The MTU/DNS values are conservative starting points,
// not guarantees — the tunnel's real overhead varies by deployment — so callers
// present them as advice, not silent truth.
type Fingerprint struct {
	// Name is the human label, e.g. "Palo Alto GlobalProtect".
	Name string
	// Match is the set of lowercased substrings that identify this VPN by an
	// adapter description. Any one matching is a hit.
	Match []string
	// MTU is the recommended MTU (bytes) for the engine's interface inside the
	// distro; 0 means "no clamp needed".
	MTU int
	// DNS is a fallback resolver list to use when the tunnel breaks split-DNS
	// resolution for containers; empty means "no change".
	DNS []string
	// WSLConfig lists the ~/.wslconfig [wsl2] keys that help this VPN. These are
	// global (they affect every WSL2 distro), so they are advisory only — doctor
	// shows them, it does not edit .wslconfig.
	WSLConfig map[string]string
	// Note explains the failure mode and why the settings help.
	Note string
}

// Match is a detected VPN: the fingerprint plus the adapter that matched it.
type Match struct {
	Fingerprint
	Adapter Adapter
}

// DB is the built-in fingerprint database. Order is by prevalence in corporate
// fleets, so the first match is usually the relevant one.
func DB() []Fingerprint {
	return []Fingerprint{
		{
			Name:  "Palo Alto GlobalProtect",
			Match: []string{"pangp", "globalprotect", "palo alto gp"},
			MTU:   1400,
			DNS:   []string{"1.1.1.1", "8.8.8.8"},
			WSLConfig: map[string]string{
				"networkingMode": "mirrored",
				"dnsTunneling":   "true",
				"autoProxy":      "true",
			},
			Note: "GlobalProtect clamps the tunnel MTU and enforces split DNS; " +
				"mirrored networking (WSL 2.0.9+) lets the distro share the host's " +
				"VPN routes and DNS, which fixes both the hang and name resolution.",
		},
		{
			Name:  "Cisco AnyConnect / Secure Client",
			Match: []string{"cisco anyconnect", "anyconnect", "cisco secure client"},
			MTU:   1300,
			DNS:   []string{"1.1.1.1", "8.8.8.8"},
			WSLConfig: map[string]string{
				"networkingMode": "mirrored",
				"dnsTunneling":   "true",
			},
			Note: "AnyConnect's DTLS tunnel has high per-packet overhead; a lower " +
				"MTU stops large registry responses from black-holing, and mirrored " +
				"networking carries the tunnel's routes into the distro.",
		},
		{
			Name:  "Zscaler",
			Match: []string{"zscaler"},
			MTU:   1400,
			WSLConfig: map[string]string{
				"dnsTunneling": "true",
			},
			Note: "Zscaler intercepts and re-signs TLS; besides MTU, the engine must " +
				"trust the Zscaler root CA — turn on `skrog config set " +
				"network.import-host-cas on` to avoid x509 pull errors.",
		},
		{
			Name:  "OpenVPN",
			Match: []string{"tap-windows", "openvpn", "ovpn", "tap-win"},
			MTU:   1400,
			Note: "OpenVPN's tun encapsulation reduces the usable MTU; clamp the " +
				"engine interface so large pulls do not stall behind a dropped fragment.",
		},
		{
			Name:  "WireGuard",
			Match: []string{"wireguard", "wintun"},
			MTU:   1420,
			Note: "WireGuard's fixed 60-byte overhead gives a 1420 MTU on a 1500 " +
				"path; match it so the engine does not emit oversized frames.",
		},
		{
			Name:  "Fortinet FortiClient",
			Match: []string{"forticlient", "fortinet", "fortissl"},
			MTU:   1400,
			WSLConfig: map[string]string{
				"networkingMode": "mirrored",
			},
			Note: "FortiClient's SSL VPN clamps the path MTU; a matching engine MTU " +
				"and mirrored networking keep container egress working through it.",
		},
	}
}

// Detect returns the fingerprints whose Match substrings appear in the
// description (or, as a fallback, the name) of any adapter that is Up. It is
// pure over the supplied adapters. A VPN whose adapter is present but Down is
// not reported: the tunnel is not the current path, so its clamp does not apply.
func Detect(adapters []Adapter) []Match {
	var out []Match
	for _, fp := range DB() {
		for _, a := range adapters {
			if !a.Up {
				continue
			}
			if fingerprintMatches(fp, a) {
				out = append(out, Match{Fingerprint: fp, Adapter: a})
				break // one adapter per fingerprint is enough
			}
		}
	}
	return out
}

func fingerprintMatches(fp Fingerprint, a Adapter) bool {
	desc := strings.ToLower(a.Description)
	name := strings.ToLower(a.Name)
	for _, m := range fp.Match {
		if strings.Contains(desc, m) || strings.Contains(name, m) {
			return true
		}
	}
	return false
}
