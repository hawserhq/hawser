package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/wslkit/skrog/internal/dockerctx"
	"github.com/wslkit/skrog/internal/pipeproxy"
	"github.com/wslkit/skrog/internal/provision"
	"github.com/wslkit/skrog/internal/wslc"
)

// wslcBackend brings up the wslc engine transport: resolve a session, put the
// agent in its root namespace, and hand back a dialer pointed at it (#316,
// #320).
//
// Unlike the distro backend there is no FallbackDialer here. The socat
// fallback exists because an older rootfs may have no agent; a wslc session
// has no agent at all until we put one there, so if the bootstrap failed the
// right answer is a clear error, not a slow path that also will not work.
func wslcBackend(ctx context.Context, agentPath, stateDir string, log *slog.Logger) (pipeproxy.Dialer, *wslc.PortWatcher, string, error) {
	l := wslc.New()

	ver, err := l.Version(ctx)
	if err != nil {
		return nil, nil, "", fmt.Errorf("wslc is not usable on this machine: %w", err)
	}

	session, err := l.ResolveSession(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	log.Info("using wslc session", "session", session, "wslc", ver)
	if session != wslc.SessionName {
		// Worth saying out loud: Skrog's containers and the user's own `wslc`
		// containers share one engine and one policy surface here (#322).
		log.Warn("sharing the wslc CLI's default session; a dedicated session "+
			"needs an API the shipped CLI does not expose",
			"session", session)
	}

	agent, err := loadGuestAgent(ctx, agentPath)
	if err != nil {
		return nil, nil, "", err
	}

	secret, err := wslcSecret(stateDir)
	if err != nil {
		return nil, nil, "", err
	}

	// place reports the caveat; bootstrap (used for recovery) swallows it,
	// because by then the caller has already been told once and a re-bootstrap
	// that "fails" for a known limitation must not fail the dial.
	place := func(ctx context.Context) error {
		// Re-resolve the session every time: the one we started with may have
		// gone with its VM, and ResolveSession is a single cheap CLI call.
		s, err := l.ResolveSession(ctx)
		if err != nil {
			return err
		}
		return l.Bootstrap(ctx, s, agent, secret)
	}
	bootstrap := func(ctx context.Context) error {
		if err := place(ctx); err != nil && !errors.Is(err, wslc.ErrNoPortForwarding) {
			return err
		}
		return nil
	}

	// ErrNoPortForwarding is a caveat, not a failure: the engine works, only
	// published ports do not. Say it once here rather than letting it surface
	// as a port that mysteriously refuses.
	ports := true
	if err := place(ctx); err != nil {
		if !errors.Is(err, wslc.ErrNoPortForwarding) {
			return nil, nil, "", fmt.Errorf("bootstrapping the agent into session %q: %w", session, err)
		}
		ports = false
		log.Warn("published ports will not reach Windows on this agent",
			"reason", "the agent lifted from the engine distro predates -forward-port",
			"fix", "pass --agent with a binary built from this tree")
	}
	log.Info("agent running in the wslc session", "bytes", len(agent), "vsock-port", wslc.AgentPort)

	// Cooldown disabled: the dialer's post-failure pause exists to stop a
	// rootfs with no agent from paying a dial timeout per connection. Here a
	// failure means the VM restarted and the agent needs re-placing, so pausing
	// would only delay the fix.
	engine := &wslc.Dialer{
		Inner:     &pipeproxy.VsockDialer{Port: wslc.AgentPort, Secret: secret, Cooldown: -1},
		Bootstrap: bootstrap,
		Logger:    log,
	}
	// The forward transport needs the same recovery: one idle termination
	// takes both listeners down together.
	forward := &wslc.Dialer{
		Inner:     &pipeproxy.VsockDialer{Port: wslc.ForwardPort, Secret: secret, Cooldown: -1},
		Bootstrap: bootstrap,
		Logger:    log,
	}
	if !ports {
		// No forward listener in the guest: a watcher would open Windows
		// listeners that can never connect, which is worse than no listener.
		return engine, nil, session, nil
	}
	watcher := &wslc.PortWatcher{
		EngineDial:  engine.Dial,
		ForwardDial: forward.Dial,
		Logger:      log,
	}
	watcher.Lease = &wslc.Lease{
		Local:   l,
		Session: session,
		Logger:  log,
	}
	return engine, watcher, session, nil
}

// loadGuestAgent finds a linux skrog-agent to place in the session.
//
// An explicit path wins. Otherwise it is lifted out of the engine distro,
// which already has one on PATH — that keeps the spike runnable on a normal
// install without a second build artifact. A wslc-only install has no distro
// to lift from, which is why the error says what to pass.
func loadGuestAgent(ctx context.Context, path string) ([]byte, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading the agent binary: %w", err)
		}
		return b, nil
	}

	b, err := agentFromDistro(ctx, provision.DefaultDistro)
	if err != nil {
		return nil, fmt.Errorf("no agent binary: pass --agent <path to a linux skrog-agent>, "+
			"or install the engine distro to lift one from (%w)", err)
	}
	return b, nil
}

