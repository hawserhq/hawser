package doctor

import "fmt"

// Disk thresholds: the engine's VHDX grows on demand, and image pulls and
// builds fail ugly when the volume runs out. Warn early, fail before docker
// starts erroring on its own.
const (
	diskFailBytes = 2 << 30 // 2 GiB
	diskWarnBytes = 5 << 30 // 5 GiB
)

// checkDisk reports free space on the volume that holds the engine's data. Low
// free space there surfaces as opaque pull/build failures deep inside dockerd,
// so naming it up front saves a long hunt.
func checkDisk() Check {
	c := Check{Name: "disk", Title: "disk space for engine data"}
	c.Run = func(f Facts) Result {
		d := f.Disk
		if d.Err != "" {
			r := result(c, Skip, "could not determine free disk space")
			r.Detail = []string{"  " + d.Err}
			return r
		}

		detail := []string{fmt.Sprintf("  %s free of %s on %s",
			humanBytes(d.FreeBytes), humanBytes(d.TotalBytes), d.Path)}

		switch {
		case d.FreeBytes < diskFailBytes:
			r := result(c, Fail, fmt.Sprintf("only %s free for engine data", humanBytes(d.FreeBytes)))
			r.Detail = detail
			r.Remedy = "free space on that volume, or move the engine data with " +
				"`--data-dir` on install; pulls and builds fail when it runs out."
			return r
		case d.FreeBytes < diskWarnBytes:
			r := result(c, Warn, fmt.Sprintf("%s free for engine data (getting low)", humanBytes(d.FreeBytes)))
			r.Detail = detail
			r.Remedy = "consider freeing space; large images or builds may exhaust it."
			return r
		default:
			return result(c, OK, fmt.Sprintf("%s free for engine data", humanBytes(d.FreeBytes)))
		}
	}
	return c
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
