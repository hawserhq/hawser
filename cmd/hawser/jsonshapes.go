package main

import (
	"github.com/zcsizmadia/hawser/internal/remote"
	"github.com/zcsizmadia/hawser/internal/runner"
)

// The --json shapes: the CLI contract that machine consumers — the VS Code
// extension, CI scripts, fleet tooling — depend on (#137). They are named types
// so jsonshapes_test.go can pin every key. Changes are additive only; exit codes
// keep the same meaning as the human output (0 ok, 1 error, 2 usage, 3 not
// installed / not found), and a non-zero exit still emits the JSON where there is
// something to say. Documented in docs/cli-json.md.

// statusJSON is `hawser status --json`.
type statusJSON struct {
	Installed  bool    `json:"installed"`
	Distro     string  `json:"distro,omitempty"`
	StateDir   string  `json:"stateDir"`
	Supervisor string  `json:"supervisor"` // running | stopped
	Engine     string  `json:"engine"`     // running | idle | stopped
	Desired    string  `json:"desired"`    // running | stopped
	Profile    string  `json:"profile,omitempty"`
	GPU        gpuJSON `json:"gpu"`
}

// gpuJSON is GPU passthrough state (#83). visible and specInstalled are probed
// only while the engine is running AND gpu is enabled — probing would boot a
// stopped distro, which status must never do (#82) — and probed says whether
// they are authoritative.
type gpuJSON struct {
	Enabled       bool `json:"enabled"`
	Probed        bool `json:"probed"`
	Visible       bool `json:"visible"`
	SpecInstalled bool `json:"specInstalled"`
}

// cliStatusJSON is `hawser cli status --json` (#66).
type cliStatusJSON struct {
	Arch         string        `json:"arch"`
	BinDir       string        `json:"binDir"`
	OnPath       bool          `json:"onPath"`
	ActiveDocker string        `json:"activeDocker,omitempty"`
	Tools        []cliToolJSON `json:"tools"`
}

// cliToolJSON is one bundled tool. Available is whether the manifest publishes
// it for the host arch at all (the docker CLI has no Windows arm64 build).
type cliToolJSON struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Role      string `json:"role"` // cli | plugin | helper
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Available bool   `json:"available"`
}

// configListJSON is `hawser config --json`. Engine is null when no engine is
// installed and {} when one is installed with nothing set — different answers.
type configListJSON struct {
	Settings map[string]string `json:"settings"`
	Engine   map[string]string `json:"engine"`
}

// profileListJSON is `hawser profile --json`; profiles is always an array.
type profileListJSON struct {
	Active   string             `json:"active,omitempty"`
	Profiles []profileEntryJSON `json:"profiles"`
}

type profileEntryJSON struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// snapshotRestoredJSON and snapshotDeletedJSON are the results of those verbs.
// `snapshot save --json` emits the snapshot.Meta, `snapshot list --json` an
// array of them (always an array, never null).
type snapshotRestoredJSON struct {
	Restored string `json:"restored"`
}

type snapshotDeletedJSON struct {
	Deleted string `json:"deleted"`
}

// remoteListJSON is `hawser remote list --json` (#138). current is "local" when
// docker is on the hawser context, a remote's name when on hawser-<name>, and ""
// when docker is on some other context entirely.
type remoteListJSON struct {
	Current string            `json:"current"`
	Remotes []remoteEntryJSON `json:"remotes"`
}

// remoteEntryJSON is a registered remote plus whether docker is on it now.
type remoteEntryJSON struct {
	remote.Info
	Current bool `json:"current"`
}

// remoteTestJSON is `hawser remote test <name> --json`.
type remoteTestJSON struct {
	Name          string `json:"name"`
	ServerVersion string `json:"serverVersion"`
	Millis        int64  `json:"ms"`
}

// traceJSON is `hawser audit trace --json -- <cmd>` (#152): what the command
// did to the engine. actions counts audit events by action name; images and
// containers are the distinct ones touched (always arrays). exitCode is the
// traced command's own, which the process also exits with.
type traceJSON struct {
	Command    []string       `json:"command"`
	ExitCode   int            `json:"exitCode"`
	Millis     int64          `json:"ms"`
	Events     int            `json:"events"`
	Actions    map[string]int `json:"actions"`
	Images     []string       `json:"images"`
	Containers []string       `json:"containers"`
	Note       string         `json:"note,omitempty"`
}

// healthcheckJSON is `hawser healthcheck --json` (#146). ready is the verdict
// the exit code carries (0 ready, 1 not, 3 not installed); reason always says
// why, in words a runner log can show.
type healthcheckJSON struct {
	Installed  bool   `json:"installed"`
	Supervisor string `json:"supervisor"` // running | stopped
	Engine     string `json:"engine"`     // running | idle | stopped
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason"`
}

