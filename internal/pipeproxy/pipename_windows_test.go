//go:build windows

package pipeproxy_test

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/hawserhq/hawser/internal/pipeproxy"
)

func TestDockerHostFor(t *testing.T) {
	// The docker CLI wants forward slashes even on Windows, so the backslash
	// pipe name has to be rewritten rather than passed through.
	tests := []struct {
		in   string
		want string
	}{
		{`\\.\pipe\docker_engine`, "npipe:////./pipe/docker_engine"},
		{`\\.\pipe\hawser_engine`, "npipe:////./pipe/hawser_engine"},
		{"//./pipe/hawser_engine", "npipe:////./pipe/hawser_engine"},
		{"hawser_custom", "npipe://hawser_custom"},
	}
	for _, tt := range tests {
		if got := pipeproxy.DockerHostFor(tt.in); got != tt.want {
			t.Errorf("DockerHostFor(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPipeInUseDetectsALiveListener(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\hawser-inuse-%d`, os.Getpid())

	if pipeproxy.PipeInUse(name) {
		t.Fatalf("%s reported in use before anything listened", name)
	}

	l, err := pipeproxy.Listen(name, "")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// Accept exactly once, and prove it finished before Close is called.
	// An open-ended accept LOOP deadlocked this test rarely on CI (a 10m
	// timeout): winio's listener Close *sends* on an unbuffered closeCh while
	// a pending Accept sends on acceptCh, the listenerRoutine's select picks
	// between them at random, and an unlucky pick parks Close forever. With
	// the accept known-complete, Close has no competitor and is deterministic.
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		if c, err := l.Accept(); err == nil {
			c.(net.Conn).Close()
		}
	}()

	if !pipeproxy.PipeInUse(name) {
		t.Errorf("%s reported free while a listener held it", name)
	}

	// The probe's instant dial-and-close can land as ERROR_NO_DATA inside the
	// listener, which retries and waits for another client — leaving the
	// accept above pending. A second dial, held open until the accept is
	// observed done, releases it on that path; on the ordinary path the accept
	// is already done and this dial simply fails against a closed-or-busy
	// instance, which is fine either way.
	timeout := 250 * time.Millisecond
	if c, err := winio.DialPipe(name, &timeout); err == nil {
		<-accepted
		c.Close()
	}
	<-accepted

	l.Close()
	if pipeproxy.PipeInUse(name) {
		t.Errorf("%s still reported in use after the listener closed", name)
	}
}

func TestSelectPipeNameHonorsExplicitChoice(t *testing.T) {
	// An explicit non-default name is taken as given; the caller meant it.
	custom := `\\.\pipe\hawser-explicit`
	got, reason := pipeproxy.SelectPipeName(custom)
	if got != custom {
		t.Errorf("got %q, want %q", got, custom)
	}
	if reason == "" {
		t.Error("no reason given for the selection")
	}
}

func TestSelectPipeNameFallsBackWhenDefaultIsHeld(t *testing.T) {
	// The behavior that makes trying Hawser zero-risk: it never takes the
	// default pipe away from Docker Desktop.
	//
	// This machine's real state decides which branch runs, so both are
	// asserted for what they must be rather than which one occurs.
	got, reason := pipeproxy.SelectPipeName("")

	switch got {
	case pipeproxy.DefaultPipeName:
		if pipeproxy.PipeInUse(pipeproxy.DefaultPipeName) {
			t.Error("chose the default pipe while something else was serving it")
		}
		if reason == "" {
			t.Error("no reason given")
		}
	case pipeproxy.FallbackPipeName:
		if !pipeproxy.PipeInUse(pipeproxy.DefaultPipeName) {
			t.Error("fell back although the default pipe was free")
		}
		if reason == "" {
			t.Error("fallback must explain itself, since it decides whether the user needs DOCKER_HOST")
		}
	default:
		t.Errorf("chose an unexpected pipe %q", got)
	}
}

func TestDefaultAndFallbackPipesDiffer(t *testing.T) {
	// If these ever collided, coexistence with Docker Desktop would silently
	// stop working.
	if pipeproxy.DefaultPipeName == pipeproxy.FallbackPipeName {
		t.Fatal("default and fallback pipe names are identical")
	}
}
