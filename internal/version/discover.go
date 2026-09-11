// Package version answers "which docker am I actually running?" as a command
// rather than an archaeology project (PLAN §04).
//
// The interesting question is not Skrog's own version but which docker.exe
// wins on PATH and whose it is. A machine that has had Docker Desktop, winget,
// Chocolatey and Skrog on it can easily have four, and a stale one resolving
// first while the skrog context is active produces symptoms that look like
// Skrog is broken.
package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Origin says who a docker.exe belongs to.
type Origin string

const (
	OriginSkrog         Origin = "skrog"
	OriginDockerDesktop Origin = "docker-desktop"
	OriginRancher       Origin = "rancher-desktop"
	OriginWinget        Origin = "winget"
	OriginChocolatey    Origin = "chocolatey"
	OriginScoop         Origin = "scoop"
	OriginUnknown       Origin = "unknown"
)

// Binary is one docker.exe found on PATH.
type Binary struct {
	Path   string `json:"path"`
	Origin Origin `json:"origin"`
	// First is true for the one that actually runs when the user types `docker`.
	First bool `json:"first"`
}

// classify attributes a docker.exe to its owner by install location.
//
// Path-based rather than signature-based on purpose: it needs no I/O, works for
// a binary the user cannot execute, and the answer is only ever advisory — the
// authoritative facts are the path itself, which is always reported.
func classify(path, skrogBin string) Origin {
	p := strings.ToLower(filepath.ToSlash(path))

	if skrogBin != "" {
		hb := strings.ToLower(filepath.ToSlash(skrogBin))
		if strings.HasPrefix(p, strings.TrimSuffix(hb, "/")+"/") {
			return OriginSkrog
		}
	}

	switch {
	case strings.Contains(p, "/docker/docker/resources/bin"):
		return OriginDockerDesktop
	case strings.Contains(p, "/rancher-desktop/"):
		return OriginRancher
	case strings.Contains(p, "/microsoft/winget/"):
		return OriginWinget
	case strings.Contains(p, "/chocolatey/"):
		return OriginChocolatey
	case strings.Contains(p, "/scoop/"):
		return OriginScoop
	case strings.Contains(p, "/skrog/"):
		return OriginSkrog
	default:
		return OriginUnknown
	}
}

// Env supplies the environment for discovery, so tests need no real PATH.
type Env struct {
	// PathVar is the raw PATH value. Empty reads os.Getenv("PATH").
	PathVar string
	// PathExt is the raw PATHEXT value; only used to decide executable suffixes.
	PathExt string
	// SkrogBin is Skrog's own bin directory, used to recognize its binaries.
	SkrogBin string
	// Getenv reads other variables. Empty uses os.Getenv.
	Getenv func(string) string
	// Stat reports whether a path exists. Empty uses os.Stat.
	Stat func(string) error
	// UserConfigDir locates ~/.docker. Empty uses os.UserHomeDir.
	DockerConfigDir string
}

func (e Env) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.Getenv(k)
}

func (e Env) stat(p string) error {
	if e.Stat != nil {
		return e.Stat(p)
	}
	_, err := os.Stat(p)
	return err
}

func (e Env) pathVar() string {
	if e.PathVar != "" {
		return e.PathVar
	}
	return e.getenv("PATH")
}

// FindDockerBinaries returns every docker executable on PATH, in resolution
// order. The first entry is the one that runs.
func FindDockerBinaries(env Env) []Binary {
	var out []Binary
	seen := map[string]bool{}

	for _, dir := range filepath.SplitList(env.pathVar()) {
		if dir == "" {
			continue
		}
		// One entry per directory: PATH resolution picks a single file per
		// directory, so reporting both docker.exe and an extensionless docker
		// from the same place (as Docker Desktop ships) would be noise dressed
		// up as a second installation.
		if seen[strings.ToLower(dir)] {
			continue
		}
		for _, name := range dockerNames(env) {
			candidate := filepath.Join(dir, name)
			if err := env.stat(candidate); err != nil {
				continue
			}
			seen[strings.ToLower(dir)] = true
			out = append(out, Binary{
				Path:   candidate,
				Origin: classify(candidate, env.SkrogBin),
				First:  len(out) == 0,
			})
			break
		}
	}
	return out
}

// dockerNames returns the filenames to look for. Windows resolves by PATHEXT;
// elsewhere (tests, and any future host) the bare name is correct.
func dockerNames(env Env) []string {
	exts := env.PathExt
	if exts == "" {
		exts = env.getenv("PATHEXT")
	}
	if exts == "" {
		return []string{"docker.exe", "docker"}
	}
	names := make([]string, 0, 4)
	for _, e := range filepath.SplitList(exts) {
		if e == "" {
			continue
		}
		if strings.EqualFold(e, ".EXE") || strings.EqualFold(e, ".COM") {
			names = append(names, "docker"+strings.ToLower(e))
		}
	}
	if len(names) == 0 {
		names = append(names, "docker.exe")
	}
	return append(names, "docker")
}

// DockerContext reports the active docker context and how it was selected.
//
// Order matters and mirrors the docker CLI: DOCKER_HOST overrides everything
// (and means no context is in play), then DOCKER_CONTEXT, then the CLI config's
// currentContext, then the implicit default.
func DockerContext(env Env) (name, source string) {
	if h := env.getenv("DOCKER_HOST"); h != "" {
		return "", "DOCKER_HOST=" + h
	}
	if c := env.getenv("DOCKER_CONTEXT"); c != "" {
		return c, "DOCKER_CONTEXT"
	}

	dir := env.DockerConfigDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "default", "implicit default"
		}
		dir = filepath.Join(home, ".docker")
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return "default", "implicit default"
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.CurrentContext == "" {
		return "default", "implicit default"
	}
	return cfg.CurrentContext, "docker config.json"
}
