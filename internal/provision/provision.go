package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zcsizmadia/hawser/internal/wsl"
)

// DefaultDistro is the WSL distribution Hawser imports. Deliberately distinct
// so it never collides with a user's own Ubuntu (PLAN §04).
const DefaultDistro = "hawser-engine"

// distroNameRE bounds what a distro name may contain (#93). Names flow into
// shells (the /mnt/wsl share/unshare scripts pass them as positional args, but
// the unshare's rm -rf operates on a path derived from the name) and into a
// profile-script line by wsl-integrate. Restricting to this charset — and
// forbidding the path-traversal spellings — closes the whole class rather than
// auditing each call site; every legitimate name (the default, a user's
// --distro) already fits.
var distroNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateDistroName(name string) error {
	if !distroNameRE.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("invalid distro name %q: use letters, digits, dot, dash, underscore", name)
	}
	return nil
}

// EngineSocket is where dockerd listens inside the distro.
const EngineSocket = "/var/run/docker.sock"

// Options configures an install. Zero values get sensible defaults, so callers
// only set what they mean to change.
type Options struct {
	// Distro is the WSL distribution name. Defaults to DefaultDistro.
	Distro string
	// StateDir holds the manifest and the rootfs cache.
	// Defaults to %LOCALAPPDATA%\Hawser.
	StateDir string
	// DataDir is where the distro's VHDX lives. Defaults to StateDir\distro.
	// Exposed because "move it off C:" is a perennial request (PLAN §03).
	DataDir string
	// RootfsURL and RootfsSHA256 identify the rootfs to install. The checksum
	// is mandatory: an unverified rootfs becomes root inside the engine VM.
	RootfsURL    string
	RootfsSHA256 string
	// EngineVersion is recorded in the manifest for `hawser version`.
	EngineVersion string
	// Headless suppresses anything that would wait for a human.
	Headless bool
	// StartTimeout bounds the wait for dockerd's socket. Defaults to 60s.
	StartTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.Distro == "" {
		o.Distro = DefaultDistro
	}
	if o.StateDir == "" {
		o.StateDir = defaultStateDir()
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(o.StateDir, "distro")
	}
	if o.StartTimeout == 0 {
		o.StartTimeout = 60 * time.Second
	}
	return o
}

func defaultStateDir() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return filepath.Join(base, "Hawser")
	}
	// Non-Windows only happens in tests; keep it deterministic rather than
	// panicking so the package stays testable everywhere.
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "hawser")
	}
	return filepath.Join(home, ".hawser")
}

// Manifest records what an install put on the machine, so uninstall can remove
// exactly that and `hawser version` can report it without re-deriving anything.
type Manifest struct {
	Distro        string    `json:"distro"`
	DataDir       string    `json:"dataDir"`
	RootfsURL     string    `json:"rootfsUrl"`
	RootfsSHA256  string    `json:"rootfsSha256"`
	EngineVersion string    `json:"engineVersion"`
	InstalledAt   time.Time `json:"installedAt"`
	// WSLVersion is what WSL reported at install time, useful when diagnosing
	// a machine whose WSL was updated afterwards.
	WSLVersion string `json:"wslVersion,omitempty"`
}

// Provisioner performs installs and removals.
type Provisioner struct {
	// WSL drives wsl.exe. Defaults to the real implementation.
	WSL wsl.WSL
	// Fetcher retrieves the rootfs. Defaults to HTTP.
	Fetcher Fetcher
	// Logger receives progress. Defaults to slog.Default().
	Logger *slog.Logger
}

func (p *Provisioner) wsl() wsl.WSL {
	if p.WSL != nil {
		return p.WSL
	}
	return wsl.NewLocal()
}

func (p *Provisioner) fetcher() Fetcher {
	if p.Fetcher != nil {
		return p.Fetcher
	}
	return HTTPFetcher{}
}

func (p *Provisioner) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// PreflightError reports that installation cannot proceed, carrying every
// problem so the user sees the full picture rather than one issue per run.
type PreflightError struct{ Report Report }

func (e *PreflightError) Error() string {
	msg := "preflight failed:"
	for _, p := range e.Report.Problems {
		msg += "\n- " + p.String()
	}
	return msg
}

