package pipeproxy

import (
	"strings"
	"testing"
	"time"
)

// The socat idle timeout is the v0.1 backstop for #35. A docker client that is
// interrupted closes cleanly, which reaches the relay as EOF — indistinguishable
// from the half-close `docker build` legitimately performs — so the relay cannot
// tell the two apart and socat never exits on stdin EOF. -T is what bounds the
// resulting leak, and its absence is invisible until a session degrades hours
// later, which is exactly why it is asserted here.
func TestRelayArgsIdleTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    string
		absent  bool
	}{
		{name: "default applied", timeout: 0, want: "-T 300"},
		{name: "explicit value", timeout: 90 * time.Second, want: "-T 90"},
		{name: "sub-minute value", timeout: 5 * time.Second, want: "-T 5"},
		{name: "negative disables", timeout: -1, absent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &WSLDialer{Distro: "skrog-engine", IdleTimeout: tt.timeout}
			joined := strings.Join(d.relayArgs(DefaultSocketPath), " ")

			if tt.absent {
				if strings.Contains(joined, "-T") {
					t.Errorf("args %q must not contain -T when the timeout is disabled", joined)
				}
				return
			}
			if !strings.Contains(joined, tt.want) {
				t.Errorf("args %q should contain %q", joined, tt.want)
			}
		})
	}
}

func TestRelayArgsShape(t *testing.T) {
	d := &WSLDialer{Distro: "skrog-engine", IdleTimeout: -1}
	got := strings.Join(d.relayArgs("/var/run/docker.sock"), " ")
	want := "-d skrog-engine -u root --exec socat STDIO UNIX-CONNECT:/var/run/docker.sock"
	if got != want {
		t.Errorf("args =\n  %q\nwant\n  %q", got, want)
	}
}

func TestIdleTimeoutDefaulting(t *testing.T) {
	if got := (&WSLDialer{}).idleTimeout(); got != DefaultIdleTimeout {
		t.Errorf("zero value = %v, want the default %v", got, DefaultIdleTimeout)
	}
	if got := (&WSLDialer{IdleTimeout: -5}).idleTimeout(); got != 0 {
		t.Errorf("negative = %v, want 0 (disabled)", got)
	}
	if got := (&WSLDialer{IdleTimeout: time.Minute}).idleTimeout(); got != time.Minute {
		t.Errorf("explicit = %v, want 1m", got)
	}
}
