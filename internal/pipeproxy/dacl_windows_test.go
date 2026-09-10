//go:build windows

package pipeproxy

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestDefaultSDDLScopedToOwner(t *testing.T) {
	// #79: the pipe ACL must never include INTERACTIVE (every logged-on
	// user's token) and must name the owning user explicitly.
	sddl, err := defaultSDDL()
	if err != nil {
		t.Fatalf("defaultSDDL: %v", err)
	}
	if strings.Contains(sddl, ";IU)") {
		t.Fatalf("descriptor still grants to INTERACTIVE: %s", sddl)
	}
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sddl, sid) {
		t.Fatalf("descriptor %s does not name the owning user %s", sddl, sid)
	}
}

func TestListenOwnerCanStillConnect(t *testing.T) {
	// The tightened ACL must not lock the owner out of their own pipe.
	name := uniquePipeName(t)
	l, err := Listen(name, "")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	done := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			c.Close()
		}
		done <- err
	}()

	// winio's ListenPipe reserves the name with a "first handle" that nothing
	// ever accepts on: makeServerPipe always creates a NEW instance, and only
	// once Accept has reached connectPipe is anything actually waiting. A dial
	// that wins the race against the goroutine above therefore lands on the
	// phantom instance, is never accepted, and Accept blocks on an instance no
	// client will ever reach — a hang rather than a failure, which is how this
	// consumed the whole 10-minute test budget on CI.
	//
	// So dial until one of them is accepted, and bound the whole thing: a
	// second dial necessarily arrives after Accept is pending. The deadline is
	// what keeps a genuine regression a failure instead of a hung suite.
	deadline := time.After(30 * time.Second)
	for {
		conn, err := winio.DialPipe(name, nil)
		if err != nil {
			t.Fatalf("owner denied by own pipe ACL: %v", err)
		}
		select {
		case err := <-done:
			conn.Close()
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			return
		case <-time.After(250 * time.Millisecond):
			// That one hit the reserved instance. Drop it and dial again.
			conn.Close()
		case <-deadline:
			conn.Close()
			t.Fatal("the listener never accepted a connection from its owner")
		}
	}
}

// pipeSeq makes each test pipe name unique. A Windows named pipe lives in a
// machine-wide namespace and an instance can outlive the listener that created
// it, so a fixed name is only safe while the test runs exactly once per
// machine. Under `go test -count=N` it is not: a later iteration's DialPipe can
// reach the previous instance while the new listener waits in Accept for a
// client that already went elsewhere. That is a hang rather than a failure,
// which is how it burned ten minutes of CI without reporting anything useful.
var pipeSeq atomic.Uint64

func uniquePipeName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\hawser-dacl-test-%d-%d`, os.Getpid(), pipeSeq.Add(1))
}
