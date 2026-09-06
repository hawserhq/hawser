//go:build windows

package pipeproxy

import (
	"strings"
	"testing"

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
	name := `\\.\pipe\hawser-dacl-test`
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

	conn, err := winio.DialPipe(name, nil)
	if err != nil {
		t.Fatalf("owner denied by own pipe ACL: %v", err)
	}
	conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("accept: %v", err)
	}
}
