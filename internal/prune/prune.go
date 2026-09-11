// Package prune reclaims disk on the engine (#145): stopped containers, unused
// images, optionally volumes and the BuildKit cache. It drives the docker CLI,
// so it acts on whatever docker currently targets — the local engine or a
// `skrog remote` — and needs nothing installed in the distro. Runners die of
// full disks; this is the lever a post-job step or a scheduled task pulls.
package prune

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes the docker CLI. The seam keeps Run testable without docker.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// DockerRunner shells out to docker. Exe empty means docker on PATH.
type DockerRunner struct {
	Exe string
}

// Run returns docker's combined output, trimmed; on failure the error carries
// the CLI's last line (the diagnosis).
func (d DockerRunner) Run(ctx context.Context, args ...string) (string, error) {
	exe := d.Exe
	if exe == "" {
		exe = "docker"
	}
	out, err := exec.CommandContext(ctx, exe, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			lines := strings.Split(text, "\n")
			return text, fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
		}
		return text, err
	}
	return text, nil
}

// Options select what to prune. Volumes are off by default because they hold
// data; everything else is rebuildable from a registry or a Dockerfile.
type Options struct {
	// All removes every unused image, not only dangling (untagged) layers.
	All bool
	// Until keeps anything newer than this age (0 = any age).
	Until time.Duration
	// BuildCache also prunes BuildKit's cache.
	BuildCache bool
	// Volumes also prunes unused volumes.
	Volumes bool
}

// Step is one docker prune invocation and its outcome.
type Step struct {
	Name           string
	Args           []string
	ReclaimedBytes int64
	Output         string
	Err            string
}

// Result is a Run's outcome.
type Result struct {
	Steps          []Step
	ReclaimedBytes int64
	Failed         int
}

// Plan lists the docker invocations for the options, in the order that frees
// the most: containers first (so their images become unused), then images.
func Plan(o Options) []Step {
	until := func(args []string) []string {
		if o.Until > 0 {
			return append(args, "--filter", "until="+durationArg(o.Until))
		}
		return args
	}
	steps := []Step{{Name: "containers", Args: until([]string{"container", "prune", "-f"})}}
	img := []string{"image", "prune", "-f"}
	if o.All {
		img = append(img, "-a")
	}
	steps = append(steps, Step{Name: "images", Args: until(img)})
	if o.Volumes {
		// volume prune has no until filter; it only ever removes unused volumes.
		steps = append(steps, Step{Name: "volumes", Args: []string{"volume", "prune", "-f"}})
	}
	if o.BuildCache {
		steps = append(steps, Step{Name: "build-cache", Args: until([]string{"builder", "prune", "-f"})})
	}
	return steps
}

// durationArg renders a duration the way docker's until filter reads it
// ("168h", "90m"), without Go's trailing zero components.
func durationArg(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	default:
		return d.String()
	}
}

// Run executes the plan. A failed step never stops the others — a runner wants
// whatever space it can get back — and the caller decides what a failure means.
func Run(ctx context.Context, r Runner, o Options) Result {
	var res Result
	for _, st := range Plan(o) {
		out, err := r.Run(ctx, st.Args...)
		st.Output = out
		if err != nil {
			st.Err = err.Error()
			res.Failed++
		} else {
			st.ReclaimedBytes = ParseReclaimed(out)
			res.ReclaimedBytes += st.ReclaimedBytes
		}
		res.Steps = append(res.Steps, st)
	}
	return res
}

// ParseReclaimed finds docker's "Total reclaimed space: 1.2GB" line (or
// builder prune's "Total:  1.2GB") and returns the bytes; 0 when absent.
func ParseReclaimed(out string) int64 {
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		var rest string
		switch {
		case strings.HasPrefix(l, "Total reclaimed space:"):
			rest = strings.TrimPrefix(l, "Total reclaimed space:")
		case strings.HasPrefix(l, "Total:"):
			rest = strings.TrimPrefix(l, "Total:")
		default:
			continue
		}
		if n, err := ParseSize(strings.TrimSpace(rest)); err == nil {
			return n
		}
	}
	return 0
}

var sizeUnits = map[string]float64{
	"": 1, "b": 1,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// ParseSize parses a human size: docker's decimal spellings ("1.234GB",
// "512kB", "0B" — kB is 1000) and binary ones ("2GiB").
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	if num == "" {
		return 0, fmt.Errorf("size %q: no number", s)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	mult, ok := sizeUnits[unit]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit %q", s, unit)
	}
	return int64(math.Round(f * mult)), nil
}
