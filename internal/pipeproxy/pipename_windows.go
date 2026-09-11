//go:build windows

package pipeproxy

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// PipeInUse reports whether something is already serving a named pipe.
//
// Checked by dialing rather than by looking for the file: a pipe exists only
// while a server holds it, and a stale path with no listener behaves nothing
// like a live one. A successful dial is closed immediately — this is a probe,
// not a connection.
func PipeInUse(name string) bool {
	timeout := 250 * time.Millisecond
	conn, err := winio.DialPipe(name, &timeout)
	if err != nil {
		// ERROR_PIPE_BUSY (all instances busy) and a dial timeout both mean a
		// server IS there, just not free to answer this instant — treating
		// them as "free" (#91) made SelectPipeName pick the default pipe, then
		// Listen fail against Docker Desktop's live pipe and the supervisor
		// exit, instead of coexisting on the fallback. Only a genuine
		// not-found reads as free.
		if errors.Is(err, windows.ERROR_PIPE_BUSY) || os.IsTimeout(err) {
			return true
		}
		return false
	}
	conn.Close()
	return true
}

// SelectPipeName picks the pipe to serve.
//
// Skrog prefers the default pipe, because that is what stock docker.exe
// connects to with no configuration. But it never takes it from Docker Desktop:
// if something is already listening there, Skrog serves its own pipe instead so
// the two coexist and trying Skrog stays a zero-risk experiment (PLAN §02).
//
// preferred may be empty for DefaultPipeName. The returned reason is worth
// surfacing to the user, since which pipe is in play determines whether they
// need DOCKER_HOST.
func SelectPipeName(preferred string) (name, reason string) {
	if preferred == "" {
		preferred = DefaultPipeName
	}

	// An explicit non-default choice is honored as given; the caller meant it.
	if preferred != DefaultPipeName {
		return preferred, "explicitly requested"
	}

	if !PipeInUse(DefaultPipeName) {
		return DefaultPipeName, "default pipe is free"
	}
	return FallbackPipeName,
		fmt.Sprintf("%s is already served by another engine (likely Docker Desktop)", DefaultPipeName)
}

// DockerHostFor renders the DOCKER_HOST value for a pipe, in the npipe form the
// docker CLI expects. The CLI wants forward slashes even on Windows.
func DockerHostFor(pipeName string) string {
	// \\.\pipe\skrog_engine -> npipe:////./pipe/skrog_engine
	trimmed := pipeName
	for _, prefix := range []string{`\\.\pipe\`, `//./pipe/`} {
		if len(trimmed) > len(prefix) && trimmed[:len(prefix)] == prefix {
			return "npipe:////./pipe/" + trimmed[len(prefix):]
		}
	}
	return "npipe://" + trimmed
}
