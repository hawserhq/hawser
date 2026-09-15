package wslc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/wslkit/skrog/internal/winpath"
)

// ShareTable maps Windows drives to the virtiofs shares that expose them inside
// a wslc session (#321).
//
// A session has no /mnt/c. Every Windows folder handed to it becomes its own
// virtiofs share, mounted in the session VM's ROOT namespace at /mnt/{GUID} —
// so a bind source of C:\src\app has to become /mnt/{GUID}/src/app, and the
// GUID has to be discovered rather than guessed.
//
// The share is created by running a container that mounts the drive, because
// that is the only way the shipped CLI exposes the operation. The share exists
// for exactly as long as that container does, which is why the holder is
// long-lived rather than `--rm`.
//
// Per DRIVE rather than per bind source, deliberately. It is what the distro
// backend already does — WSL auto-mounts whole drives at /mnt/c, and any
// container bind-mounting through them can reach the whole drive — so this is
// the same exposure users already have, not a new one, and it costs one visible
// holder container instead of one per distinct source. `allow-bind-sources` in
// policy.yaml remains the way to narrow it.
type ShareTable struct {
	Local   *Local
	Session string

	// EngineDial reaches the engine socket, for inspecting the holder to learn
	// the share path the CLI created.
	EngineDial func(context.Context) (io.ReadWriteCloser, error)

	Logger *slog.Logger

	mu     sync.Mutex
	shares map[string]string // drive letter -> /mnt/{GUID}
}

// HolderPrefix names the containers that hold drive shares open. Visible in
// `docker ps`, which is unavoidable: the share dies with the container.
const HolderPrefix = "skrog-share-"

// HolderImage is what the holder runs. busybox is already present in any
// session that has pulled anything, and `sleep infinity` costs nothing.
const HolderImage = "busybox"

// Translate maps one bind source for the wslc backend, creating the drive's
// share on first use.
//
// The three cases, in the order they are decided:
//
//   - a Windows named pipe means this engine's socket, on any backend (#164);
//   - a Windows drive path resolves through the share table;
//   - anything already guest-absolute passes through, which is what makes
//     /var/run/docker.sock and /tmp binds work here at all.
func (s *ShareTable) Translate(ctx context.Context, source string) (string, error) {
	if winpath.IsPipe(source) {
		return EngineSocket, nil
	}
	drive, rest, ok := winpath.SplitDrive(source)
	if !ok {
		return source, nil
	}

	share, err := s.shareFor(ctx, drive)
	if err != nil {
		return "", fmt.Errorf("sharing %s: into the wslc session: %w", strings.ToUpper(drive)+":", err)
	}
	if rest == "" {
		return share, nil
	}
	return share + "/" + rest, nil
}

// Translator adapts Translate to the signature the pipe handler wants, binding
// the proxy's context so share creation can be cancelled with it.
func (s *ShareTable) Translator(ctx context.Context) func(string) (string, error) {
	return func(source string) (string, error) { return s.Translate(ctx, source) }
}

// shareFor returns the guest mount point for a drive, creating the holder if
// needed.
func (s *ShareTable) shareFor(ctx context.Context, drive string) (string, error) {
	s.mu.Lock()
	cached, ok := s.shares[drive]
	s.mu.Unlock()
	if ok {
		// Confirm it is still there: the GUID is per mount, so a session VM
		// that restarted has a different one, or none.
		if s.shareExists(ctx, cached) {
			return cached, nil
		}
		s.mu.Lock()
		delete(s.shares, drive)
		s.mu.Unlock()
	}

	share, err := s.ensureHolder(ctx, drive)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.shares == nil {
		s.shares = map[string]string{}
	}
	s.shares[drive] = share
	s.mu.Unlock()

	s.log().Info("shared a drive into the wslc session",
		"drive", strings.ToUpper(drive)+":", "guest-path", share)
	return share, nil
}

