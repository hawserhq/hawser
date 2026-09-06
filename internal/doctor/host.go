package doctor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/supervise"
	"github.com/zcsizmadia/hawser/internal/version"
	"github.com/zcsizmadia/hawser/internal/wsl"
)

// Facts is everything the checks read, gathered once from the machine. Checks
// are pure functions of a Facts value, so a test builds the struct by hand and
// never touches WSL, PATH, the registry, or an engine.
type Facts struct {
	// StateDir is Hawser's resolved state directory.
	StateDir string
	// AppVersion is Hawser's own build version.
	AppVersion string

	// WSL is the host's WSL status. WSLErr is set when querying it failed.
	WSL    wsl.Status
	WSLErr string

	// Report is the `hawser version` picture: docker binaries on PATH, active
	// context, negotiated API version, and the install manifest. Never nil after
	// Gather.
	Report *version.Report

	// EngineReachable is true when the engine answers in its distro. EngineIdle
	// is true when it is down by design (idle timeout), which is healthy.
	EngineReachable bool
	EngineIdle      bool
	// Desired is the supervisor's desired engine state ("running"/"stopped").
	Desired string
	// SupervisorHeld is true when the single-instance lock is held, i.e. a
	// supervisor is running.
	SupervisorHeld bool

	// CredHelpers are the docker credential helpers the CLI config references,
	// each annotated with whether its binary resolves on PATH.
	CredHelpers []CredHelper

	// Disk describes free space on the volume holding the engine's data.
	Disk DiskInfo

	// Session0 is advisory guidance about unattended (no-logon) operation; see
	// checkSession0.
	Session0 Session0Info
}

// CredHelper is one docker credential helper referenced by the CLI config, and
// whether its executable can be found. A referenced-but-missing helper is what
// broke `hawser migrate` live (docker-credential-wincred absent from PATH).
type CredHelper struct {
	// Name is the helper's short name, e.g. "desktop", "wincred".
	Name string `json:"name"`
	// Source says where it was configured: "credsStore" or "credHelpers[<reg>]".
	Source string `json:"source"`
	// Binary is the executable docker will exec, e.g. "docker-credential-wincred".
	Binary string `json:"binary"`
	// Resolved is true when Binary was found on PATH; Path is where.
	Resolved bool   `json:"resolved"`
	Path     string `json:"path,omitempty"`
}

// DiskInfo is free/total space on a volume, in bytes. Err is set when the query
// failed (a non-Windows host, or a path that does not exist yet).
type DiskInfo struct {
	Path       string `json:"path"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
	Err        string `json:"err,omitempty"`
}

// Session0Info carries the advisory session-0 guidance. Verifying the account
// right reliably needs elevation, so doctor explains it rather than asserting a
// value it may not be able to read as a standard user.
type Session0Info struct {
	// AutostartConfigured is true when a logon autostart is registered; it is
	// the common path and makes the session-0 note purely informational.
	AutostartConfigured bool `json:"autostartConfigured"`
}

// GatherOptions locates what Gather needs.
type GatherOptions struct {
	StateDir   string
	HawserBin  string
	AppVersion string
	// AutostartConfigured reports whether logon autostart is set up; supplied by
	// the caller because it lives in an OS-specific package.
	AutostartConfigured bool
}

// Gather reads the machine into Facts, degrading gracefully: doctor is what you
// run when things are broken, so a component that cannot be read is recorded as
// unknown rather than aborting the run.
func Gather(ctx context.Context, opts GatherOptions) Facts {
	stateDir := opts.StateDir
	pOpts := provision.Options{StateDir: stateDir}
	p := &provision.Provisioner{}
	w := wsl.NewLocal()

	f := Facts{
		StateDir:   stateDir,
		AppVersion: opts.AppVersion,
		Desired:    string(supervise.ReadDesired(stateDir)),
		Session0:   Session0Info{AutostartConfigured: opts.AutostartConfigured},
	}

	if st, err := w.Status(ctx); err != nil {
		f.WSLErr = err.Error()
	} else {
		f.WSL = st
	}

	f.Report = (&version.Collector{
		App:         opts.AppVersion,
		Env:         version.Env{HawserBin: opts.HawserBin},
		WSL:         w,
		Provisioner: p,
		Options:     pOpts,
	}).Collect(ctx)

	// Reachability is a host-side probe that never boots a stopped distro (#82).
	if f.Report.Engine.Installed {
		pOpts.Distro = f.Report.Engine.Distro
		f.EngineReachable = p.EngineRunning(ctx, pOpts)
		f.EngineIdle = supervise.ReadEngineState(stateDir) == supervise.EngineIdle
	}
	f.SupervisorHeld = supervise.Held(stateDir)

	f.CredHelpers = discoverCredHelpers(dockerConfigPath(), execLookPath)
	f.Disk = diskInfo(engineDataDir(stateDir))

	return f
}

// dockerConfigPath returns the docker CLI config location, honoring DOCKER_CONFIG.
func dockerConfigPath() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return filepath.Join(d, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

// engineDataDir is the volume whose free space matters: the distro's VHDX grows
// there. It mirrors provision's default (StateDir\distro) without importing its
// unexported helper.
func engineDataDir(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "distro")
}

func execLookPath(name string) (string, error) { return exec.LookPath(name) }

// discoverCredHelpers reads the docker CLI config and reports each referenced
// credential helper with whether its binary resolves. Pure but for the injected
// lookPath and the file read, so the parsing is unit-tested via parseCredHelpers.
func discoverCredHelpers(configPath string, lookPath func(string) (string, error)) []CredHelper {
	if configPath == "" {
		return nil
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		return nil // no config, or unreadable: nothing configured to check
	}
	return parseCredHelpers(b, lookPath)
}

// parseCredHelpers turns a docker config.json into the list of helpers it wires
// up, deduplicated, each resolved against lookPath.
func parseCredHelpers(configJSON []byte, lookPath func(string) (string, error)) []CredHelper {
	var cfg struct {
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil
	}

	seen := map[string]bool{}
	var out []CredHelper
	add := func(name, source string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		bin := "docker-credential-" + name
		h := CredHelper{Name: name, Source: source, Binary: bin}
		if path, err := lookPath(bin); err == nil {
			h.Resolved, h.Path = true, path
		}
		out = append(out, h)
	}

	add(cfg.CredsStore, "credsStore")
	for registry, name := range cfg.CredHelpers {
		add(name, "credHelpers["+registry+"]")
	}
	return out
}
