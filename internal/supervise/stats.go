package supervise

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Stats is what the supervisor knows and the CLI cannot: how long the engine
// has been up, how often the idle timeout has taken it down, and what the
// bridge has carried (#179).
//
// It reaches `hawser status --stats` through a file rather than an IPC channel,
// for three reasons. The supervisor is a separate process, so something has to
// cross the boundary. A file keeps working when the supervisor has died (#166),
// which is exactly when a reading is most interesting — and because every
// reading is timestamped, a stale one is visible rather than believed. And
// there is no new surface to secure: the docker pipe stays the docker API.
type Stats struct {
	// UpdatedAt is when the supervisor last wrote this. A reader compares it
	// with now to decide whether the numbers still describe reality.
	UpdatedAt time.Time `json:"updatedAt"`
	// PID is the supervisor that wrote it, so a reader can tell a live file
	// from one left behind by a process that is gone.
	PID int `json:"pid"`

	Lifecycle Lifecycle `json:"lifecycle"`
	Bridge    Bridge    `json:"bridge"`
}

// Lifecycle is the supervisor's own history since it started.
type Lifecycle struct {
	// StartedAt is when this supervisor started; EngineStartedAt when it last
	// brought the engine up (zero if it has not).
	StartedAt       time.Time `json:"startedAt"`
	EngineStartedAt time.Time `json:"engineStartedAt,omitempty"`
	// EngineStarts counts starts this supervisor performed, including cold
	// starts on demand and restarts after a crash -- a number climbing on an
	// idle machine is the engine flapping.
	EngineStarts int `json:"engineStarts"`
	// IdleStops counts idle-timeout stops, and LastIdleStopAt / LastWakeAt
	// bracket the most recent cycle. Together they say whether idle-timeout is
	// set somewhere useful: no stops means it never fires, and a stop
	// immediately followed by a wake means it fires too eagerly.
	IdleStops      int       `json:"idleStops"`
	LastIdleStopAt time.Time `json:"lastIdleStopAt,omitempty"`
	LastWakeAt     time.Time `json:"lastWakeAt,omitempty"`
}

// Bridge is what the pipe carried, plus which transport carried it.
type Bridge struct {
	Connections   uint64 `json:"connections"`
	BytesToEngine uint64 `json:"bytesToEngine"`
	BytesToClient uint64 `json:"bytesToClient"`
	ActiveConns   int    `json:"activeConns"`
	// Transport is "vsock" (the fast path) or "fallback" (the socat relay,
	// ~165 ms per connection instead of ~0.6 ms). This is the field that
	// explains a slow docker with a healthy engine.
	Transport string `json:"transport"`
}

// statsFile is the flush target, beside the supervisor's log.
func statsFile(stateDir string) string {
	return filepath.Join(stateDir, "supervisor-stats.json")
}

// WriteStats writes the file atomically: a reader must see a whole reading or
// the previous one, never half of each. Failure is the caller's to ignore --
// statistics must never be able to take the supervisor down.
func WriteStats(stateDir string, s Stats) error {
	s.UpdatedAt = time.Now().UTC()
	if s.PID == 0 {
		s.PID = os.Getpid()
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := statsFile(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stats-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ReadStats reads the last reading. A missing file returns ok=false rather than
// an error: no supervisor has run since the state dir was made, which is a
// state and not a fault.
func ReadStats(stateDir string) (Stats, bool, error) {
	b, err := os.ReadFile(statsFile(stateDir))
	if os.IsNotExist(err) {
		return Stats{}, false, nil
	}
	if err != nil {
		return Stats{}, false, fmt.Errorf("reading supervisor stats: %w", err)
	}
	var s Stats
	if err := json.Unmarshal(b, &s); err != nil {
		return Stats{}, false, fmt.Errorf("parsing supervisor stats: %w", err)
	}
	return s, true, nil
}

// Age is how old a reading is. A reader shows it rather than hiding it: the
// supervisor flushes every few seconds, so anything much older means the
// supervisor is gone or wedged, and the numbers below it are history.
func (s Stats) Age() time.Duration {
	if s.UpdatedAt.IsZero() {
		return 0
	}
	return time.Since(s.UpdatedAt)
}

// Fresh reports whether a reading is recent enough to describe the present.
// The bound is generous relative to the flush interval so a busy machine does
// not flap between fresh and stale.
func (s Stats) Fresh() bool {
	return !s.UpdatedAt.IsZero() && s.Age() < 30*time.Second
}

// LifecycleSnapshot returns the supervisor's own counters. Safe to call from
// another goroutine: it takes the same mutex the reconciler does, so a reading
// never lands mid-tick.
func (s *Supervisor) LifecycleSnapshot() Lifecycle {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lifecycle
	l.StartedAt = s.startedAt
	if !s.upSince.IsZero() {
		l.EngineStartedAt = s.upSince
	}
	return l
}