// Install provisions the engine distro: preflight, verified download, import,
// then start.
//
// It is deliberately not idempotent over an existing distro — preflight refuses
// when the target is already registered, because that distro holds the user's
// images and volumes and silently reimporting would destroy them.
func (p *Provisioner) Install(ctx context.Context, opts Options) (*Manifest, error) {
	opts = opts.withDefaults()
	if err := validateDistroName(opts.Distro); err != nil {
		return nil, err
	}
	if opts.RootfsURL == "" {
		return nil, fmt.Errorf("install: RootfsURL is required")
	}

	report, err := p.Preflight(ctx, opts)
	if err != nil {
		return nil, err
	}
	for _, w := range report.Warnings {
		p.logger().Warn(w.Summary, "fix", w.Remedy)
	}
	if !report.OK {
		return nil, &PreflightError{Report: report}
	}

	rootfs := filepath.Join(opts.StateDir, "rootfs", filepath.Base(opts.RootfsURL))
	if err := p.fetchRootfs(ctx, opts.RootfsURL, opts.RootfsSHA256, rootfs); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data dir %s: %w", opts.DataDir, err)
	}

	p.logger().Info("importing distro", "distro", opts.Distro, "dataDir", opts.DataDir)
	if err := p.wsl().Import(ctx, opts.Distro, opts.DataDir, rootfs); err != nil {
		return nil, fmt.Errorf("importing %s: %w", opts.Distro, err)
	}

	engineVersion := opts.EngineVersion
	if engineVersion == "" {
		// The rootfs records what it actually contains, which is more
		// trustworthy than a flag and is the only source available when the
		// rootfs came from --rootfs-url rather than the release manifest.
		engineVersion = p.engineVersionFromDistro(ctx, opts)
	}

	m := &Manifest{
		Distro:        opts.Distro,
		DataDir:       opts.DataDir,
		RootfsURL:     opts.RootfsURL,
		RootfsSHA256:  opts.RootfsSHA256,
		EngineVersion: engineVersion,
		InstalledAt:   time.Now().UTC(),
		WSLVersion:    report.Status.Version,
	}
	// Written before the engine starts: if start fails, uninstall still knows
	// what to clean up rather than leaving an orphaned distro behind.
	if err := p.writeManifest(opts, m); err != nil {
		return nil, err
	}

	if err := p.StartEngine(ctx, opts); err != nil {
		return m, fmt.Errorf("starting engine: %w", err)
	}
	return m, nil
}

// EngineVersionFile is where the rootfs records the engine it carries.
const EngineVersionFile = "/etc/hawser/engine-version"

// engineVersionFromDistro reads the version the rootfs declares. Best-effort:
// a rootfs without the marker is unusual but not a reason to fail an install,
// and `hawser version` reports an unknown engine rather than a wrong one.
func (p *Provisioner) engineVersionFromDistro(ctx context.Context, opts Options) string {
	out, err := p.wsl().Exec(ctx, opts.Distro, "root", "cat", EngineVersionFile)
	if err != nil {
		p.logger().Debug("rootfs declares no engine version",
			"file", EngineVersionFile, "error", err)
		return ""
	}
	return strings.TrimSpace(out)
}