// logLineJSON is one line of `hawser logs --json` (#146): the same envelope for
// every source so a log shipper needs one pipeline. line is the raw record; a
// shipper that wants dockerd's logfmt or the audit JSON parses it further.
type logLineJSON struct {
	Source string `json:"source"` // supervisor | dockerd | audit
	Line   string `json:"line"`
}

// prewarmJSON is `hawser prewarm --json <file>` (#149). images is in list order
// and always an array; the exit code is 0 only when failed is 0.
type prewarmJSON struct {
	File        string             `json:"file"`
	Concurrency int                `json:"concurrency"`
	Pulled      int                `json:"pulled"`
	Failed      int                `json:"failed"`
	Millis      int64              `json:"ms"`
	Images      []prewarmImageJSON `json:"images"`
}

type prewarmImageJSON struct {
	Ref    string `json:"ref"`
	OK     bool   `json:"ok"`
	Millis int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// runnerCheckJSON is `hawser runner check --json` (#150). ready is the verdict
// the exit code carries (0 ready — warnings allowed — 1 not ready, 3 not
// installed); findings is always an array, each with a remedy when not ok.
type runnerCheckJSON struct {
	Ready    bool             `json:"ready"`
	Findings []runner.Finding `json:"findings"`
}

// resetJSON is `hawser reset --to <snapshot> --json` (#142): what the engine
// was reset to and how long the whole cycle took (verify, unregister, import,
// engine back) — the number a runner's clean-slate budget is measured against.
type resetJSON struct {
	Snapshot      string `json:"snapshot"`
	EngineVersion string `json:"engineVersion,omitempty"`
	Millis        int64  `json:"ms"`
}

// pruneJSON is `hawser prune --json` (#145): bytes reclaimed per step and in
// total. steps is always an array in plan order; error is omitted on success.
// Exit 0 only when failed is 0.
type pruneJSON struct {
	ReclaimedBytes int64           `json:"reclaimedBytes"`
	Failed         int             `json:"failed"`
	Steps          []pruneStepJSON `json:"steps"`
}

type pruneStepJSON struct {
	Name           string `json:"name"` // containers | images | volumes | build-cache
	ReclaimedBytes int64  `json:"reclaimedBytes"`
	Error          string `json:"error,omitempty"`
}

// compactJSONShape is `hawser compact --json` (#64). reclaimedBytes is the only
// honest measure of what happened: beforeBytes/afterBytes are the .vhdx's size
// ON DISK, and offeredBytes is what fstrim printed -- the disk's whole free
// extent, not space reclaimed, which is why it is named "offered". held is set
// when the disk could not be released, and pairs with exit code 11.
type compactJSONShape struct {
	Distro         string   `json:"distro"`
	Path           string   `json:"path"`
	Trimmed        bool     `json:"trimmed"`
	OfferedBytes   uint64   `json:"offeredBytes,omitempty"`
	BeforeBytes    uint64   `json:"beforeBytes"`
	AfterBytes     uint64   `json:"afterBytes"`
	ReclaimedBytes uint64   `json:"reclaimedBytes"`
	WaitedSeconds  float64  `json:"waitedSeconds"`
	Restarted      bool     `json:"restarted"`
	DryRun         bool     `json:"dryRun"`
	Steps          []string `json:"steps,omitempty"`
	Held           []string `json:"held,omitempty"`
}

// engineListJSON is `hawser engine list --json` (#65): what this build can
// install, what is installed, and what a rollback would return to. available
// is always an array; ref is the revisioned label (29.7.2-4), which is the
// unit an upgrade moves between, while version is only the dockerd version.
type engineListJSON struct {
	Installed string            `json:"installed,omitempty"`
	Previous  string            `json:"previous,omitempty"`
	Available []engineEntryJSON `json:"available"`
}

type engineEntryJSON struct {
	Ref     string `json:"ref"`
	Version string `json:"version"`
	Default bool   `json:"default"`
	// Published is false for a manifest entry with no checksum yet: a
	// placeholder that cannot be installed.
	Published bool `json:"published"`
}

// engineUpgradeJSON is `hawser engine upgrade|rollback --json` (#65).
// rolledBack true with a non-zero exit is the interesting case: the upgrade
// failed and the previous engine was put back, so the engine is up.
type engineUpgradeJSON struct {
	From          string   `json:"from,omitempty"`
	To            string   `json:"to"`
	Replaced      []string `json:"replaced,omitempty"`
	EngineVersion string   `json:"engineVersion,omitempty"`
	RolledBack    bool     `json:"rolledBack"`
	DryRun        bool     `json:"dryRun"`
	Steps         []string `json:"steps,omitempty"`
}
