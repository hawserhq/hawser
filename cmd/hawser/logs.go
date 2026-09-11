package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hawserhq/hawser/internal/provision"
)

// runLogs is `hawser logs` (#146): the supervisor, dockerd, or audit log, with
// --follow, and --json for shippers (Loki, Splunk, ELK) — one object per line,
// {"source","line"}, uniform across the three sources so a shipper needs one
// pipeline, not three.
func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		source   = fs.String("source", "supervisor", "which log: supervisor, dockerd, or audit")
		n        = fs.Int("n", 200, "print the last N lines first (0 = all)")
		follow   = fs.Bool("follow", false, "keep printing new lines as they arrive (Ctrl-C to stop)")
		asJSON   = fs.Bool("json", false, `one JSON object per line, {"source","line"} — for log shippers`)
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser logs [--source supervisor|dockerd|audit] [-n <lines>] [--follow] [--json]

  supervisor   the always-on bridge: engine starts/stops, recovery, idle stops
  dockerd      the engine daemon's own log, read from inside the distro
  audit        the container-affecting API record (needs `+"`config set audit on`"+`)

--follow survives log rotation (the supervisor and audit logs rotate at 5 MB).

Exit codes: 0 ok, %d error, %d usage, %d not installed (dockerd).

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	ctx, stop := interruptible()
	defer stop()
	out := lineEmitter{source: *source, asJSON: *asJSON}

	switch *source {
	case "supervisor":
		return tailFile(ctx, filepath.Join(opts.StateDir, "supervisor.log"), *n, *follow, out)
	case "audit":
		return tailFile(ctx, filepath.Join(opts.StateDir, "audit.log"), *n, *follow, out)
	case "dockerd":
		return tailDockerd(ctx, opts, *n, *follow, out)
	default:
		fmt.Fprintf(os.Stderr, "hawser: unknown --source %q (supervisor, dockerd, audit)\n", *source)
		return exitUsage
	}
}

// lineEmitter prints one log line, plain or as the JSON object shippers ingest.
type lineEmitter struct {
	source string
	asJSON bool
}

func (e lineEmitter) emit(line string) { fmt.Println(formatLine(e.source, line, e.asJSON)) }

// formatLine is the pure half of emit: the plain line, or logLineJSON.
func formatLine(source, line string, asJSON bool) string {
	if !asJSON {
		return line
	}
	b, _ := json.Marshal(logLineJSON{Source: source, Line: line})
	return string(b)
}

// tailFile prints the last n lines of path, then — with follow — new complete
// lines as they are appended. Rotation (the writer renames the file and starts a
// fresh one) shows up as a shrink; the follower restarts from the top of the new
// file. Only complete lines are emitted, so a line caught mid-write is never
// split in two.
func tailFile(ctx context.Context, path string, n int, follow bool, out lineEmitter) int {
	lines, offset, err := lastLines(path, n)
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(os.Stderr, "hawser: no %s log yet at %s\n", out.source, path)
		if !follow {
			return exitOK
		}
		offset = 0
	case err != nil:
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	for _, l := range lines {
		out.emit(l)
	}
	if !follow {
		return exitOK
	}

	for {
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(250 * time.Millisecond):
		}
		st, err := os.Stat(path)
		if err != nil {
			continue // between a rotation and the new file's creation
		}
		if st.Size() < offset {
			offset = 0 // rotated: the file at path is a fresh one
		}
		if st.Size() == offset {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err == nil {
			r := bufio.NewReader(f)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					break // EOF, or a partial line: wait for the rest
				}
				offset += int64(len(line))
				out.emit(strings.TrimRight(line, "\r\n"))
			}
		}
		f.Close()
	}
}

// lastLines returns the last n lines of the file (all when n <= 0) and its
// size, which is where a follower should continue from. Logs here are capped at
// 5 MB by rotation, so reading the whole file is fine.
func lastLines(path string, n int) ([]string, int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	text := strings.TrimRight(string(b), "\r\n")
	var lines []string
	if text != "" {
		lines = strings.Split(text, "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], "\r")
		}
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, int64(len(b)), nil
}

// tailDockerd streams the engine's own log from inside the distro through
// `tail` — the daemon writes it there, and the distro is the only place it
// exists. Reading is a wsl exec, so it does start a stopped distro; that is
// acceptable for a log request, unlike for status (#82).
func tailDockerd(ctx context.Context, opts provision.Options, n int, follow bool, out lineEmitter) int {
	p := &provision.Provisioner{Logger: cliLogger(true)}
	distro, ok := resolveDistro(p, opts)
	if !ok {
		fmt.Fprintln(os.Stderr, "hawser: no install found. Run `hawser install` first.")
		return exitNotFound
	}
	tailArgs := []string{"-n"}
	if n > 0 {
		tailArgs = append(tailArgs, strconv.Itoa(n))
	} else {
		tailArgs = append(tailArgs, "+1")
	}
	if follow {
		tailArgs = append(tailArgs, "-f")
	}
	args := append([]string{"-d", distro, "-u", "root", "tail"}, tailArgs...)
	args = append(args, "/var/log/dockerd.log")

	cmd := exec.CommandContext(ctx, "wsl.exe", args...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: reading dockerd log: %v\n", err)
		return exitError
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		// wsl.exe can hand back UTF-16 output; NUL bytes are its fingerprint.
		out.emit(strings.ReplaceAll(sc.Text(), "\x00", ""))
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "hawser: reading dockerd log: %v\n", err)
		return exitError
	}
	return exitOK
}
