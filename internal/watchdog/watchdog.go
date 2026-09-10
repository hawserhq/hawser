// Package watchdog decides whether a supervisor that exited should be started
// again (#166).
//
// The supervisor is the always-on layer: while it is down, the docker pipe is
// gone and every docker command fails with "The system cannot find the file
// specified" until someone runs `hawser start`. Crashes should not be able to
// do that. A Go fatal runtime error cannot be recovered inside the process, so
// recovery belongs to whoever launched it — hawserw.exe, the logon launcher.
//
// The policy is the classic one, and every rule is there to avoid a restart
// loop being worse than the crash:
//
//   - A clean exit is final. The supervisor exiting 0 means it was asked to go.
//   - An instant failure is not a crash. A bad flag or a held single-instance
//     lock fails in milliseconds and would loop forever; only a process that
//     ran for MinUptime is treated as "was working, then died".
//   - Backoff doubles, so a persistent crash costs little.
//   - A crash budget bounds the whole thing: MaxRestarts inside Window, then
//     the watchdog gives up and says so rather than churning all night.
package watchdog

import (
	"fmt"
	"time"
)

// code renders a process exit code the way a human reads it: small codes as
// themselves, and Windows status codes (a killed process, an access violation)
// in hex, where 0xFFFFFFFF and 0xC0000005 are recognizable.
func code(c int) string {
	if c >= 0 && c < 256 {
		return fmt.Sprintf("%d", c)
	}
	return fmt.Sprintf("0x%08X", uint32(c))
}

// Policy is the restart policy. The zero value is not useful; use Default.
type Policy struct {
	// MinUptime is how long the child must have run for its exit to count as a
	// crash rather than a failure to start.
	MinUptime time.Duration
	// FirstDelay is the pause before the first restart; each subsequent
	// consecutive restart doubles it, capped at MaxDelay.
	FirstDelay time.Duration
	// MaxDelay caps the backoff.
	MaxDelay time.Duration
	// MaxRestarts is how many restarts are allowed within Window.
	MaxRestarts int
	// Window is the crash-budget window.
	Window time.Duration
}

// Default is the shipped policy: a supervisor that ran at least 2s and then
// died comes back after 1s, backing off to 30s, at most 10 times an hour.
//
// 2s is deliberately short. The failures that must NOT be retried all happen
// before the pipe is even opened — a held single-instance lock, an unparsable
// flag, no install — and they exit in tens of milliseconds. A crash, measured,
// can recur within ten seconds of a restart, so a longer threshold would call
// a real crash "a failure to start" and give up while the engine is reachable
// again.
var Default = Policy{
	MinUptime:   2 * time.Second,
	FirstDelay:  time.Second,
	MaxDelay:    30 * time.Second,
	MaxRestarts: 10,
	Window:      time.Hour,
}

// Decision is what to do about an exited child.
type Decision struct {
	// Restart is whether to start it again.
	Restart bool
	// Delay is how long to wait first.
	Delay time.Duration
	// Reason explains the decision in one line, for the watchdog log.
	Reason string
}

// Decide answers whether a child that exited with exitCode after running for
// uptime should be restarted. restarts is the history of restart times inside
// this watchdog's lifetime (most recent last), and consecutive is how many
// restarts have happened without a run reaching MinUptime.
func (p Policy) Decide(exitCode int, uptime time.Duration, restarts []time.Time, consecutive int, now time.Time) Decision {
	if exitCode == 0 {
		return Decision{Reason: "supervisor exited cleanly"}
	}
	if uptime < p.MinUptime {
		return Decision{Reason: fmt.Sprintf(
			"supervisor exited %s after %s, sooner than the %s that separates a crash from a failure to start "+
				"(a held single-instance lock, a bad flag, or no install)",
			code(exitCode), uptime.Round(time.Millisecond), p.MinUptime)}
	}
	if n := countWithin(restarts, now, p.Window); n >= p.MaxRestarts {
		return Decision{Reason: fmt.Sprintf(
			"supervisor exited %s, but it has already been restarted %d times in the last %s; giving up "+
				"(run `hawser start` to try again, and see hawser#166)",
			code(exitCode), n, p.Window)}
	}
	return Decision{
		Restart: true,
		Delay:   p.backoff(consecutive),
		Reason: fmt.Sprintf("supervisor exited %s after %s; restarting",
			code(exitCode), uptime.Round(time.Second)),
	}
}

// backoff returns the delay before a restart, doubling with each consecutive
// short-lived run and capped at MaxDelay.
func (p Policy) backoff(consecutive int) time.Duration {
	d := p.FirstDelay
	for i := 0; i < consecutive; i++ {
		d *= 2
		if d >= p.MaxDelay {
			return p.MaxDelay
		}
	}
	if d > p.MaxDelay {
		return p.MaxDelay
	}
	return d
}

func countWithin(times []time.Time, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	n := 0
	for _, t := range times {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}
