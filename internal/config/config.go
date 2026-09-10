// Package config is Hawser's user-tunable settings: a small JSON file in the
// state directory, edited through `hawser config` rather than by hand so
// every value is validated on the way in.
//
// Settings are read fresh at each use (the supervisor reads per tick), so a
// `hawser config set` takes effect without restarting anything.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zcsizmadia/hawser/internal/wslconfig"
)

// KeyIdleTimeout is how long the bridge must be quiet (no open connections,
// no running containers) before the supervisor stops the engine to return
// its RAM. "off" (the default) disables idle stops entirely.
const KeyIdleTimeout = "idle-timeout"

// Lifecycle hook keys (#70): each holds a path to an executable the supervisor
// runs on the named engine event, time-bounded and best-effort — a failing or
// slow hook is logged but never blocks the lifecycle. Empty (the default) means
// no hook. HookKeys enumerates them; the supervisor reads the path for an event
// as "hook." + event.
const (
	KeyHookPostStart  = "hook.post-start"   // engine started (recovery or first start)
	KeyHookPreStop    = "hook.pre-stop"     // engine about to stop on `hawser stop`
	KeyHookOnIdleStop = "hook.on-idle-stop" // engine stopped by the idle timeout
	KeyHookOnWake     = "hook.on-wake"      // engine cold-started on demand
)

// HookKeys lists the hook config keys, in lifecycle order.
func HookKeys() []string {
	return []string{KeyHookPostStart, KeyHookPreStop, KeyHookOnIdleStop, KeyHookOnWake}
}

// KeyAudit toggles the container-affecting API audit log (#121). "on" writes a
// JSON-lines record of pulls, container create/start/stop/remove, exec and
// builds to audit.log in the state dir; "off" (the default) disables it.
// Changing it takes effect on the next `hawser restart`.
const KeyAudit = "audit"

// Corporate-network keys (#62), applied to the engine on the next `hawser
// restart`. NetworkKeys enumerates them.
const (
	// KeyProxy is the HTTP(S) proxy URL dockerd uses for registry pulls.
	KeyProxy = "network.proxy"
	// KeyNoProxy is the comma-separated proxy bypass list.
	KeyNoProxy = "network.no-proxy"
	// KeyImportHostCAs, when on, trusts the host's root CA store inside the
	// engine — the fix for a TLS-inspecting corporate proxy.
	KeyImportHostCAs = "network.import-host-cas"
)

// NetworkKeys lists the corporate-network keys.
func NetworkKeys() []string { return []string{KeyProxy, KeyNoProxy, KeyImportHostCAs} }

// KeyGPU, when on, installs the NVIDIA CDI spec in the engine on every start so
// containers can use the GPU (#83). "off" (the default) removes it. Set through
// `hawser enable-gpu` rather than by hand, since that also verifies the GPU is
// visible and restarts the engine.
const KeyGPU = "gpu"

// KeyVerifySignature, when on, checks the rootfs Sigstore signature before it
// is imported, in addition to the always-enforced SHA-256 pin (#147). Opt-in:
// it needs cosign on PATH, and an air-gapped install has no transparency log
// to reach.
const KeyVerifySignature = "install.verify-signature"

// WSL VM sizing (#148). These live in the GLOBAL ~/.wslconfig, which every
// WSL2 distro on the machine shares -- so setting one here records an
// intention, and `hawser wsl-config apply` is what writes it, after showing
// the diff. Nothing propagates on its own.
const (
	KeyWSLMemory            = "wsl.memory"
	KeyWSLProcessors        = "wsl.processors"
	KeyWSLSwap              = "wsl.swap"
	KeyWSLAutoMemoryReclaim = "wsl.auto-memory-reclaim"
)

// WSLKeys lists the sizing keys, and maps each to its .wslconfig name.
var WSLKeys = map[string]string{
	KeyWSLMemory:            "memory",
	KeyWSLProcessors:        "processors",
	KeyWSLSwap:              "swap",
	KeyWSLAutoMemoryReclaim: "autoMemoryReclaim",
}

// KeyDiskWarnBelow is the free-space floor on the engine data volume under
// which `hawser doctor` warns (#145) — a size such as 10GB or 8GiB. Empty means
// the built-in 5 GiB. Runners with small disks raise it so a full volume is
// flagged before pulls start failing.
const KeyDiskWarnBelow = "disk.warn-below"

