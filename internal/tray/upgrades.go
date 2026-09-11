package tray

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/wslkit/skrog/internal/upgrade"
)

// Upgrades is what the tray shows after a "Check for updates" click.
type Upgrades struct {
	// Summary is one line, short enough for a tooltip.
	Summary string
	// Available is true when something can actually be upgraded, which is the
	// only case where opening the releases page is what the user wanted.
	Available bool
}

// CheckUpgrades runs `skrog upgrade --json` and renders the answer for the
// menu (#191).
//
// The item used to open the releases page and check nothing — it was named
// for an action it did not perform. Per the tray's own rule that every item
// shells out to the CLI, the check is the CLI's, and the tray only renders
// it. This adds no menu item: "Check updates" is already one of the six in
// PLAN §03, so the six-item cap is untouched.
func (c CLI) CheckUpgrades(ctx context.Context) (Upgrades, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := hideWindow(exec.CommandContext(ctx, c.Exe, "upgrade", "--json")).Output()
	// Exit 3 means "something can be upgraded" — a result, not a failure — so
	// the output is parsed before the error is judged.
	rep, perr := parseUpgrades(out)
	if perr != nil {
		if err != nil {
			return Upgrades{}, err
		}
		return Upgrades{}, perr
	}
	return summarize(rep), nil
}

func parseUpgrades(out []byte) (upgrade.Report, error) {
	var rep upgrade.Report
	if err := json.Unmarshal(out, &rep); err != nil {
		return rep, fmt.Errorf("reading `skrog upgrade --json`: %w", err)
	}
	if len(rep.Streams) == 0 {
		return rep, fmt.Errorf("`skrog upgrade --json` reported no components")
	}
	return rep, nil
}

// summarize turns the report into one tooltip line.
//
// It names what is behind rather than counting it: "skrog 0.4.0, engine
// 29.9.0 available" tells someone whether they care, where "3 updates
// available" makes them click to find out.
func summarize(rep upgrade.Report) Upgrades {
	var behind []string
	unknown := false
	for _, s := range rep.Streams {
		switch s.Status {
		case upgrade.StatusAvailable:
			behind = append(behind, label(s.Name)+" "+s.Latest)
		case upgrade.StatusUnknown:
			unknown = true
		}
	}
	if len(behind) > 0 {
		return Upgrades{Summary: strings.Join(behind, ", ") + " available", Available: true}
	}
	if unknown {
		// Never report "up to date" for "could not tell".
		return Upgrades{Summary: "could not check everything — run `skrog upgrade`"}
	}
	return Upgrades{Summary: "everything is up to date"}
}

// label gives the stream names their user-facing spelling; "app" is an
// implementation word.
func label(name string) string {
	switch name {
	case "app":
		return "skrog"
	case "cli":
		return "docker CLI"
	default:
		return name
	}
}
