package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/wslkit/skrog/internal/pipeproxy"
	"github.com/wslkit/skrog/internal/supervise"
	"github.com/wslkit/skrog/internal/wslc"
)

// wslcEngineAdapter is supervise.Engine for a WSL container session (#335).
//
// The distro adapter starts and stops a distro Skrog imported. Here the engine
// belongs to Microsoft and the unit of lifecycle is the session VM, which is a
// better fit than it sounds: starting one is creating it, stopping one is
// terminating it, and a terminated session costs nothing while a stopped
// distro still holds its VHD open.
//
// Idle-stop therefore means something concrete on this backend -- roughly
// 820 MB of a second VM handed back -- so it is wired the same way as the
// distro's rather than disabled.
type wslcEngineAdapter struct {
	local   *wslc.Local
	agent   []byte
	secret  string
	session string
	log     *slog.Logger
}

// Running reports whether the session is up AND our agent is in it.
//
// Both halves matter. A session whose VM came back after idle-termination has
// a tmpfs root, so it is running with no agent -- reporting that as healthy
// would leave the supervisor content while every docker call failed.
func (e *wslcEngineAdapter) Running(ctx context.Context) bool {
	if e.session == "" {
		return false
	}
	ok, err := e.local.HasSession(ctx, e.session)
	if err != nil || !ok {
		return false
	}
	running, err := e.local.AgentRunning(ctx, e.session)
	return err == nil && running
}

// Start resolves a session -- creating one if nothing is running -- and places
// the agent. Idempotent, which is what the supervisor's restart loop needs.
func (e *wslcEngineAdapter) Start(ctx context.Context) error {
	session, err := e.local.ResolveSession(ctx)
	if err != nil {
		return err
	}
	e.session = session
	// ErrNoPortForwarding is a caveat about published ports, not a failed
	// start: the engine is reachable either way, and failing here would put
	// the supervisor into a restart loop over a working bridge.
	if err := e.local.Bootstrap(ctx, session, e.agent, e.secret); err != nil {
		if !isNoPortForwarding(err) {
			return err
		}
		e.log.Warn("published ports will not reach Windows on this agent")
	}
	return nil
}

// Stop terminates the session VM, and only ever the session Skrog resolved.
//
// The same rule the distro adapter follows for distros (#35): Skrog shares the
// machine. Terminating every session would take down whatever the user is
// running with `wslc` by hand.
func (e *wslcEngineAdapter) Stop(ctx context.Context) error {
	if e.session == "" {
		return nil
	}
	return e.local.Terminate(ctx, e.session)
}

func isNoPortForwarding(err error) bool {
	return errors.Is(err, wslc.ErrNoPortForwarding)
}

// wslcSupervised is everything the supervisor needs to serve a wslc session:
// the transport, the engine lifecycle, the bind translation, and the port
// watcher that carries published ports to Windows (#335).
type wslcSupervised struct {
	Dialer    pipeproxy.Dialer
	Engine    supervise.Engine
	Translate pipeproxy.SourceTranslator
	Policies  wslc.Policies
	Session   string
	stopWatch context.CancelFunc
	watcher   *wslc.PortWatcher
	shares    *wslc.ShareTable
}

// Close releases what the stack holds: the port listeners and the share holder
// containers. Called on shutdown, so a supervisor that exits cleanly leaves no
// skrog-share-* containers behind.
func (s *wslcSupervised) Close() {
	if s.stopWatch != nil {
		s.stopWatch()
	}
	if s.watcher != nil {
		s.watcher.StopAll()
	}
	if s.shares != nil {
		s.shares.Close(context.Background())
	}
}

// startWslcStack brings the backend up for the supervisor.
//
// It reuses wslcBackend, which `skrog proxy --engine wslc` already uses, so the
// two entry points cannot drift into behaving differently -- the supervised
// path was the one place that would have been tempting to reimplement.
func startWslcStack(ctx context.Context, agentPath, stateDir string, log *slog.Logger) (*wslcSupervised, error) {
	dialer, watcher, session, err := wslcBackend(ctx, agentPath, stateDir, log)
	if err != nil {
		return nil, err
	}

	// Policy is read once at startup and fails closed, exactly as the proxy
	// path does: a deployed allowlist that cannot be read is not the same as
	// no allowlist (#322).
	policies, err := wslc.ReadPolicies()
	if err != nil {
		return nil, fmt.Errorf("cannot read the WSL container policy: %w", err)
	}
	if policies.Restrictive() {
		log.Info("enforcing the deployed WSL container policy",
			"registry-allowlist", policies.RegistryAllowlist,
			"privileged-allowed", policies.PrivilegedAllowed)
	}

	l := wslc.New()
	agent, err := loadGuestAgent(ctx, agentPath)
	if err != nil {
		return nil, err
	}
	secret, err := wslcSecret(stateDir)
	if err != nil {
		return nil, err
	}

	shares := &wslc.ShareTable{
		Local:      l,
		Session:    session,
		EngineDial: dialer.Dial,
		Logger:     log,
	}

	s := &wslcSupervised{
		Dialer:    dialer,
		Translate: shares.Translator(ctx),
		Policies:  policies,
		Session:   session,
		watcher:   watcher,
		shares:    shares,
		Engine: &wslcEngineAdapter{
			local:   l,
			agent:   agent,
			secret:  secret,
			session: session,
			log:     log,
		},
	}

	if watcher != nil {
		wctx, cancel := context.WithCancel(ctx)
		s.stopWatch = cancel
		go watcher.Run(wctx)
	}
	return s, nil
}

// Handler is the pipe handler for this backend: the share table's bind
// translation, the audit log, and the machine's deployed WSL policy stacked in
// front of Skrog's own policy.yaml.
//
// Built here rather than in supervise.go so the wslc types stay in one file and
// the shared path keeps exactly the handler it always had.
func (s *wslcSupervised) Handler(auditor pipeproxy.AuditSink, own pipeproxy.Gate) func(net.Conn, io.ReadWriteCloser) error {
	return pipeproxy.RewriteBindsFor(s.Translate, auditor,
		combinedGate{wsl: &wslc.PolicyGate{Policies: s.Policies}, skrog: own})
}

// wslcStatus reports the engine state and session name for `skrog status` on a
// wslc install, without starting anything.
//
// "running" requires both a live session AND our agent in it: a session whose
// VM came back after idle-termination has a tmpfs root and no agent, and
// calling that running would tell a readiness probe the engine is up while
// every docker call fails. Listing sessions never boots one, so this stays
// safe to poll (#82).
func wslcStatus(ctx context.Context) (state, session string) {
	l := wslc.New()
	sessions, err := l.Sessions(ctx)
	if err != nil || len(sessions) == 0 {
		return "stopped", ""
	}
	name := wslc.DefaultSessionName()
	found := ""
	for _, s := range sessions {
		if s.DisplayName == wslc.SessionName || s.DisplayName == name {
			found = s.DisplayName
			break
		}
	}
	if found == "" {
		found = sessions[0].DisplayName
	}
	if running, err := l.AgentRunning(ctx, found); err == nil && running {
		return "running", found
	}
	return "stopped", found
}
