package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/wslkit/skrog/internal/dockerctx"
	"github.com/wslkit/skrog/internal/prewarm"
)

// runPrewarm is `skrog prewarm <images.txt>` (#149): pull a pinned image list
// ahead of need — a golden-image bake, a post-start hook, a runner warm-up.
// Pulls go through the docker CLI to whatever docker currently targets (the
// local engine, or a `skrog remote`), so they inherit that engine's proxy and
// CA configuration like any other pull.
func runPrewarm(args []string) int {
	fs := flag.NewFlagSet("prewarm", flag.ContinueOnError)
	var (
		concurrency = fs.Int("concurrency", 3, "how many pulls run at once")
		asJSON      = fs.Bool("json", false, "emit machine-readable JSON")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: skrog prewarm [--concurrency <n>] [--json] <images.txt>

Pulls every image listed in the file — one reference per line, # comments and
blank lines ignored, digest pins encouraged — with a bounded number in flight.
A failed pull never stops the others; the exit code says whether all succeeded.

  skrog healthcheck --wait 2m && skrog prewarm images.txt

Pulls target whatever docker currently targets: the local engine, or the remote
selected with `+"`skrog remote use`"+`.

Exit codes: 0 all pulled, %d some failed, %d usage.

flags:
`, exitError, exitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}

	f, err := os.Open(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
		return exitError
	}
	refs, err := prewarm.ParseList(f)
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
		return exitUsage
	}
	if len(refs) == 0 {
		fmt.Fprintf(os.Stderr, "skrog: %s lists no images\n", rest[0])
		return exitUsage
	}

	ctx, stop := interruptible()
	defer stop()

	if err := (&dockerctx.Manager{}).Available(ctx); err != nil {
		var noCLI *dockerctx.ErrNoDockerCLI
		if errors.As(err, &noCLI) {
			fmt.Fprintf(os.Stderr, "skrog: %v\n  (`skrog cli install` provides one)\n", err)
			return exitError
		}
	}

	res := prewarm.Run(ctx, refs, *concurrency, prewarm.DockerPuller{})

	report := prewarmJSON{
		File: rest[0], Concurrency: *concurrency,
		Pulled: res.Pulled, Failed: res.Failed, Millis: res.Millis,
		Images: []prewarmImageJSON{},
	}
	for _, img := range res.Images {
		report.Images = append(report.Images, prewarmImageJSON{
			Ref: img.Ref, OK: img.OK, Millis: img.Millis, Error: img.Err,
		})
	}
	code := exitOK
	if res.Failed > 0 {
		code = exitError
	}

	if *asJSON {
		if c := emitJSON(report); c != exitOK {
			return c
		}
		return code
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "IMAGE\tRESULT\tTIME")
	for _, img := range report.Images {
		result := "pulled"
		if !img.OK {
			result = "FAILED: " + img.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", img.Ref, result, (time.Duration(img.Millis) * time.Millisecond).Round(time.Millisecond))
	}
	tw.Flush()
	fmt.Printf("\n%d pulled, %d failed in %s\n", res.Pulled, res.Failed,
		(time.Duration(res.Millis) * time.Millisecond).Round(time.Millisecond))
	return code
}
