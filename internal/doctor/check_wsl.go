package doctor

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/wslkit/skrog/internal/provision"
)

// checkWSL diagnoses the WSL2 platform Skrog stands on: present, a supported
// release, and defaulting to version 2. It reuses provision's preflight
// thresholds so "supported" means one thing across the codebase.
func checkWSL() Check {
	c := Check{Name: "wsl", Title: "WSL2 platform"}
	c.Run = func(f Facts) Result {
		if f.WSLErr != "" || !f.WSL.Installed {
			r := result(c, Fail, "WSL2 is not installed or not enabled")
			r.Remedy = "run `wsl --install --no-distribution` in an elevated prompt, reboot, " +
				"then run doctor again. If it still fails, enable the 'Virtual Machine " +
				"Platform' and 'Windows Subsystem for Linux' features and confirm " +
				"virtualization is on in firmware."
			if f.WSLErr != "" {
				r.Detail = []string{"querying WSL failed: " + f.WSLErr}
			}
			return r
		}

		if f.WSL.DefaultVersion == 1 {
			r := result(c, Warn, "the default WSL version is 1")
			r.Remedy = "run `skrog doctor --fix`, or `wsl --set-default-version 2`. " +
				"Skrog imports its own distro as WSL 2 regardless, but this avoids " +
				"surprises with your other distros."
			return r
		}

		if f.WSL.Version == "" {
			r := result(c, Warn, "cannot determine the WSL version (inbox WSL, no `wsl --version`)")
			r.Remedy = "install the Store version with `wsl --update` for mirrored " +
				"networking and DNS tunneling, which several VPN/proxy fixes rely on."
			return r
		}
		if provision.WSLTooOld(f.WSL.Version) {
			r := result(c, Warn, fmt.Sprintf("WSL %s is older than the tested minimum %s",
				f.WSL.Version, provision.MinWSLVersion))
			r.Remedy = "run `wsl --update` to get a supported release."
			return r
		}

		return result(c, OK, fmt.Sprintf("WSL %s, default version 2", f.WSL.Version))
	}

	// The one safely auto-remediable WSL issue: flipping the default to 2 needs
	// no elevation and cannot harm an existing distro (each keeps its own
	// version). Guarded so --fix is a no-op when the default is already 2.
	c.Fix = func(ctx context.Context, f Facts) (string, error) {
		if f.WSL.DefaultVersion != 1 {
			return "", nil
		}
		out, err := exec.CommandContext(ctx, "wsl", "--set-default-version", "2").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("wsl --set-default-version 2: %w: %s", err, out)
		}
		return "set the default WSL version to 2", nil
	}
	return c
}
