package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hawserhq/hawser/internal/config"
)

// hookTimeout bounds a lifecycle hook: long enough for a registry login or a
// compose bring-up, short enough that a wedged script cannot pile up.
const hookTimeout = 2 * time.Minute

// hookRunner returns the supervisor's Hook callback: on a lifecycle event it
// looks up the configured script and runs it off-thread, time-bounded, with
// output to the supervisor log. It returns immediately so it never blocks the
// reconciler, and a failing or missing hook is logged but never propagated —
// hooks observe the lifecycle, they do not gate it (#70).
func hookRunner(stateDir string, log *slog.Logger) func(event string) {
	return func(event string) {
		path, err := config.Get(stateDir, "hook."+event)
		if err != nil || strings.TrimSpace(path) == "" {
			return
		}
		go runHook(stateDir, event, path, log)
	}
}

func runHook(stateDir, event, path string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	log.Info("running lifecycle hook", "event", event, "path", path)
	cmd := hookCommand(ctx, path)
	// The hook learns which event fired and where Hawser keeps its state, so one
	// script can serve several events and reach `hawser` if it wants to.
	cmd.Env = append(os.Environ(),
		"HAWSER_EVENT="+event,
		"HAWSER_STATE_DIR="+stateDir,
	)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		log.Warn("lifecycle hook failed", "event", event, "error", err, "output", trimmed)
		return
	}
	log.Info("lifecycle hook finished", "event", event, "output", trimmed)
}

// hookCommand builds the command for a hook path, dispatching by extension so a
// PowerShell or batch script is runnable directly, not only a bare .exe.
func hookCommand(ctx context.Context, path string) *exec.Cmd {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ps1":
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path)
	case ".cmd", ".bat":
		return exec.CommandContext(ctx, "cmd", "/c", path)
	default:
		return exec.CommandContext(ctx, path)
	}
}
