package doctor

import (
	"fmt"
	"sort"
	"strings"
)

// checkVPN recognizes a corporate VPN from the host's active adapters and spells
// out the connectivity settings that keep container egress working through the
// tunnel (#63). It is the second half of the "works at home, breaks at work"
// story after CA trust (#62): a GlobalProtect or AnyConnect tunnel clamps the
// path MTU or hijacks DNS, and the engine's pulls hang or containers cannot
// resolve names — with no error that points at the VPN.
//
// Advisory about the GLOBAL settings, by design: the ~/.wslconfig keys affect
// every WSL2 distro on the machine, not just Hawser's, so doctor NAMES the VPN
// and shows the exact settings rather than editing global config behind your
// back. The engine's own MTU is different -- that distro is ours -- so the
// remedy names `hawser config set engine.mtu <n>` with the fingerprint's
// recommended value, which dockerd validates and rolls back if it breaks.
func checkVPN() Check {
	c := Check{Name: "vpn", Title: "corporate VPN"}
	c.Run = func(f Facts) Result {
		if len(f.VPNs) == 0 {
			return result(c, Skip, "no corporate VPN adapter detected")
		}

		names := make([]string, 0, len(f.VPNs))
		var detail []string
		var wslKeys []string
		wslSeen := map[string]string{}
		bestMTU := 0
		for _, m := range f.VPNs {
			names = append(names, m.Name)
			detail = append(detail, fmt.Sprintf("  %s  (adapter: %s)", m.Name, m.Adapter.Name))
			if m.MTU > 0 {
				detail = append(detail, fmt.Sprintf("    recommended engine MTU: %d", m.MTU))
				// Two tunnels up at once: the smaller clamp is the one that
				// has to win, or the larger still fragments.
				if bestMTU == 0 || m.MTU < bestMTU {
					bestMTU = m.MTU
				}
			}
			if len(m.DNS) > 0 {
				detail = append(detail, "    DNS fallback: "+strings.Join(m.DNS, ", "))
			}
			for k, v := range m.WSLConfig {
				if _, ok := wslSeen[k]; !ok {
					wslSeen[k] = v
					wslKeys = append(wslKeys, k)
				}
			}
			detail = append(detail, "    "+m.Note)
		}

		r := result(c, Warn, "a VPN is active ("+strings.Join(names, ", ")+
			"); container egress may need MTU/DNS tuning")
		r.Detail = detail

		var remedy strings.Builder
		remedy.WriteString("if pulls hang or containers cannot resolve names on the VPN:\n")
		if len(wslKeys) > 0 {
			sort.Strings(wslKeys)
			remedy.WriteString("      1. add to ~/.wslconfig under [wsl2], then `wsl --shutdown` " +
				"(affects ALL WSL2 distros, so review first):\n")
			remedy.WriteString("         [wsl2]\n")
			for _, k := range wslKeys {
				remedy.WriteString(fmt.Sprintf("         %s=%s\n", k, wslSeen[k]))
			}
		}
		// Unlike step 1, this one is actionable: the engine distro is ours, so
		// clamping its MTU is not a global change. dockerd validates the value
		// before it applies and the engine rolls back if it will not restart.
		if bestMTU > 0 {
			remedy.WriteString(fmt.Sprintf("      2. if the engine still stalls, clamp the engine's MTU:\n"+
				"         hawser config set engine.mtu %d\n", bestMTU))
		} else {
			remedy.WriteString("      2. if the engine still stalls, clamp the engine's MTU with " +
				"`hawser config set engine.mtu <value>` (1400 is a common starting point).\n")
		}
		remedy.WriteString("      3. behind a TLS-inspecting VPN (e.g. Zscaler), also " +
			"`hawser config set network.import-host-cas on` so pulls trust the VPN's root CA.")
		r.Remedy = remedy.String()
		return r
	}
	return c
}
