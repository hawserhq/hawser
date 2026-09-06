package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// consoleHandler renders slog records as short human lines.
//
// The default text handler emits logfmt, which is right for a service and wrong
// for someone watching an install: "time=... level=INFO msg=importing distro
// distro=hawser-engine" buries the sentence in metadata. This prints the
// message, then only the attributes that add something.
type consoleHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	// attrs are carried from WithAttrs so log.With(...) context is not lost
	// (#93): the old no-op silently dropped it. Groups are flattened into a
	// key prefix, which is all this line-oriented handler needs.
	attrs  []slog.Attr
	prefix string
}

func newConsoleHandler(w io.Writer, level slog.Level) slog.Handler {
	return &consoleHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	switch {
	case r.Level >= slog.LevelError:
		b.WriteString("error: ")
	case r.Level >= slog.LevelWarn:
		b.WriteString("warning: ")
	default:
		b.WriteString("  ")
	}
	b.WriteString(r.Message)

	// Handler-level attrs (from log.With) render alongside the record's own.
	var fix slog.Value
	haveFix := false
	emit := func(a slog.Attr) {
		if a.Key == "fix" { // remedy goes on its own line, below
			fix, haveFix = a.Value, true
			return
		}
		fmt.Fprintf(&b, " %s%s=%v", h.prefix, a.Key, a.Value)
	}
	for _, a := range h.attrs {
		emit(a)
	}
	r.Attrs(func(a slog.Attr) bool { emit(a); return true })
	b.WriteString("\n")

	if haveFix {
		fmt.Fprintf(&b, "    fix: %v\n", fix)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &nh
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.prefix = h.prefix + name + "."
	return &nh
}

// emitJSON writes v to stdout for scripting, and is the only place --json
// output is produced so the shape stays consistent across commands.
func emitJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	return exitOK
}

// interruptCtx is a plain background context for short-lived setup calls that
// should not be cancelled by the Ctrl-C that stops the long-running server.
func interruptCtx() context.Context { return context.Background() }
