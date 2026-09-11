package doctor

import (
	"fmt"
	"strings"

	"github.com/hawserhq/hawser/internal/wslconfig"
)

// smallHostBytes is where WSL's default sizing starts to hurt. Below ~8 GB of
// host RAM, the default (half of it) plus a build's own memory is enough to
// push the machine into swap — and the symptom is a slow laptop, not an error
// anyone would connect to the engine.
const smallHostBytes = 8 << 30

// checkWSLSizing reports the WSL2 VM's effective sizing, and warns only where
// it has a concrete consequence (#148).
//
// Always reporting is the point: "how much memory can the engine take" is a
// question people ask, and the answer lives in a global file most have never
// opened. Judging is deliberately narrow — WSL's 50% default is right on a
// 32 GB workstation and wrong on an 8 GB runner, so a check that warned
// whenever no explicit limit was set would cry wolf on most machines. It warns
// when the host is small AND nothing has been set, which is the case where the
// default actually bites.
func checkWSLSizing() Check {
	c := Check{Name: "wsl-sizing", Title: "WSL2 VM sizing"}
	c.Run = func(f Facts) Result {
		s := f.WSLSizing
		if s.Err != "" {
			return result(c, Skip, "could not read "+s.Path+": "+s.Err)
		}

		var detail []string
		for _, k := range wslconfig.Managed() {
			if v, ok := s.Effective[k]; ok {
				detail = append(detail, fmt.Sprintf("  %-18s %s", k, v))
			}
		}
		if len(detail) > 0 {
			detail = append(detail, "  (from "+s.Path+")")
		}

		// Pending changes are worth surfacing wherever we are: someone set
		// wsl.memory and never ran apply, so the setting they think is in
		// force is not.
		if len(s.Pending) > 0 {
			r := result(c, Warn, fmt.Sprintf("%d sizing change(s) recorded but not applied to %s",
				len(s.Pending), s.Path))
			r.Detail = append(detail, "", "pending: "+strings.Join(s.Pending, ", "))
			r.Remedy = "`hawser wsl-config apply` shows the diff and writes it (--yes on a runner).\n" +
				"      The new sizing takes effect when the WSL VM next starts."
			return r
		}

		if len(s.Effective) == 0 {
			summary := "no explicit sizing; WSL's defaults apply (memory: 50% of host RAM)"
			if s.HostBytes > 0 && s.HostBytes <= smallHostBytes {
				r := result(c, Warn, fmt.Sprintf(
					"no explicit sizing on a %s host, so the VM may take ~%s of it",
					humanIEC(s.HostBytes), humanIEC(s.HostBytes/2)))
				r.Detail = []string{
					"  WSL2's default is half the host's RAM. That is fine on a large machine",
					"  and tight here: the engine plus a build can push this host into swap,",
					"  which looks like a slow laptop rather than an engine problem.",
				}
				r.Remedy = fmt.Sprintf("size it deliberately, then apply:\n"+
					"        hawser config set wsl.memory %dGB\n"+
					"        hawser config set wsl.processors 2\n"+
					"        hawser wsl-config apply\n"+
					"      ~/.wslconfig is shared by every WSL2 distro, so apply shows the diff first.",
					maxInt(2, int(s.HostBytes/(1<<30))/3))
				return r
			}
			return result(c, OK, summary)
		}

		r := result(c, OK, "sizing set: "+strings.Join(sizingSummary(s.Effective), ", "))
		r.Detail = detail
		return r
	}
	return c
}

// sizingSummary renders the set keys compactly for the one-line verdict.
func sizingSummary(eff map[string]string) []string {
	var out []string
	for _, k := range wslconfig.Managed() {
		if v, ok := eff[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// humanIEC renders bytes in binary units, matching how Windows reports RAM.
func humanIEC(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	if f >= 10 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
