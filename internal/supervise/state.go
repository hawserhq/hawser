// Package supervise keeps the engine alive: a health loop with backoff,
// driven by a desired state the CLI writes and the supervisor honors.
//
// The desired state lives in a file rather than a control socket on purpose.
// `skrog stop` must mean *stays* stopped — without a recorded intent, the
// health loop would helpfully restart the engine the user just stopped, and
// the two would fight. A file also works when the supervisor is not running
// at all, which is exactly when `skrog start` needs somewhere to leave its
// instruction.
package supervise

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Desired is what the user last asked for.
type Desired string

const (
	// DesiredRunning means the health loop keeps the engine up. The default:
	// installing Skrog is itself the request to have a working engine.
	DesiredRunning Desired = "running"
	// DesiredStopped means the health loop keeps the engine down.
	DesiredStopped Desired = "stopped"
)

func statePath(stateDir string) string {
	return filepath.Join(stateDir, "desired-state")
}

// ReadDesired returns the recorded intent, defaulting to running when nothing
// was ever recorded.
func ReadDesired(stateDir string) Desired {
	b, err := os.ReadFile(statePath(stateDir))
	if err != nil {
		return DesiredRunning
	}
	if Desired(strings.TrimSpace(string(b))) == DesiredStopped {
		return DesiredStopped
	}
	return DesiredRunning
}

// WriteDesired records the intent atomically, so a crash mid-write cannot
// leave a state the reader misparses into an unintended restart.
func WriteDesired(stateDir string, d Desired) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	if err := commit(statePath(stateDir), []byte(d+"\n")); err != nil {
		return fmt.Errorf("committing desired state: %w", err)
	}
	return nil
}

// EngineState is the supervisor's own record of *why* the engine is down —
// distinct from Desired, which is what the user asked for. Its one non-default
// value, idle, is what lets `skrog status` (and scripts) tell "stopped by
// design, will wake on demand" from "broken".
type EngineState string

const (
	// EngineActive is the normal state; represented by the file being absent.
	EngineActive EngineState = "active"
	// EngineIdle means the supervisor stopped the engine because the bridge
	// was quiet for the configured idle-timeout. Deleting the file is the
	// wake-up poke: the supervisor treats its disappearance as demand.
	EngineIdle EngineState = "idle"
)

func engineStatePath(stateDir string) string {
	return filepath.Join(stateDir, "engine-state")
}

// ReadEngineState returns the recorded state; absent or unreadable is active.
func ReadEngineState(stateDir string) EngineState {
	b, err := os.ReadFile(engineStatePath(stateDir))
	if err != nil {
		return EngineActive
	}
	if EngineState(strings.TrimSpace(string(b))) == EngineIdle {
		return EngineIdle
	}
	return EngineActive
}

// WriteEngineState records the state atomically; writing active removes the
// file, so absence stays the ground truth for the normal case.
func WriteEngineState(stateDir string, st EngineState) error {
	if st == EngineActive {
		// Deleting is as exposed to a concurrent reader as renaming is, and
		// this delete is the wake-up poke the supervisor watches for -- losing
		// it leaves an idle engine that nothing wakes.
		return remove(engineStatePath(stateDir))
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	if err := commit(engineStatePath(stateDir), []byte(st+"\n")); err != nil {
		return fmt.Errorf("committing engine state: %w", err)
	}
	return nil
}