// path is the settings file inside the state dir.
func path(stateDir string) string {
	return filepath.Join(stateDir, "config.json")
}

// Config is the parsed, validated view.
type Config struct {
	// IdleTimeout of zero means idle stops are off.
	IdleTimeout time.Duration
	// Audit enables the container-affecting API audit log.
	Audit bool
	// Proxy / NoProxy configure dockerd's registry proxy; ImportHostCAs trusts
	// the host root CA store inside the engine.
	Proxy         string
	NoProxy       string
	ImportHostCAs bool
	// GPU installs the NVIDIA CDI spec so containers can use the GPU (#83).
	GPU bool
	// VerifySignature checks the rootfs signature before import (#147).
	VerifySignature bool
	// DiskWarnBelow is the doctor free-space floor in bytes; 0 means default.
	DiskWarnBelow uint64
}

// Load parses the settings file. A missing file is the default configuration,
// not an error; a corrupt one is an error, because silently reverting a
// user's settings to defaults is worse than telling them.
func Load(stateDir string) (Config, error) {
	raw, err := load(stateDir)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if v, ok := raw[KeyIdleTimeout]; ok {
		d, err := parseIdleTimeout(v)
		if err != nil {
			return Config{}, fmt.Errorf("config %s: %w", KeyIdleTimeout, err)
		}
		c.IdleTimeout = d
	}
	c.Audit = raw[KeyAudit] == "on"
	c.Proxy = raw[KeyProxy]
	c.NoProxy = raw[KeyNoProxy]
	c.ImportHostCAs = raw[KeyImportHostCAs] == "on"
	c.GPU = raw[KeyGPU] == "on"
	c.VerifySignature = raw[KeyVerifySignature] == "on"
	if v := strings.TrimSpace(raw[KeyDiskWarnBelow]); v != "" {
		n, err := parseSize(v)
		if err != nil {
			return Config{}, fmt.Errorf("config %s: %w", KeyDiskWarnBelow, err)
		}
		c.DiskWarnBelow = n
	}
	return c, nil
}

func load(stateDir string) (map[string]string, error) {
	b, err := os.ReadFile(path(stateDir))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path(stateDir), err)
	}
	return m, nil
}

// validators parse-and-normalize each known key; unknown keys are refused so
// a typo ("idle-timout") fails loudly instead of configuring nothing.
var validators = map[string]func(string) (string, error){
	KeyIdleTimeout: func(v string) (string, error) {
		d, err := parseIdleTimeout(v)
		if err != nil {
			return "", err
		}
		if d == 0 {
			return "off", nil
		}
		return d.String(), nil
	},
	KeyHookPostStart:   validateHookPath,
	KeyHookPreStop:     validateHookPath,
	KeyHookOnIdleStop:  validateHookPath,
	KeyHookOnWake:      validateHookPath,
	KeyAudit:           validateOnOff,
	KeyProxy:           validateProxy,
	KeyNoProxy:         func(v string) (string, error) { return strings.TrimSpace(v), nil },
	KeyImportHostCAs:   validateOnOff,
	KeyGPU:             validateOnOff,
	KeyDiskWarnBelow:   validateSize,
	KeyVerifySignature: validateOnOff,

	// Validated the way WSL reads them, so a typo fails here rather than
	// silently sizing the VM as something else (#148).
	KeyWSLMemory:            func(v string) (string, error) { return wslconfig.Validate(wslconfig.KeyMemory, v) },
	KeyWSLProcessors:        func(v string) (string, error) { return wslconfig.Validate(wslconfig.KeyProcessors, v) },
	KeyWSLSwap:              func(v string) (string, error) { return wslconfig.Validate(wslconfig.KeySwap, v) },
	KeyWSLAutoMemoryReclaim: func(v string) (string, error) { return wslconfig.Validate(wslconfig.KeyAutoMemoryReclaim, v) },
}

// validateProxy accepts an http(s) URL, or empty to clear.
func validateProxy(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return "", fmt.Errorf("%q must be an http:// or https:// URL", v)
	}
	return v, nil
}

