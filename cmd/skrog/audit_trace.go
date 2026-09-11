package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wslkit/skrog/internal/audit"
	"github.com/wslkit/skrog/internal/config"
)

// runAuditTrace is `skrog audit trace -- <cmd> [args]` (#152): run a command
// and report what it did to the engine — images pulled, containers created,
// execs, builds — from the audit records appended while it ran. It is the
// trace for opaque CI YAML (`act`, `gitlab-ci-local`) and for "what did that
// script just do".
//
// Attribution is by position in the log: everything appended after the command
// started. That is exact for the one-user case and honest about the other:
// concurrent docker use during the run is included, and the doc says so.
// The traced command's exit code is propagated, so `audit trace -- make test`
// still fails the way `make test` would.
func runAuditTrace(stateDir string, cmdArgs []string, asJSON, raw bool) int {
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "skrog: audit trace needs a command: skrog audit trace -- <cmd> [args]")
		return exitUsage
	}
	// Still a refusal rather than turning it on automatically: recording every
	// container-affecting call is the user's decision to make, and a trace
	// that silently switched on an audit log and left it on would be a
	// surprise. The recipe is now one command, and needs no restart (#202).
	if on, _ := config.Get(stateDir, config.KeyAudit); on != "on" {
		fmt.Fprintln(os.Stderr, "skrog: audit is off, so there is nothing to trace. Turn it on first:\n"+
			"  skrog config set audit on\n"+
			"It applies immediately; then run the trace again.")
		return exitError
	}

	logPath := filepath.Join(stateDir, "audit.log")
	offset := logSize(logPath)

	began := time.Now()
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	elapsed := time.Since(began)

	code := exitOK
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			fmt.Fprintf(os.Stderr, "skrog: could not run %q: %v\n", cmdArgs[0], runErr)
			return exitError
		}
		if code = ee.ExitCode(); code < 0 {
			code = exitError // killed by a signal: no code to propagate
		}
	}

	events, note, err := eventsSince(logPath, offset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skrog: reading audit log: %v\n", err)
		return exitError
	}

	if raw {
		for _, e := range events {
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
		}
		return code
	}
	sum := summarizeTrace(cmdArgs, code, elapsed, events)
	sum.Note = note
	if asJSON {
		if c := emitJSON(sum); c != exitOK {
			return c
		}
		return code
	}
	printTrace(sum)
	return code
}

func logSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// eventsSince parses the audit records appended after offset. A missing log
// means nothing was recorded (most likely the supervisor is not running with
// audit on). If the log shrank — rotated mid-run — the whole current file is
// used and the note says so, rather than silently reporting nothing.
func eventsSince(path string, offset int64) ([]audit.Event, string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "no audit records were written (is the supervisor running with audit on?)", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	note := ""
	if st, err := f.Stat(); err == nil && st.Size() < offset {
		offset, note = 0, "audit log rotated during the run; the summary covers the current file"
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, "", err
	}

	var out []audit.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e audit.Event
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, note, sc.Err()
}

// summarizeTrace folds events into the trace report: per-action counts and the
// distinct images and containers touched. Containers are the names given at
// create, or the id/name as it appeared in the request path for later actions.
// Pure, so it is unit-tested without a log.
func summarizeTrace(cmdArgs []string, code int, elapsed time.Duration, events []audit.Event) traceJSON {
	s := traceJSON{
		Command:    cmdArgs,
		ExitCode:   code,
		Millis:     elapsed.Milliseconds(),
		Events:     len(events),
		Actions:    map[string]int{},
		Images:     []string{},
		Containers: []string{},
	}
	images, containers := map[string]bool{}, map[string]bool{}
	for _, e := range events {
		s.Actions[e.Action]++
		if e.Image != "" {
			images[e.Image] = true
		}
		switch {
		case e.Name != "":
			containers[e.Name] = true
		case e.Container != "" && strings.HasPrefix(e.Action, "container-"):
			containers[e.Container] = true
		}
	}
	for k := range images {
		s.Images = append(s.Images, k)
	}
	for k := range containers {
		s.Containers = append(s.Containers, k)
	}
	sort.Strings(s.Images)
	sort.Strings(s.Containers)
	return s
}

func printTrace(s traceJSON) {
	took := (time.Duration(s.Millis) * time.Millisecond).Round(time.Millisecond)
	fmt.Printf("\n--- skrog audit trace: %s (exit %d, %s) ---\n", strings.Join(s.Command, " "), s.ExitCode, took)
	if s.Events == 0 {
		fmt.Println("no container-affecting API calls recorded")
		if s.Note != "" {
			fmt.Println("  note: " + s.Note)
		}
		return
	}
	keys := make([]string, 0, len(s.Actions))
	for k := range s.Actions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-18s %d\n", k, s.Actions[k])
	}
	if len(s.Images) > 0 {
		fmt.Printf("  images:     %s\n", strings.Join(s.Images, ", "))
	}
	if len(s.Containers) > 0 {
		fmt.Printf("  containers: %s\n", strings.Join(s.Containers, ", "))
	}
	if s.Note != "" {
		fmt.Println("  note: " + s.Note)
	}
}
