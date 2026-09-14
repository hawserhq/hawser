package wslc

import (
	"fmt"

	"github.com/wslkit/skrog/internal/winpath"
)

// EngineSocket is where dockerd listens inside a wslc session.
const EngineSocket = "/var/run/docker.sock"

// TranslateBindSource maps a bind source for the wslc backend.
//
// Three cases, and the middle one is the reason this exists rather than reusing
// winpath.ToWSL:
//
//   - A Windows named pipe means "this engine's socket", exactly as on the
//     distro backend (#164). Testcontainers' Ryuk and docker-in-docker depend
//     on it: Ryuk is handed the host's docker endpoint to bind, and on Windows
//     that endpoint is a pipe. Without this, testcontainers-go binds the
//     literal "npipe:////./pipe/..." string as a path — it only special-cases
//     the pipe when the engine reports itself as Docker Desktop, and a wslc
//     session reports "Microsoft Azure Linux 3.0".
//
//   - A Windows drive path has no meaning here yet and must fail loudly.
//     winpath.ToWSL would turn C:\src into /mnt/c/src, which is right for a
//     distro that auto-mounts drives and wrong for a session, where each
//     Windows folder is its own virtiofs share at /mnt/{GUID} and no /mnt/c
//     exists. Passing that through would create a directory inside the
//     container instead of mounting anything — a silent wrong answer where an
//     error is far kinder (#321).
//
//   - Anything already guest-absolute passes through untouched. That is what
//     makes /var/run/docker.sock and /tmp binds work on this backend at all,
//     and it is the thing the wslc CLI cannot express.
func TranslateBindSource(source string) (string, error) {
	if winpath.IsPipe(source) {
		return EngineSocket, nil
	}
	if winpath.HasDrive(source) {
		return "", fmt.Errorf("bind source %q: Windows paths are not supported on the wslc backend yet "+
			"(each Windows folder needs its own virtiofs share; see #321). "+
			"Guest paths such as /tmp and /var/run/docker.sock work normally", source)
	}
	return source, nil
}
