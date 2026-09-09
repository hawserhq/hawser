package doctor

// checkGPU reports on NVIDIA GPU passthrough (#83). The states worth
// distinguishing: not set up at all, set up and working, and the two ways it
// silently breaks — enabled but the spec is gone (a reinstall the engine has not
// re-applied yet), or enabled but the GPU is no longer visible (a driver/WSL
// regression). When the engine is down, doctor reports the config intent but
// cannot verify the distro (probing would boot it, which doctor must not do).
func checkGPU() Check {
	c := Check{Name: "gpu", Title: "NVIDIA GPU passthrough"}
	c.Run = func(f Facts) Result {
		g := f.GPU
		if !g.EngineInstalled {
			return result(c, Skip, "no engine installed")
		}

		if !g.ConfigEnabled {
			// Not enabled. If we can see the GPU is present, nudge; otherwise stay quiet.
			if g.Probed && g.Visible {
				r := result(c, Skip, "an NVIDIA GPU is visible but passthrough is off")
				r.Remedy = "run `hawser enable-gpu` to let containers use it " +
					"(`docker run --device nvidia.com/gpu=all ...`)."
				return r
			}
			return result(c, Skip, "GPU passthrough is off")
		}

		// Enabled. Verify against the running engine when we could probe it.
		if !g.Probed {
			r := result(c, OK, "GPU passthrough is enabled (engine down; not verified)")
			r.Detail = []string{"  start the engine and re-run doctor to verify the CDI spec"}
			return r
		}
		switch {
		case g.Visible && g.SpecInstalled:
			return result(c, OK, "GPU passthrough enabled and the CDI spec is installed")
		case !g.Visible:
			r := result(c, Warn, "GPU passthrough is on but no GPU is visible in the distro")
			r.Detail = []string{"  the NVIDIA driver or WSL may have changed"}
			r.Remedy = "check the Windows NVIDIA driver and run `wsl --update`; " +
				"`hawser enable-gpu --off` if this machine no longer has the GPU."
			return r
		default: // visible but spec missing
			r := result(c, Warn, "GPU passthrough is on but the CDI spec is missing from the engine")
			r.Remedy = "run `hawser restart` (re-installs the spec), or `hawser enable-gpu` again."
			return r
		}
	}
	return c
}
