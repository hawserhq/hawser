package wslc

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The session VM's root filesystem is a tmpfs overlay over a read-only
// system.vhd: nothing placed there survives a VM boot, and the VM
// idle-terminates whenever it has no running containers (Microsoft's own
// IWSLCVirtualMachineFactory documentation says so). So the bootstrap is not a
// one-time install — it runs again after every boot, and everything it does
// has to be idempotent and cheap (#323).
const (
	// AgentPath is where the agent lands in the root namespace. /tmp is on the
	// tmpfs upper layer, which is writable and discarded on boot — exactly the
	// lifetime we want.
	AgentPath = "/tmp/skrog-agent"

	// AgentLogPath collects the agent's own output, so a failed bootstrap can
	// be diagnosed without re-running it.
	AgentLogPath = "/tmp/skrog-agent.log"

	// SecretPath matches the agent's built-in default (guest/agent.SecretFile),
	// so the agent needs no extra flag to find it.
	SecretPath = "/etc/skrog/agent-secret"
)

// AgentPort is the vsock port the agent listens on inside a wslc session:
// ASCII "hawc", one letter off vsockproto.Port ("haws") which the distro agent
// uses.
//
// The distinct port is what makes the two backends tell themselves apart.
// VsockDialer already enumerates every running compute system and accepts the
// first that completes the handshake, so with both an engine distro and a wslc
// session running, one shared port would be ambiguous — you would reach
// whichever VM answered first, which is not a property to build an engine
// selection on. Keying on the port instead means the existing dialer needs no
// VM-GUID plumbing at all: `VsockDialer{Port: AgentPort}` can only ever find a
// wslc agent, in whichever VM it happens to live, and the GUID cache handles
// the rest.
const AgentPort uint32 = 0x68617763

// Bootstrap places the agent and its auth secret in the session VM's root
// namespace and starts the agent, returning once it is listening.
//
// Everything travels over the `system session run` stdin relay rather than a
// virtiofs share: a share is a Windows folder, readable by every other process
// running as the user, and the secret is what stops a sibling listener from
// impersonating the agent (#81, #320).
//
// Calling this on a session that already has a healthy agent is cheap and
// harmless — it re-streams and restarts, which is also the recovery path after
// an idle termination.
func (l *Local) Bootstrap(ctx context.Context, session string, agent []byte, secret string) error {
	if len(agent) == 0 {
		return fmt.Errorf("wslc: agent binary is empty")
	}
	if strings.ContainsAny(secret, "\n\r") {
		// The secret is delivered by `cat`, so an embedded newline would
		// truncate it and the handshake would fail in a way that looks like a
		// transport bug rather than a bad secret.
		return fmt.Errorf("wslc: secret must not contain newlines")
	}

	// Stop anything already running before overwriting the binary: replacing a
	// file that a live process is executing fails with ETXTBSY.
	if _, err := l.RunInSession(ctx, session, "sh", "-c", stopScript); err != nil {
		return fmt.Errorf("wslc: stopping any previous agent: %w", err)
	}

	if _, err := l.RunInSessionStdin(ctx, session, bytes.NewReader(agent),
		"sh", "-c", installScript); err != nil {
		return fmt.Errorf("wslc: installing the agent: %w", err)
	}

	if secret != "" {
		if _, err := l.RunInSessionStdin(ctx, session, strings.NewReader(secret),
			"sh", "-c", secretScript); err != nil {
			return fmt.Errorf("wslc: installing the agent secret: %w", err)
		}
	}

	out, err := l.RunInSession(ctx, session, "sh", "-c", startScript())
	if err != nil {
		return fmt.Errorf("wslc: starting the agent: %w", err)
	}
	if !strings.Contains(out, "listening") {
		return fmt.Errorf("wslc: agent did not report listening: %s", out)
	}
	return nil
}

// AgentRunning reports whether an agent process is alive in the session. Used
// by the supervisor's health loop to decide whether a re-bootstrap is needed
// after an idle termination.
func (l *Local) AgentRunning(ctx context.Context, session string) (bool, error) {
	out, err := l.RunInSession(ctx, session, "sh", "-c",
		"pgrep -f "+agentPattern+" >/dev/null 2>&1 && echo yes || echo no")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "yes"), nil
}

// agentPattern is AgentPath as a pgrep/pkill regex that cannot match the very
// shell running it.
//
// `pgrep -f /tmp/skrog-agent` matches any process whose command line contains
// that string — including `sh -c "pgrep -f /tmp/skrog-agent …"` itself. The
// consequences are not symmetric but both are bad: pkill SIGTERMs its own
// shell (exit 143, which is how this was found), and pgrep reports the agent
// as running when it is not, so a health check can never fail and a dead agent
// is never restarted.
//
// Wrapping the first character in a character class is the standard fix: as a
// regex "[/]tmp/…" still matches "/tmp/…", but the literal text "[/]tmp/…" on
// the shell's own command line does not.
// AgentPath is "/tmp/skrog-agent", so this is "[/]tmp/skrog-agent". Written
// out rather than sliced because Go constants cannot be built from slices, and
// a var here would break the script consts below.
const agentPattern = `[/]tmp/skrog-agent`

// The scripts are separate consts rather than inline strings so the quoting is
// reviewable in one place. Each is passed as a single argv element to `sh -c`,
// so no Windows-side shell ever sees them.
const (
	// pkill matches the full path, never a bare name: the session VM is shared
	// with whatever else the user is running, and killing by image name is the
	// mistake that took out Docker Desktop in #35. See agentPattern for why the
	// path is bracketed.
	//
	// `exit 0` because "nothing to kill" is the normal first-run case and pkill
	// exits 1 for it.
	stopScript = `pkill -f ` + agentPattern + ` 2>/dev/null; exit 0`

	installScript = `cat > ` + AgentPath + `.new && chmod 0755 ` + AgentPath + `.new && mv -f ` + AgentPath + `.new ` + AgentPath

	secretScript = `mkdir -p /etc/skrog && cat > ` + SecretPath + ` && chmod 0600 ` + SecretPath
)

// startScript launches the agent and waits for it to report itself listening.
//
// setsid detaches it from the relay process, which exits as soon as this
// command returns; without it the agent dies with its parent. The wait loop is
// what makes Bootstrap synchronous — it returns only once the banner appears,
// so a caller that dials immediately afterwards does not race the listener.
//
// A function rather than a const because the port has one source of truth
// (AgentPort) and writing it twice is how the two drift apart.
func startScript() string {
	port := strconv.FormatUint(uint64(AgentPort), 10)
	return `setsid ` + AgentPath + ` -port ` + port + ` >` + AgentLogPath + ` 2>&1 &
for i in $(seq 1 50); do
  if grep -q listening ` + AgentLogPath + ` 2>/dev/null; then cat ` + AgentLogPath + `; exit 0; fi
  sleep 0.1
done
echo "timed out waiting for the agent"; cat ` + AgentLogPath + ` 2>/dev/null; exit 1`
}
