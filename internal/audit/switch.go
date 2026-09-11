package audit

import (
	"io"
	"sync"
	"time"
)

// Switch is an AuditSink that turns the log on and off while the supervisor
// runs, so `hawser config set audit on` takes effect on the next docker call
// with nothing to restart (#202).
//
// Before this, the setting was read once when the supervisor started and the
// CLI told users to run `hawser restart`. That advice did not work: restart
// bounces the *engine*, and the supervisor holding the setting survives it.
// Turning the audit log on therefore did nothing, reported no error, and
// produced no log — the worst shape a security feature can fail in, because
// the operator believes it is on.
//
// Opening the destination is deferred until auditing is actually wanted, so a
// machine that never enables it never creates the file.
//
// The zero value is not useful; set Enabled and Open.
type Switch struct {
	// Enabled reports whether the audit log should be running right now. It
	// is called once per observed request, so it must be cheap — back it with
	// a config.Watcher, which stats before it reads.
	Enabled func() bool
	// Open creates the destination. Called each time auditing transitions
	// from off to on, and the writer is closed on the way back down, so a log
	// turned off releases its file handle rather than holding it forever.
	Open func() (io.WriteCloser, error)
	// OnChange reports each transition, and any failure to open. Optional.
	OnChange func(enabled bool, err error)

	mu  sync.Mutex
	w   io.WriteCloser
	log *Logger
	// failed suppresses a repeated open error: a destination that cannot be
	// created fails the same way on every request, and one line in the
	// supervisor log is the useful number.
	failed bool
}

// Observe implements the bridge's audit sink: it reconciles the log's state
// with the setting, then records the call if the log is on.
func (s *Switch) Observe(start time.Time, method, path, rawQuery string, status int, err error) {
	s.mu.Lock()
	log := s.reconcileLocked()
	s.mu.Unlock()

	if log == nil {
		return
	}
	log.Observe(start, method, path, rawQuery, status, err)
}

// reconcileLocked brings the open writer into line with the setting and
// returns the logger to use, or nil when auditing is off.
func (s *Switch) reconcileLocked() *Logger {
	want := s.Enabled != nil && s.Enabled()

	switch {
	case want && s.log == nil:
		if s.failed {
			return nil
		}
		w, err := s.Open()
		if err != nil {
			// Stay off rather than pretend: a sink that swallows events is
			// indistinguishable from one that has nothing to record.
			s.failed = true
			if s.OnChange != nil {
				s.OnChange(false, err)
			}
			return nil
		}
		s.w, s.log = w, New(w)
		if s.OnChange != nil {
			s.OnChange(true, nil)
		}
	case !want && s.log != nil:
		s.closeLocked()
		if s.OnChange != nil {
			s.OnChange(false, nil)
		}
	case !want:
		// Turning the setting off clears a previous open failure, so fixing
		// the cause and turning it back on gets a fresh attempt.
		s.failed = false
	}
	return s.log
}

// Close releases the destination. Safe to call when auditing is off.
func (s *Switch) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *Switch) closeLocked() error {
	if s.w == nil {
		s.log = nil
		return nil
	}
	err := s.w.Close()
	s.w, s.log = nil, nil
	return err
}
