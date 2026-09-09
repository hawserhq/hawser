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
// Advisory by design. The remedies touch the global ~/.wslconfig (every WSL2
// distro, not just Hawser's) and a live MTU clamp, so doctor NAMES the VPN and
// shows the exact settings rather than editing global config behind your back.
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
		for _, m := range f.VPNs {
			names = append(names, m.Name)
			detail = append(detail, fmt.Sprintf("  %s  (adapter: %s)", m.Name, m.Adapter.Name))
			if m.MTU > 0 {
				detail = append(detail, fmt.Sprintf("    recommended engine MTU: %d", m.MTU))
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
		remedy.WriteString("      2. if the engine still stalls, clamp its interface MTU inside the distro.\n")
		remedy.WriteString("      3. behind a TLS-inspecting VPN (e.g. Zscaler), also " +
			"`hawser config set network.import-host-cas on` so pulls trust the VPN's root CA.")
		r.Remedy = remedy.String()
		return r
	}
	return c
}
