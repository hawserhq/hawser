package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestConsoleHandlerCarriesWithAttrs(t *testing.T) {
	// #93: WithAttrs used to be a no-op, silently dropping log.With context.
	var buf bytes.Buffer
	log := slog.New(newConsoleHandler(&buf, slog.LevelInfo)).With("distro", "hawser-engine")
	log.Info("engine recovered")
	out := buf.String()
	if !strings.Contains(out, "engine recovered") || !strings.Contains(out, "distro=hawser-engine") {
		t.Errorf("With attrs dropped: %q", out)
	}
}

func TestConsoleHandlerWithGroupPrefixesKeys(t *testing.T) {
	var buf bytes.Buffer
	h := newConsoleHandler(&buf, slog.LevelInfo)
	log := slog.New(h).WithGroup("net")
	log.Info("dial", "host", "engine")
	if !strings.Contains(buf.String(), "net.host=engine") {
		t.Errorf("group prefix missing: %q", buf.String())
	}
}

func TestConsoleHandlerFixOnItsOwnLine(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newConsoleHandler(&buf, slog.LevelWarn)).Warn("cannot start", "fix", "run hawser install")
	out := buf.String()
	if !strings.Contains(out, "warning: cannot start") || !strings.Contains(out, "\n    fix: run hawser install") {
		t.Errorf("fix formatting: %q", out)
	}
	_ = context.Background
}