// validateOnOff normalizes a boolean-ish setting to "on" or "off".
func validateOnOff(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "yes":
		return "on", nil
	case "off", "false", "0", "no", "":
		return "off", nil
	}
	return "", fmt.Errorf("%q is not on or off", v)
}

// validateHookPath accepts a path to an existing file, or empty/"off" to clear
// the hook. Existence is checked at set time so a typo fails loudly here rather
// than silently doing nothing when the event fires.
func validateHookPath(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "off") {
		return "", nil
	}
	if _, err := os.Stat(v); err != nil {
		return "", fmt.Errorf("%q is not a readable path: %w", v, err)
	}
	return v, nil
}

func parseIdleTimeout(v string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (try 20m, 1h30m, or off)", v)
	}
	if d < 0 {
		return 0, fmt.Errorf("%q is negative", v)
	}
	if d < 10*time.Second {
		return 0, fmt.Errorf("%q is below the 10s minimum; use off to disable", v)
	}
	return d, nil
}

// Keys lists the settable keys, for help text.
func Keys() []string {
	ks := make([]string, 0, len(validators))
	for k := range validators {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Get returns the stored value for a known key, or its default.
func Get(stateDir, key string) (string, error) {
	if _, ok := validators[key]; !ok {
		return "", fmt.Errorf("unknown config key %q (known: %s)", key, strings.Join(Keys(), ", "))
	}
	raw, err := load(stateDir)
	if err != nil {
		return "", err
	}
	if v, ok := raw[key]; ok {
		return v, nil
	}
	return defaultFor(key), nil
}

func defaultFor(key string) string {
	switch key {
	case KeyIdleTimeout, KeyAudit, KeyImportHostCAs, KeyGPU, KeyVerifySignature:
		return "off"
	case KeyDiskWarnBelow:
		return "5GiB"
	}
	return ""
}

// commandHint answers a key that people reasonably reach for but which is an
// operation rather than a stored value. PLAN.md and #64 both spell the data
// dir as `config set data-dir`, so it will be typed; "unknown config key" is a
// dead end when the thing they want does exist, under a command.
func commandHint(key string) string {
	if key == "data-dir" {
		return "`data-dir` is not a stored setting: moving the engine data exports, " +
			"moves and re-imports the distro. Run `hawser relocate <new-directory>` " +
			"(`hawser relocate --help`)."
	}
	return ""
}

// Set validates and stores one key, atomically.
func Set(stateDir, key, value string) error {
	validate, ok := validators[key]
	if !ok {
		if h := commandHint(key); h != "" {
			return errors.New(h)
		}
		return fmt.Errorf("unknown config key %q (known: %s)", key, strings.Join(Keys(), ", "))
	}
	normalized, err := validate(value)
	if err != nil {
		return err
	}
	raw, err := load(stateDir)
	if err != nil {
		return err
	}
	raw[key] = normalized

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := path(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmp, path(stateDir)); err != nil {
		return fmt.Errorf("committing config: %w", err)
	}
	return nil
}

// All returns every stored setting plus defaults for unset known keys.
func All(stateDir string) (map[string]string, error) {
	raw, err := load(stateDir)
	if err != nil {
		return nil, err
	}
	for _, k := range Keys() {
		if _, ok := raw[k]; !ok {
			raw[k] = defaultFor(k)
		}
	}
	return raw, nil
}

// validateSize accepts a human size (10GB, 8GiB, 512MB) or empty to clear,
// storing the trimmed spelling the user wrote.
func validateSize(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if _, err := parseSize(v); err != nil {
		return "", err
	}
	return v, nil
}

var sizeUnits = map[string]float64{
	"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// parseSize turns "10GB" / "8GiB" / "512MB" into bytes (decimal or binary
// units, case-insensitive). A bare number is refused: a floor without a unit is
// a typo waiting to be 10 bytes.
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	if num == "" || unit == "" {
		return 0, fmt.Errorf("%q is not a size (try 10GB or 8GiB)", s)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%q is not a size (try 10GB or 8GiB)", s)
	}
	mult, ok := sizeUnits[unit]
	if !ok {
		return 0, fmt.Errorf("%q has an unknown unit %q (B, kB, MB, GB, TB, KiB, MiB, GiB, TiB)", s, unit)
	}
	return uint64(f * mult), nil
}
