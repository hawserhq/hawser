package audit

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errOpen stands in for "the audit file could not be created" -- a full disk,
// a read-only state dir, a permissions problem.
var errOpen = errors.New("cannot open the audit log")

// nopCloser makes a buffer a WriteCloser and records that it was closed, so a
// test can tell "auditing stopped" from "auditing stopped and let go of the
// file".
type nopCloser struct {
	bytes.Buffer
	closed int
}

func (n *nopCloser) Close() error { n.closed++; return nil }

// observe drives one auditable call through the switch.
func observe(s *Switch) {
	s.Observe(time.Now(), "POST", "/v1.45/containers/create", "name=x", 201, nil)
}

func TestSwitchStartsLoggingWhenTheSettingTurnsOn(t *testing.T) {
	// The bug: `skrog config set audit on` produced no log, because the
	// setting was read once when the supervisor started.
	var on bool
	buf := &nopCloser{}
	opens := 0
	s := &Switch{
		Enabled: func() bool { return on },
		Open:    func() (io.WriteCloser, error) { opens++; return buf, nil },
	}

	observe(s)
	if buf.Len() != 0 {
		t.Fatalf("wrote %q while auditing was off", buf.String())
	}
	if opens != 0 {
		t.Error("opened the log while auditing was off")
	}

	on = true
	observe(s)
	if !strings.Contains(buf.String(), `"action":"container-create"`) {
		t.Errorf("no event after the setting turned on; got %q", buf.String())
	}
	if opens != 1 {
		t.Errorf("opened the log %d times, want 1", opens)
	}
}

func TestSwitchStopsAndReleasesTheFileWhenTurnedOff(t *testing.T) {
	on := true
	buf := &nopCloser{}
	s := &Switch{
		Enabled: func() bool { return on },
		Open:    func() (io.WriteCloser, error) { return buf, nil },
	}
	observe(s)
	n := buf.Len()
	if n == 0 {
		t.Fatal("nothing written while auditing was on")
	}

	on = false
	observe(s)
	if buf.Len() != n {
		t.Error("kept writing after the setting turned off")
	}
	if buf.closed != 1 {
		t.Errorf("closed %d times, want 1 — a log turned off should not hold its handle", buf.closed)
	}
}

func TestSwitchReportsEveryTransitionOnce(t *testing.T) {
	on := false
	var got []bool
	s := &Switch{
		Enabled:  func() bool { return on },
		Open:     func() (io.WriteCloser, error) { return &nopCloser{}, nil },
		OnChange: func(enabled bool, err error) { got = append(got, enabled) },
	}
	observe(s)
	on = true
	observe(s)
	observe(s)
	observe(s)
	on = false
	observe(s)
	observe(s)

	want := []bool{true, false}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("transitions = %v, want %v — steady state must not log", got, want)
	}
}

func TestSwitchStaysOffWhenTheLogCannotBeOpened(t *testing.T) {
	// A sink that swallows events looks exactly like one with nothing to
	// record, so a failure to open has to be visible and must not be retried
	// on every single docker call.
	attempts := 0
	var errs []error
	s := &Switch{
		Enabled: func() bool { return true },
		Open: func() (io.WriteCloser, error) {
			attempts++
			return nil, errOpen
		},
		OnChange: func(enabled bool, err error) {
			if err != nil {
				errs = append(errs, err)
			}
		},
	}
	for i := 0; i < 5; i++ {
		observe(s)
	}
	if attempts != 1 {
		t.Errorf("tried to open %d times, want 1", attempts)
	}
	if len(errs) != 1 {
		t.Errorf("reported %d errors, want 1", len(errs))
	}
}

func TestSwitchRetriesAfterTheSettingIsCycled(t *testing.T) {
	on := true
	fail := true
	attempts := 0
	buf := &nopCloser{}
	s := &Switch{
		Enabled: func() bool { return on },
		Open: func() (io.WriteCloser, error) {
			attempts++
			if fail {
				return nil, errOpen
			}
			return buf, nil
		},
	}
	observe(s)
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}

	// Fix the cause, then turn it off and on again — the operator's obvious
	// next move, and it has to work.
	fail = false
	on = false
	observe(s)
	on = true
	observe(s)

	if attempts != 2 {
		t.Errorf("attempts = %d, want a second try after the setting was cycled", attempts)
	}
	if buf.Len() == 0 {
		t.Error("no event recorded after the log was fixed and re-enabled")
	}
}

func TestSwitchCloseIsSafeWhenOff(t *testing.T) {
	s := &Switch{Enabled: func() bool { return false }}
	if err := s.Close(); err != nil {
		t.Errorf("Close with auditing off: %v", err)
	}
}

func TestSwitchIsSafeUnderConcurrentCalls(t *testing.T) {
	// The bridge observes from one goroutine per connection, and the setting
	// can flip under them at any moment. Run this under -race.
	var on atomic.Bool
	on.Store(true)
	s := &Switch{
		Enabled: on.Load,
		Open:    func() (io.WriteCloser, error) { return &nopCloser{}, nil },
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%7 == 0 {
				on.Store(i%14 == 0)
			}
			observe(s)
		}(i)
	}
	wg.Wait()
	s.Close()
}
