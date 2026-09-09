package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/dockercli"
	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/remote"
	"github.com/zcsizmadia/hawser/internal/supervise"
	"github.com/zcsizmadia/hawser/internal/version"
	"github.com/zcsizmadia/hawser/internal/vpnfingerprint"
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

	// Proxy is the configured engine proxy URL; ImportHostCAs whether host CA
	// trust is on (#62).
	Proxy         string
	ImportHostCAs bool

	// VPNs are the corporate VPN clients detected from the host's active network
	// adapters, each with its recommended connectivity settings (#63).
	VPNs []vpnfingerprint.Match

	// CLI is the bundled docker CLI's install/PATH state (#66).
	CLI CLIStatus

	// GPU is the NVIDIA GPU-passthrough state (#83).
	GPU GPUStatus

	// Remotes are the registered remote engines (#138), so checkContext can tell
	// "docker is on a remote we know" from "docker is aimed somewhere odd".
	Remotes []remote.Info
}

// GPUStatus describes GPU passthrough (#83): whether it is turned on in config,
// and — only when the engine is already running, so doctor never boots it (#82)
// — whether the distro actually sees the GPU and the CDI spec is installed.
type GPUStatus struct {
	// EngineInstalled gates the check; GPU is meaningless without an engine.
	EngineInstalled bool `json:"engineInstalled"`
	// ConfigEnabled is the `gpu` setting (`hawser enable-gpu`).
	ConfigEnabled bool `json:"configEnabled"`
	// Probed is true when the running engine was queried for the two below;
	// false means the engine was down, so they are not authoritative.
	Probed bool `json:"probed"`
	// Visible is true when /dev/dxg and the WSL CUDA library are present.
	Visible bool `json:"visible"`
	// SpecInstalled is true when the CDI spec is present in the distro.
	SpecInstalled bool `json:"specInstalled"`
}

// CLIStatus describes the bundled docker CLI (#66): whether it is installed,
// where, whether its directory is on the user PATH, and which docker the shell
// actually resolves — so checkCLI can tell "installed and active" from "installed
// but Docker Desktop still wins".
type CLIStatus struct {
	Installed    bool   `json:"installed"`
	BinDir       string `json:"binDir"`
	OnPath       bool   `json:"onPath"`
	ActiveDocker string `json:"activeDocker,omitempty"`
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
		f.GPU.EngineInstalled = true
	}
	f.SupervisorHeld = supervise.Held(stateDir)

	f.CredHelpers = discoverCredHelpers(dockerConfigPath(), execLookPath)
	f.Disk = diskInfo(engineDataDir(stateDir))

	if c, err := config.Load(stateDir); err == nil {
		f.Proxy = c.Proxy
		f.ImportHostCAs = c.ImportHostCAs
		f.GPU.ConfigEnabled = c.GPU
	}

	// GPU distro probes only when the engine is already up: GPUAvailable uses
	// wsl exec, which would boot a stopped distro, and doctor must not (#82).
	if f.EngineReachable {
		f.GPU.Probed = true
		f.GPU.Visible = p.GPUAvailable(ctx, pOpts)
		f.GPU.SpecInstalled = p.GPUSpecInstalled(ctx, pOpts)
	}

	f.VPNs = vpnfingerprint.Detect(gatherAdapters(ctx))
	f.CLI = gatherCLIStatus(stateDir)
	// Best-effort: an unreadable remotes dir means "no remotes", not a failure.
	f.Remotes, _ = (&remote.Manager{StateDir: stateDir}).List()

	return f
}

// gatherCLIStatus reads the bundled docker CLI's install and PATH state (#66).
func gatherCLIStatus(stateDir string) CLIStatus {
	s := CLIStatus{}
	if stateDir == "" {
		return s
	}
	s.BinDir = filepath.Join(stateDir, "bin")
	if _, err := os.Stat(filepath.Join(s.BinDir, "docker.exe")); err == nil {
		s.Installed = true
	}
	if onPath, err := dockercli.UserPathContains(s.BinDir); err == nil {
		s.OnPath = onPath
	}
	if active, err := execLookPath("docker"); err == nil {
		s.ActiveDocker = active
	}
	return s
}

// gatherAdapters reads the host's network adapters via Get-NetAdapter (a
// standard-user cmdlet, no elevation). It degrades to nil off Windows or when
// the cmdlet is unavailable: VPN detection is advisory, so a missing reading is
// simply "no VPN detected", never an error.
func gatherAdapters(ctx context.Context) []vpnfingerprint.Adapter {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"Get-NetAdapter | Select-Object Name,InterfaceDescription,Status | ConvertTo-Json -Compress")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseAdapters(out)
}

// parseAdapters turns Get-NetAdapter's JSON into adapters. ConvertTo-Json emits
// a bare object for a single adapter and an array for several, so both shapes
// are handled. Pure, so the parsing is unit-tested without a host.
func parseAdapters(jsonOut []byte) []vpnfingerprint.Adapter {
	type raw struct {
		Name                 string `json:"Name"`
		InterfaceDescription string `json:"InterfaceDescription"`
		Status               string `json:"Status"`
	}
	trimmed := bytes.TrimSpace(jsonOut)
	if len(trimmed) == 0 {
		return nil
	}
	var many []raw
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &many); err != nil {
			return nil
		}
	} else {
		var one raw
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return nil
		}
		many = []raw{one}
	}
	out := make([]vpnfingerprint.Adapter, 0, len(many))
	for _, r := range many {
		out = append(out, vpnfingerprint.Adapter{
			Name:        r.Name,
			Description: r.InterfaceDescription,
			// Get-NetAdapter reports "Up" for a connected adapter.
			Up: r.Status == "Up",
		})
	}
	return out
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
