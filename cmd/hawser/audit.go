package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/provision"
)

func runAudit(args []string) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		n        = fs.Int("n", 0, "show only the last N events (0 = all)")
		since    = fs.Duration("since", 0, "show only events newer than this (e.g. 30m, 2h)")
		asJSON   = fs.Bool("json", false, "emit the events as one JSON array instead of JSON lines (tail), or the summary as JSON (trace)")
		raw      = fs.Bool("raw", false, "trace: print the matching records as JSON lines instead of a summary")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser audit tail [--since <dur>] [-n <count>] [--json]
       hawser audit trace [--json|--raw] -- <cmd> [args...]

trace runs a command and then summarizes what it did to the engine — images
pulled, containers created, execs, builds — from the audit records written
while it ran (a trace for opaque CI YAML: `+"`hawser audit trace -- act -j build`"+`).
The command's exit code is propagated. Concurrent docker use during the run is
included in the summary.

tail prints the container-affecting API audit log — image pulls, container
create/start/stop/remove, exec and builds that crossed the bridge — as the
JSON lines they are recorded in. Enable recording with:

  hawser config set audit on   (then `+"`hawser restart`"+`)

The record is derived from the request line only, never the body, so no
credentials or payloads are written.

Exit codes: 0 ok, %d error, %d usage.

flags:
`, exitError, exitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	switch {
	case len(rest) >= 1 && rest[0] == "trace":
		return runAuditTrace(opts.StateDir, rest[1:], *asJSON, *raw)
	case len(rest) == 1 && rest[0] == "tail":
		// falls through to the tail below
	default:
		fs.Usage()
		return exitUsage
	}
	path := filepath.Join(opts.StateDir, "audit.log")

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		on, _ := config.Get(opts.StateDir, config.KeyAudit)
		if on != "on" {
			fmt.Fprintln(os.Stderr, "audit is off; enable it with `hawser config set audit on` then `hawser restart`")
		} else {
			fmt.Fprintln(os.Stderr, "audit is on but no events recorded yet")
		}
		if *asJSON {
			// No log yet is an empty answer, not an error.
			return emitJSON([]json.RawMessage{})
		}
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	defer f.Close()

	var cutoff time.Time
	if *since > 0 {
		cutoff = time.Now().Add(-*since)
	}

	// Collect matching lines; keep only the last N if asked. A ring buffer would
	// save memory, but an audit log is small and this keeps the code obvious.
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !cutoff.IsZero() && !newerThan(line, cutoff) {
			continue
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: reading audit log: %v\n", err)
		return exitError
	}

	if *n > 0 && len(lines) > *n {
		lines = lines[len(lines)-*n:]
	}
	if *asJSON {
		// One document instead of JSON lines, for consumers that want a single
		// parse. Records pass through exactly as recorded (audit.Event), never
		// re-shaped; a corrupt line fails the encode loudly rather than being
		// silently dropped.
		recs := make([]json.RawMessage, 0, len(lines))
		for _, line := range lines {
			recs = append(recs, json.RawMessage(line))
		}
		return emitJSON(recs)
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return exitOK
}

// newerThan reports whether an audit line's timestamp is at or after cutoff. An
// unparseable line is kept (better to over-report than silently drop).
func newerThan(line string, cutoff time.Time) bool {
	var e struct {
		Time string `json:"time"`
	}
	if json.Unmarshal([]byte(line), &e) != nil {
		return true
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", e.Time)
	if err != nil {
		return true
	}
	return !t.Before(cutoff)
}