// agentFromDistro copies the agent out of the engine distro.
//
// base64 rather than a raw stream on purpose: wsl.exe's output goes through
// UTF-16 detection and newline handling, which is fine for text and silently
// corrupts a binary.
func agentFromDistro(ctx context.Context, distro string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "wsl.exe", "-d", distro, "-u", "root", "--exec",
		"sh", "-c", "base64 -w0 \"$(command -v skrog-agent)\"")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading skrog-agent from distro %s: %w", distro, err)
	}
	// wsl.exe may hand back UTF-16; base64's alphabet is ASCII, so dropping
	// NULs is enough to normalise either encoding.
	clean := strings.Map(func(r rune) rune {
		if r == 0 || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, string(out))
	if clean == "" {
		return nil, fmt.Errorf("distro %s has no skrog-agent on PATH", distro)
	}
	b, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("decoding the agent from distro %s: %w", distro, err)
	}
	return b, nil
}

// wslcSecret reuses the install's agent secret when there is one, so both
// backends authenticate the same way, and otherwise mints an ephemeral one.
//
// Ephemeral is safe because we are also the party installing it in the guest:
// the two ends are configured together in this process. What it must never be
// is empty — that would leave the agent speaking the unauthenticated v1
// handshake, and any process on the host can reach a VM's vsock ports.
func wslcSecret(stateDir string) (string, error) {
	if stateDir != "" {
		if b, err := os.ReadFile(provision.AgentSecretPath(stateDir)); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s, nil
			}
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating an agent secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// runProxyWslc serves the pipe against a wslc session instead of Skrog's own
// distro. Kept separate from runProxy rather than threaded through it: almost
// nothing before the listener is shared — there is no manifest, no distro to
// start, and path translation is not merely skipped but wrong (see below).
func runProxyWslc(agentPath, pipeName, sddl string, noContext bool, opts provision.Options, log *slog.Logger) int {
	ctx := interruptCtx()

	dialer, watcher, session, err := wslcBackend(ctx, agentPath, optsWithResolvedStateDir(opts).StateDir, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
		return exitError
	}

	// Published ports are carried by Skrog on this backend, because the relay
	// that would carry them is driven from the Windows side by wslcsession and
	// a bridge talking to the engine socket never invokes it (#330). The
	// watcher opens and closes the host listeners as containers come and go.
	//
	// Nil when the guest agent is too old to forward; the warning has already
	// been logged and serving docker without published ports is the better of
	// the two available outcomes.
	if watcher != nil {
		go watcher.Run(ctx)
		defer watcher.StopAll()
	}

	selected, reason := pipeproxy.SelectPipeName(pipeName)
	dockerHost := pipeproxy.DockerHostFor(selected)

	listener, err := pipeproxy.Listen(selected, sddl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
		return exitError
	}
	defer listener.Close()

	log.Info("serving pipe", "pipe", selected, "reason", reason)
	log.Info("relaying to the wslc engine", "session", session)

	if !noContext {
		mgr := &dockerctx.Manager{}
		if err := mgr.Ensure(ctx, dockerHost); err != nil {
			log.Warn("could not wire the docker context", "error", err)
		}
	}

	// No RewriteBinds here, and that is not an oversight.
	//
	// The rewrite maps a Windows bind source to /mnt/<drive>, which is correct
	// for a distro that auto-mounts drives and wrong for a wslc session, where
	// each Windows folder is its own virtiofs share at /mnt/{GUID} and there is
	// no /mnt/c at all. Translating here would hand dockerd a path that does
	// not exist instead of an error the user can read. Guest-absolute sources
	// (/var/run/docker.sock, /tmp) work untouched, which is what makes
	// docker-in-docker and Ryuk viable on this backend.
	//
	// Windows-path binds therefore fail until #321 builds the share table, and
	// policy/audit ride on the same handler, so both are off here too — which
	// is why this backend is experimental and not something `skrog serve`
	// offers yet (#322).
	// The administrator's WSL container policy is enforced here, standing in
	// for the checks in wslcsession that a direct docker.sock relay bypasses
	// (#322). Skrog reads WSL's own configuration, so a deployed allowlist
	// means the same thing through this pipe as through `wslc`.
	policies, err := wslc.ReadPolicies()
	if err != nil {
		// Fail closed. A policy that cannot be read is not the same as no
		// policy, and guessing in the permissive direction is how a bypass
		// ships.
		fmt.Fprintf(os.Stderr, "skrog: cannot read the WSL container policy: %v\n", err)
		return exitError
	}
	if policies.Restrictive() {
		log.Info("enforcing the deployed WSL container policy",
			"registry-allowlist", policies.RegistryAllowlist,
			"privileged-allowed", policies.PrivilegedAllowed)
	}

	// Only the named-pipe case is translated here. See wslc.TranslateBindSource:
	// a pipe means this engine's socket on any backend (#164), while a Windows
	// drive path has no meaning in a session yet and fails loudly rather than
	// silently mounting nothing (#321).
	srv := &pipeproxy.Server{
		Dialer:  dialer,
		Logger:  log,
		Handler: pipeproxy.RewriteBindsFor(wslc.TranslateBindSource, nil, &wslc.PolicyGate{Policies: policies}),
	}

	fmt.Fprintf(os.Stderr, `
Bridge is up against the wslc session %q (experimental).

  docker --context %s ps
  $env:DOCKER_HOST = "%s"; docker ps

The deployed WSL container policy is enforced here (registry allowlist,
privileged containers). Skrog's own policy.yaml and the audit log are not
yet wired on this backend (#322).

Not yet supported: Windows-path bind mounts (#321), UDP published ports.

Ctrl-C to stop.

`, session, dockerctx.Name, dockerHost)

	sctx, stop := interruptible()
	defer stop()

	if err := srv.Serve(sctx, listener); err != nil {
		fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
		return exitError
	}
	log.Info("bridge stopped")
	return exitOK
}
