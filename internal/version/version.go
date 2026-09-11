package version

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Report is the full component picture `skrog version` prints.
type Report struct {
	// App is Skrog's own version, stamped at build time.
	App string `json:"app"`
	// Engine describes the installed engine, from the install manifest.
	Engine EngineInfo `json:"engine"`
	// WSL is the host's WSL release, empty when it cannot be determined.
	WSL string `json:"wsl,omitempty"`
	// Docker lists every docker.exe on PATH, in resolution order.
	Docker []Binary `json:"docker"`
	// Context is the active docker context; empty when DOCKER_HOST overrides.
	Context string `json:"context"`
	// ContextSource says how Context was selected, which is half the answer
	// when someone's shell disagrees with their config.
	ContextSource string `json:"contextSource"`
	// APIVersion is the negotiated engine API version, when the engine
	// answered. Empty means it was not reachable, not that it is unknown.
	APIVersion string `json:"apiVersion,omitempty"`
	// Warnings flags conditions that make the setup behave unexpectedly.
	Warnings []string `json:"warnings,omitempty"`
}

// EngineInfo is what the install manifest recorded.
type EngineInfo struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Distro    string `json:"distro,omitempty"`
	Rootfs    string `json:"rootfsSha256,omitempty"`
	// WSLAtInstall is the WSL version present when Skrog was installed;
	// a difference from WSL means the host was updated since.
	WSLAtInstall string `json:"wslAtInstall,omitempty"`
}

// analyze fills in Warnings from the collected facts. Kept separate so the
// rules are testable without touching a filesystem.
func (r *Report) analyze() {
	first := r.firstDocker()

	// PATH shadowing: the skrog context is selected, but a foreign docker.exe
	// runs first. Commands still work — that binary talks to whatever its
	// context says — but the user is not driving Skrog, which looks like a
	// Skrog fault (PLAN §05, v0.3 doctor check).
	if r.Context == "skrog" && first != nil && first.Origin != OriginSkrog {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"the skrog context is active but %s (%s) resolves first on PATH; "+
				"put Skrog's bin directory earlier, or use its full path",
			first.Path, first.Origin))
	}

	if len(r.Docker) == 0 {
		r.Warnings = append(r.Warnings,
			"no docker.exe found on PATH; install Skrog's bundled CLI or add it to PATH")
	}

	if r.Engine.Installed && r.Engine.WSLAtInstall != "" && r.WSL != "" &&
		r.Engine.WSLAtInstall != r.WSL {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"WSL was %s when Skrog was installed and is %s now; "+
				"run `skrog doctor` if the engine misbehaves",
			r.Engine.WSLAtInstall, r.WSL))
	}

	if !r.Engine.Installed {
		r.Warnings = append(r.Warnings, "no engine installed; run `skrog install`")
	}
}

func (r *Report) firstDocker() *Binary {
	for i := range r.Docker {
		if r.Docker[i].First {
			return &r.Docker[i]
		}
	}
	return nil
}

// WriteText renders the report for a terminal.
func (r *Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "skrog\t%s\n", r.App)
	if r.Engine.Installed {
		fmt.Fprintf(tw, "engine\t%s\t(distro %s)\n", orUnknown(r.Engine.Version), r.Engine.Distro)
		if r.Engine.Rootfs != "" {
			fmt.Fprintf(tw, "rootfs\t%s\n", shortSHA(r.Engine.Rootfs))
		}
	} else {
		fmt.Fprintf(tw, "engine\tnot installed\n")
	}
	fmt.Fprintf(tw, "wsl\t%s\n", orUnknown(r.WSL))

	if r.Context != "" {
		fmt.Fprintf(tw, "context\t%s\t(%s)\n", r.Context, r.ContextSource)
	} else {
		fmt.Fprintf(tw, "context\t-\t(%s)\n", r.ContextSource)
	}
	if r.APIVersion != "" {
		fmt.Fprintf(tw, "api\t%s\n", r.APIVersion)
	}

	if len(r.Docker) == 0 {
		fmt.Fprintf(tw, "docker\tnone found on PATH\n")
	}
	for _, b := range r.Docker {
		marker := " "
		if b.First {
			marker = "*"
		}
		fmt.Fprintf(tw, "docker %s\t%s\t%s\n", marker, b.Origin, b.Path)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(r.Docker) > 1 {
		fmt.Fprintln(w, "\n(* is the one that runs when you type `docker`)")
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "\nwarning: %s\n", warn)
	}
	return nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
}
