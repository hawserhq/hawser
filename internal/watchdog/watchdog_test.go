package watchdog_test

import (
	"strings"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/watchdog"
)

var now = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

func TestCleanExitIsFinal(t *testing.T) {
	d := watchdog.Default.Decide(0, time.Hour, nil, 0, now)
	if d.Restart {
		t.Error("a supervisor that exited 0 was asked to leave; restarting it fights the user")
	}
}

func TestCrashRestarts(t *testing.T) {
	d := watchdog.Default.Decide(2, 45*time.Minute, nil, 0, now)
	if !d.Restart {
		t.Fatalf("a long-running supervisor that died was not restarted: %s", d.Reason)
	}
	if d.Delay != time.Second {
		t.Errorf("first restart delay = %s, want 1s", d.Delay)
	}
	if !strings.Contains(d.Reason, "exited 2") {
		t.Errorf("reason does not name the exit code: %q", d.Reason)
	}
}

func TestInstantFailureIsNotACrash(t *testing.T) {
	// The two real cases: the single-instance lock is held (exit 1, instantly)
	// and a bad flag (exit 2, instantly). Restarting either loops forever.
	for _, code := range []int{1, 2, 3} {
		d := watchdog.Default.Decide(code, 40*time.Millisecond, nil, 0, now)
		if d.Restart {
			t.Errorf("exit %d after 40ms was treated as a crash", code)
		}
		if !strings.Contains(d.Reason, "failure to start") {
			t.Errorf("reason should explain the distinction: %q", d.Reason)
		}
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		d := watchdog.Default.Decide(2, time.Minute, nil, i, now)
		if !d.Restart {
			t.Fatalf("consecutive=%d: not restarting", i)
		}
		if d.Delay != w {
			t.Errorf("consecutive=%d: delay = %s, want %s", i, d.Delay, w)
		}
	}
}

func TestCrashBudgetStops(t *testing.T) {
	var restarts []time.Time
	for i := 0; i < watchdog.Default.MaxRestarts; i++ {
		restarts = append(restarts, now.Add(-time.Duration(i)*time.Minute))
	}
	d := watchdog.Default.Decide(2, time.Minute, restarts, 0, now)
	if d.Restart {
		t.Error("restarted past the crash budget")
	}
	if !strings.Contains(d.Reason, "giving up") || !strings.Contains(d.Reason, "skrog start") {
		t.Errorf("giving up should say so and how to recover: %q", d.Reason)
	}
}

func TestBudgetIsAWindowNotATotal(t *testing.T) {
	// Ten crashes yesterday must not stop a restart today: the budget is a
	// rate, so a machine that has been up for weeks is not stuck.
	var restarts []time.Time
	for i := 0; i < 50; i++ {
		restarts = append(restarts, now.Add(-25*time.Hour))
	}
	d := watchdog.Default.Decide(2, time.Minute, restarts, 0, now)
	if !d.Restart {
		t.Errorf("old restarts still counted against the budget: %s", d.Reason)
	}
}

func TestWindowsStatusCodesAreReadable(t *testing.T) {
	// A killed process reports 0xFFFFFFFF and an access violation 0xC0000005;
	// as decimals those are noise in a log line.
	d := watchdog.Default.Decide(int(uint32(0xFFFFFFFF)), time.Minute, nil, 0, now)
	if !strings.Contains(d.Reason, "0xFFFFFFFF") {
		t.Errorf("reason = %q, want the status code in hex", d.Reason)
	}
	d = watchdog.Default.Decide(2, time.Minute, nil, 0, now)
	if !strings.Contains(d.Reason, "exited 2 ") {
		t.Errorf("small codes should stay decimal: %q", d.Reason)
	}
}

func TestACrashThatRecursQuicklyStillCountsAsACrash(t *testing.T) {
	// Measured on a real crash (#166): the restarted supervisor died again
	// after ~9.5s. A threshold above that would classify it as a failure to
	// start and stop restarting while the engine was still recoverable.
	d := watchdog.Default.Decide(2, 9500*time.Millisecond, nil, 0, now)
	if !d.Restart {
		t.Errorf("a crash 9.5s in was not retried: %s", d.Reason)
	}
}
