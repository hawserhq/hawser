// Package prewarm pulls a pinned list of images ahead of need (#149): in a
// golden-image bake, a post-start hook, or a runner warm-up step, so the first
// job does not pay for the pulls. It drives the docker CLI, so pulls go to
// whatever docker currently targets — the local engine or a `hawser remote` —
// and happen inside that engine, inheriting its proxy and CA configuration like
// any other pull.
package prewarm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Puller pulls one image. The seam keeps Run testable without a docker binary.
type Puller interface {
	Pull(ctx context.Context, ref string) error
}

// DockerPuller shells out to `docker pull`. Exe empty means docker on PATH.
type DockerPuller struct {
	Exe string
}

func (d DockerPuller) exe() string {
	if d.Exe != "" {
		return d.Exe
	}
	return "docker"
}

// Pull runs `docker pull --quiet <ref>`; on failure the error is the CLI's own
// last line (the diagnosis), not the exit status.
func (d DockerPuller) Pull(ctx context.Context, ref string) error {
	out, err := exec.CommandContext(ctx, d.exe(), "pull", "--quiet", ref).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	lines := strings.Split(msg, "\n")
	return fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
}

// ParseList reads an image list: one reference per line, blank lines and
// `#` comments ignored (a trailing `# comment` after a reference too),
// duplicates dropped keeping first position. Digest-pinned references are
// encouraged but not required.
func ParseList(r io.Reader) ([]string, error) {
	var refs []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) > 1 {
			return nil, fmt.Errorf("image list: one reference per line, got %q", strings.TrimSpace(line))
		}
		ref := fields[0]
		if strings.HasPrefix(ref, "-") {
			return nil, fmt.Errorf("image list: %q is not an image reference", ref)
		}
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs, sc.Err()
}

// Image is one pull's outcome.
type Image struct {
	Ref    string
	OK     bool
	Millis int64
	Err    string
}

// Result is a Run's outcome, images in list order.
type Result struct {
	Images []Image
	Pulled int
	Failed int
	Millis int64
}

// Run pulls every reference with at most concurrency in flight (<= 0 means 3),
// and reports each outcome in list order. A failed pull never stops the others:
// a warm-up wants as many images present as it can get, and the caller decides
// what a failure means (the CLI exits non-zero if any failed).
func Run(ctx context.Context, refs []string, concurrency int, p Puller) Result {
	if concurrency <= 0 {
		concurrency = 3
	}
	started := time.Now()
	res := Result{Images: make([]Image, len(refs))}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			err := p.Pull(ctx, ref)
			img := Image{Ref: ref, OK: err == nil, Millis: time.Since(t0).Milliseconds()}
			if err != nil {
				img.Err = err.Error()
			}
			res.Images[i] = img
		}(i, ref)
	}
	wg.Wait()

	for _, img := range res.Images {
		if img.OK {
			res.Pulled++
		} else {
			res.Failed++
		}
	}
	res.Millis = time.Since(started).Milliseconds()
	return res
}
