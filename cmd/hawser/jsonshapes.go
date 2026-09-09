package main

import "github.com/zcsizmadia/hawser/internal/remote"

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