// shareExists checks the mount point is still present in the root namespace.
func (s *ShareTable) shareExists(ctx context.Context, share string) bool {
	out, err := s.Local.RunInSession(ctx, s.Session, "sh", "-c",
		"test -d '"+share+"' && echo yes || echo no")
	return err == nil && strings.Contains(out, "yes")
}

// ensureHolder starts (or reuses) the container that holds a drive's share
// open, and returns the share's path in the root namespace.
func (s *ShareTable) ensureHolder(ctx context.Context, drive string) (string, error) {
	name := HolderPrefix + drive

	// An existing holder from this or a previous run is reused; its share is
	// already mounted and inspecting it is far cheaper than recreating it.
	if share, err := s.inspectHolder(ctx, name); err == nil && share != "" {
		if s.shareExists(ctx, share) {
			return share, nil
		}
	}

	// Remove any stale holder before recreating, or the name is taken.
	_, _ = s.Local.RunInSession(ctx, s.Session, "sh", "-c",
		"curl -s -X DELETE --unix-socket "+EngineSocket+
			" http://localhost/"+APIVersion+"/containers/"+name+"?force=1 -o /dev/null")

	// The drive root, spelled the way the CLI wants it: `C:\`.
	windowsRoot := strings.ToUpper(drive) + `:\`
	if _, err := s.Local.RunInSession(ctx, s.Session, "true"); err != nil {
		return "", fmt.Errorf("session %q is not reachable: %w", s.Session, err)
	}
	if err := s.runHolder(ctx, name, windowsRoot); err != nil {
		return "", err
	}

	share, err := s.inspectHolder(ctx, name)
	if err != nil {
		return "", err
	}
	if share == "" {
		return "", fmt.Errorf("holder %s reported no share for %s", name, windowsRoot)
	}
	return share, nil
}

// runHolder starts the holder through the wslc CLI.
//
// The CLI rather than the engine socket, because creating the virtiofs share is
// precisely the thing the socket cannot do: the share is made by wslcsession on
// the Windows side when it handles a -v, which is the whole reason this issue
// exists.
func (s *ShareTable) runHolder(ctx context.Context, name, windowsRoot string) error {
	out, err := s.Local.run(ctx, "--session", s.Session, "run", "-d",
		"--name", name, "-v", windowsRoot+":/share", HolderImage, "sleep", "infinity")
	if err != nil {
		return fmt.Errorf("starting the share holder for %s: %w", windowsRoot, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("share holder for %s produced no container id", windowsRoot)
	}
	return nil
}

// inspectHolder reads the share path the CLI created, from the engine's own
// view of the holder. The Source of its bind is the /mnt/{GUID} we need.
func (s *ShareTable) inspectHolder(ctx context.Context, name string) (string, error) {
	body, err := apiGet(ctx, s.EngineDial, "/"+APIVersion+"/containers/"+name+"/json")
	if err != nil {
		return "", err
	}
	var ins struct {
		Mounts []struct {
			Type        string `json:"Type"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := json.Unmarshal(body, &ins); err != nil {
		return "", fmt.Errorf("reading the share holder's mounts: %w", err)
	}
	for _, m := range ins.Mounts {
		if m.Destination == "/share" && strings.HasPrefix(m.Source, "/mnt/") {
			return m.Source, nil
		}
	}
	return "", nil
}

// Close removes the holder containers, releasing the shares.
func (s *ShareTable) Close(ctx context.Context) {
	s.mu.Lock()
	drives := make([]string, 0, len(s.shares))
	for d := range s.shares {
		drives = append(drives, d)
	}
	s.shares = nil
	s.mu.Unlock()

	for _, d := range drives {
		_, _ = s.Local.RunInSession(ctx, s.Session, "sh", "-c",
			"curl -s -X DELETE --unix-socket "+EngineSocket+
				" http://localhost/"+APIVersion+"/containers/"+HolderPrefix+d+"?force=1 -o /dev/null")
	}
}

func (s *ShareTable) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.New(slog.DiscardHandler)
}
