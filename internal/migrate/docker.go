package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DockerCLI drives one engine through the docker CLI. The source is addressed
// by context (Docker Desktop is "desktop-linux"); the destination by Host, the
// engine's own DOCKER_HOST pipe — not a shared context name, which may be
// absent or point elsewhere. It is both an Engine and, paired with a
// destination, half of a Transfer.
type DockerCLI struct {
	// Exe is the docker binary; empty means "docker" on PATH.
	Exe string
	// Context selects a docker context (docker --context <name>).
	Context string
	// Host selects an engine directly (docker -H <host>), e.g. the Skrog
	// pipe. Takes precedence over Context when set.
	Host string

	// run overrides command execution; nil runs the real docker CLI. It
	// receives the fully-resolved argv (after Host/Context prefixing). The seam
	// exists so the JSON parsing and query logic are testable without docker.
	run func(ctx context.Context, argv []string) (string, error)
}

func (d DockerCLI) exe() string {
	if d.Exe != "" {
		return d.Exe
	}
	return "docker"
}

func (d DockerCLI) args(rest ...string) []string {
	var a []string
	switch {
	case d.Host != "":
		a = append(a, "-H", d.Host)
	case d.Context != "":
		a = append(a, "--context", d.Context)
	}
	return append(a, rest...)
}

func (d DockerCLI) output(ctx context.Context, rest ...string) (string, error) {
	argv := d.args(rest...)
	if d.run != nil {
		return d.run(ctx, argv)
	}
	cmd := exec.CommandContext(ctx, d.exe(), argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("docker %s: %s", strings.Join(rest, " "), msg)
	}
	return stdout.String(), nil
}

// dockerImage is the subset of `docker images --format json` we use.
type dockerImage struct {
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	ID         string `json:"ID"`
	Size       string `json:"Size"` // human string ("1.2GB"); VirtualSize is bytes but not always present
}

// Images lists tagged images. Each repo:tag is one Image; the CLI already
// emits one row per tag, so a multi-tag image appears once per tag, which is
// what save wants.
func (d DockerCLI) Images(ctx context.Context) ([]Image, error) {
	out, err := d.output(ctx, "images", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var images []Image
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var di dockerImage
		if err := json.Unmarshal([]byte(line), &di); err != nil {
			return nil, fmt.Errorf("parsing image row: %w", err)
		}
		ref := di.Repository + ":" + di.Tag
		if di.Repository == "<none>" || di.Tag == "<none>" {
			ref = "" // dangling; the planner counts and skips it
		}
		images = append(images, Image{Ref: ref, ID: di.ID, Size: parseHumanSize(di.Size)})
	}
	return images, nil
}

// Volumes lists named volumes with sizes. `docker system df -v` reports
// per-volume size; `volume ls` alone does not.
func (d DockerCLI) Volumes(ctx context.Context) ([]Volume, error) {
	out, err := d.output(ctx, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	var vols []Volume
	sizes := d.volumeSizes(ctx) // best-effort; missing sizes become -1
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name == "" {
			continue
		}
		sz, ok := sizes[name]
		if !ok {
			sz = -1
		}
		vols = append(vols, Volume{Name: name, Size: sz})
	}
	return vols, nil
}

// volumeSizes maps volume name -> bytes via `system df -v`. Best-effort: any
// failure yields an empty map and volumes size as "unknown".
func (d DockerCLI) volumeSizes(ctx context.Context) map[string]int64 {
	out, err := d.output(ctx, "system", "df", "-v", "--format", "{{json .}}")
	if err != nil {
		return nil
	}
	var df struct {
		Volumes []struct {
			Name string `json:"Name"`
			Size string `json:"Size"`
		} `json:"Volumes"`
	}
	// system df emits one JSON object; some versions emit it across the whole
	// output, so decode the first object found.
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&df); err != nil {
		return nil
	}
	sizes := make(map[string]int64, len(df.Volumes))
	for _, v := range df.Volumes {
		sizes[v.Name] = parseHumanSize(v.Size)
	}
	return sizes
}

// HasImage / HasVolume answer for the destination (the receiver is the
// destination CLI).
func (d DockerCLI) HasImage(ctx context.Context, ref string) (bool, error) {
	// inspect exits non-zero when absent; distinguish "absent" from "engine
	// unreachable" by checking the message.
	_, err := d.output(ctx, "image", "inspect", ref)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "No such image") || strings.Contains(err.Error(), "no such image") {
		return false, nil
	}
	return false, err
}

func (d DockerCLI) HasVolume(ctx context.Context, name string) (bool, error) {
	_, err := d.output(ctx, "volume", "inspect", name)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "no such volume") || strings.Contains(err.Error(), "No such volume") {
		return false, nil
	}
	return false, err
}

func parseHumanSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return -1
	}
	// Sizes look like "1.2GB", "533MB", "0B". Strip an optional trailing "B".
	s = strings.TrimSuffix(s, "B")
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "T"):
		mult, s = 1<<40, strings.TrimSuffix(s, "T")
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return int64(f * float64(mult))
}
