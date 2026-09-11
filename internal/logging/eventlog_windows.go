//go:build windows

package logging

import (
	"context"
	"log/slog"

	"golang.org/x/sys/windows/svc/eventlog"
)

// EventLogHandler mirrors Warn-and-above records to the Windows Event Log,
// wrapping another handler that receives everything.
//
// Only warnings and errors: the Event Log is where an admin looks when
// something is wrong, and routine health-loop chatter there would train them
// to ignore the source. The rotating file keeps the full story.
//
// Registration caveat, stated rather than hidden: writing under an
// unregistered source works, but Event Viewer prefixes entries with "the
// description for Event ID ... cannot be found" before showing the message.
// Proper source registration writes HKLM and so needs elevation; install does
// it best-effort when elevated and skips silently when not.
type EventLogHandler struct {
	inner slog.Handler
	log   *eventlog.Log
}

// NewEventLogHandler wraps inner. When the Event Log cannot be opened at all,
// inner is returned unwrapped: losing the mirror must not cost the file log.
func NewEventLogHandler(inner slog.Handler, source string) slog.Handler {
	l, err := eventlog.Open(source)
	if err != nil {
		return inner
	}
	return &EventLogHandler{inner: inner, log: l}
}

// EventSource is the name entries appear under.
const EventSource = "Skrog"

// RegisterEventSource creates the HKLM registration so Event Viewer renders
// entries cleanly. Needs elevation; callers treat failure as cosmetic.
func RegisterEventSource() error {
	return eventlog.InstallAsEventCreate(EventSource,
		eventlog.Error|eventlog.Warning|eventlog.Info)
}

// UnregisterEventSource removes the registration; same elevation caveat.
func UnregisterEventSource() error {
	return eventlog.Remove(EventSource)
}

func (h *EventLogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *EventLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && h.log != nil {
		msg := r.Message
		r.Attrs(func(a slog.Attr) bool {
			msg += " " + a.Key + "=" + a.Value.String()
			return true
		})
		// Mirror failures are swallowed: the file log is authoritative, and an
		// Event Log hiccup must never break supervision.
		if r.Level >= slog.LevelError {
			h.log.Error(1, msg)
		} else {
			h.log.Warning(1, msg)
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *EventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &EventLogHandler{inner: h.inner.WithAttrs(attrs), log: h.log}
}

func (h *EventLogHandler) WithGroup(name string) slog.Handler {
	return &EventLogHandler{inner: h.inner.WithGroup(name), log: h.log}
}