// StartEngine launches dockerd and waits for its socket.
func (p *Provisioner) StartEngine(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()

	if running, _ := p.engineRunning(ctx, opts); running {
		p.logger().Info("engine already running", "distro", opts.Distro)
		// Neither the agent nor the socket share is tied to dockerd's
		// lifetime: a supervisor that finds a healthy engine (its own
		// restart, say) must still make sure both are up.
		p.startAgent(ctx, opts)
		p.shareEngineSocket(ctx, opts)
		return nil
	}

	p.logger().Info("starting dockerd", "distro", opts.Distro)
	// Output goes to a log inside the distro; the caller gets it via
	// `hawser logs` rather than having it interleaved here.
	if _, err := p.wsl().Start(ctx, opts.Distro, "root",
		"sh", "-c", "dockerd >>/var/log/dockerd.log 2>&1"); err != nil {
		return fmt.Errorf("launching dockerd: %w", err)
	}
	p.startAgent(ctx, opts)

	deadline := time.Now().Add(opts.StartTimeout)
	for time.Now().Before(deadline) {
		if running, _ := p.engineRunning(ctx, opts); running {
			p.logger().Info("engine socket is up", "distro", opts.Distro)
			// Shared only once the socket exists: a bind mount of a missing
			// file cannot be made, and the share is re-done on every start
			// because the previous engine's bind went stale with it.
			p.shareEngineSocket(ctx, opts)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	// Include the daemon's own last words; without them this is undiagnosable.
	log, _ := p.wsl().Exec(ctx, opts.Distro, "root", "tail", "-30", "/var/log/dockerd.log")
	return fmt.Errorf("dockerd did not create %s within %s. Last log lines:\n%s",
		EngineSocket, opts.StartTimeout, log)
}

// SharedSocketPath is where the engine socket is bind-mounted for other WSL
// distros (#42): /mnt/wsl is a tmpfs shared VM-wide across every distro, and a
// bind mount of the socket file shares the live inode, which a symlink cannot
// (it would resolve in the reader's own namespace, where /var/run/docker.sock
// does not exist). Namespaced by distro name so a second install — the e2e
// suite runs one beside a real install — shares its own socket, not a
// collision.
func SharedSocketPath(distro string) string {
	return "/mnt/wsl/" + distro + "/docker.sock"
}

// shareEngineSocket publishes the engine socket at SharedSocketPath,
// best-effort: sharing is Desktop-parity plumbing for wsl-integrate, and its
// failure must not fail an engine start. The path travels as a positional
// parameter, never spliced into the script, so a hostile distro name cannot
// inject shell. A stale share from a previous engine run (the bind outlives
// the distro in the VM's tmpfs) is unmounted first.
//
// The socket is chmod'd 0666 so the *ordinary* user in an integrated distro
// can reach it — dockerd creates it 0660 root:root, and UIDs/groups do not
// map reliably across distros, so group-based access is unreliable where a
// world bit is not. This does not widen the trust boundary: the engine is
// already reachable by any process in the user's Windows session through the
// named pipe, and /mnt/wsl is shared only among that user's own distros in
// their own utility VM. chmod targets the shared inode, so the engine's own
// /var/run/docker.sock (which only root touches inside the engine distro) is
// affected too, which is immaterial there.
func (p *Provisioner) shareEngineSocket(ctx context.Context, opts Options) {
	const script = `dir=$(dirname "$1") && mkdir -p "$dir" && ` +
		`{ umount "$1" 2>/dev/null || true; } && rm -f "$1" && touch "$1" && ` +
		`mount --bind /var/run/docker.sock "$1" && chmod 0666 "$1"`
	if _, err := p.wsl().Exec(ctx, opts.Distro, "root",
		"sh", "-c", script, "sh", SharedSocketPath(opts.Distro)); err != nil {
		p.logger().Debug("engine socket not shared to /mnt/wsl", "error", err)
	}
}

// agentStartCmd is what launches hawser-agent (#40), guarded three ways: a
// rootfs that predates the agent has nothing to start (the socat relay stays
// the transport), an agent already running must not be doubled, and `exec`
// keeps the process tree flat. Never fatal by design — the engine is fully
// usable without the vsock path.
//
// The agent's -socket is passed explicitly as EngineSocket (#92): both
// transports must target the same engine socket. A host-side `--socket`
// override on `hawser proxy` steers only the socat fallback and cannot reach
// the agent (which runs in-distro and connects to dockerd's real socket
// there), so binding the agent to the same constant keeps the two from
// silently diverging.
const agentStartCmd = "command -v hawser-agent >/dev/null 2>&1 || exit 0; " +
	"pgrep -x hawser-agent >/dev/null 2>&1 && exit 0; " +
	"exec hawser-agent -socket " + EngineSocket + " >>/var/log/hawser-agent.log 2>&1"

func (p *Provisioner) startAgent(ctx context.Context, opts Options) {
	if _, err := p.wsl().Start(ctx, opts.Distro, "root", "sh", "-c", agentStartCmd); err != nil {
		p.logger().Debug("hawser-agent not started", "error", err)
	}
}

// enginePing asks dockerd itself, over its socket, using only what the rootfs
// already ships (socat): a stale socket file left by a crashed dockerd must
// read as DOWN, not up (#82 — `test -S` said "running" forever after an
// OOM-kill, so the supervisor never repaired and status lied).
const enginePing = `printf 'GET /_ping HTTP/1.1\r\nHost: hawser\r\nConnection: close\r\n\r\n'` +
	` | socat -t 2 - UNIX-CONNECT:` + EngineSocket

func (p *Provisioner) engineRunning(ctx context.Context, opts Options) (bool, error) {
	// Never exec into the distro without knowing it is already running:
	// wsl.exe BOOTS a stopped distro to run the command (#82), so a health
	// probe against an idle-stopped engine would revive the distro/VM every
	// few seconds and defeat the RAM reclaim idle-stop exists for. Listing is
	// a pure host-side query.
	distros, err := p.wsl().List(ctx)
	if err != nil {
		return false, err
	}
	alive := false
	for _, d := range distros {
		if d.Name == opts.Distro && strings.EqualFold(d.State, "Running") {
			alive = true
			break
		}
	}
	if !alive {
		return false, nil
	}

	out, err := p.wsl().Exec(ctx, opts.Distro, "root", "sh", "-c", enginePing)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "200 OK"), nil
}

// Uninstall removes the distro and Hawser's own state, and nothing else.
//
// Best-effort by design: a partially installed machine must still come clean,
// so a missing distro or absent state directory is not an error. Errors are
// collected and reported together.
func (p *Provisioner) Uninstall(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()

	// A recorded manifest is more trustworthy than the caller's options: it says
	// where this install actually put things.
	if m, err := p.ReadManifest(opts); err == nil {
		if m.Distro != "" {
			opts.Distro = m.Distro
		}
		if m.DataDir != "" {
			opts.DataDir = m.DataDir
		}
	}

	var errs []error

	distros, err := p.wsl().List(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing distros: %w", err))
	}
	registered := false
	for _, d := range distros {
		if d.Name == opts.Distro {
			registered = true
		}
	}

	if registered {
		// Clear the /mnt/wsl share first, while the distro can still run a
		// command: after unregister the dead socket file would sit in the
		// VM's shared tmpfs until the next VM restart. Best-effort — a
		// stopped distro would be booted just to clean a tmpfs entry.
		const unshare = `umount "$1" 2>/dev/null; rm -rf "$(dirname "$1")"`
		p.wsl().Exec(ctx, opts.Distro, "root",
			"sh", "-c", unshare, "sh", SharedSocketPath(opts.Distro))

		p.logger().Info("terminating distro", "distro", opts.Distro)
		if err := p.wsl().Terminate(ctx, opts.Distro); err != nil {
			// Not fatal: unregister stops it anyway.
			p.logger().Warn("terminate failed, continuing", "error", err)
		}
		p.logger().Info("unregistering distro", "distro", opts.Distro)
		if err := p.wsl().Unregister(ctx, opts.Distro); err != nil {
			errs = append(errs, fmt.Errorf("unregistering %s: %w", opts.Distro, err))
		}
	} else {
		p.logger().Info("distro not registered, nothing to unregister", "distro", opts.Distro)
	}

	// Only paths Hawser created. DataDir is removed because wsl --unregister
	// deletes the VHDX but leaves the directory.
	if err := os.RemoveAll(opts.DataDir); err != nil {
		errs = append(errs, fmt.Errorf("removing %s: %w", opts.DataDir, err))
	}
	if err := os.Remove(p.manifestPath(opts)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("removing manifest: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("uninstall completed with errors: %w", errsJoin(errs))
	}
	return nil
}

func errsJoin(errs []error) error {
	msg := ""
	for i, e := range errs {
		if i > 0 {
			msg += "; "
		}
		msg += e.Error()
	}
	return fmt.Errorf("%s", msg)
}

func (p *Provisioner) manifestPath(opts Options) string {
	return filepath.Join(opts.withDefaults().StateDir, "manifest.json")
}

func (p *Provisioner) writeManifest(opts Options, m *Manifest) error {
	path := p.manifestPath(opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	// Atomic write (#93): a crash mid-write left a truncated manifest.json,
	// after which ReadManifest errors and the supervisor exits "no install
	// found" until a reinstall. Temp-plus-rename means a reader sees either
	// the old file or the whole new one, never a partial.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("committing manifest: %w", err)
	}
	return nil
}

// ReadManifest returns what a previous install recorded.
func (p *Provisioner) ReadManifest(opts Options) (*Manifest, error) {
	b, err := os.ReadFile(p.manifestPath(opts))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	return &m, nil
}

// EngineRunning reports whether the engine socket answers inside the distro.
func (p *Provisioner) EngineRunning(ctx context.Context, opts Options) bool {
	opts = opts.withDefaults()
	running, _ := p.engineRunning(ctx, opts)
	return running
}

// StopEngine terminates the engine's own distro — and nothing else. This is
// the only stop primitive Hawser has on purpose: `wsl --shutdown` stops every
// distro on the machine, including Docker Desktop's and the user's own, and is
// never called (PLAN §02; the coexistence note on #35).
func (p *Provisioner) StopEngine(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()
	p.logger().Info("terminating distro", "distro", opts.Distro)
	return p.wsl().Terminate(ctx, opts.Distro)
}
