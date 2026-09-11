package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/hawserhq/hawser/internal/dockerctx"
	"github.com/hawserhq/hawser/internal/pipeproxy"
	"github.com/hawserhq/hawser/internal/provision"
)

func runProxy(args []string) int {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	var (
		distro     = fs.String("distro", "", "WSL distro to relay to (default: from the install manifest)")
		stateDir   = fs.String("state-dir", "", "override Hawser's state directory")
		pipeName   = fs.String("pipe", "", "pipe to serve (default: "+pipeproxy.DefaultPipeName+", or Hawser's own if that is taken)")
		socketPath = fs.String("socket", provision.EngineSocket, "engine socket inside the distro")
		noContext  = fs.Bool("no-context", false, "do not create or update the hawser docker context")
		noRewrite  = fs.Bool("no-path-translation", false, "relay bytes verbatim, without translating Windows bind paths")
		sddl       = fs.String("sddl", "", "security descriptor for the pipe (advanced; default restricts to SYSTEM, admins and interactive users)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser proxy [flags]

Serves the Windows named pipe that stock docker.exe connects to, relaying it to
the engine inside the WSL2 distro. Runs in the foreground until interrupted.

This is the v0.1 way to run the bridge. A Windows service that supervises it
without a logged-in session is v0.2 work (issue #3).

Exit codes: 0 clean shutdown, %d error, %d usage, %d no engine installed.

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	log := cliLogger(false)
	opts := provision.Options{Distro: *distro, StateDir: *stateDir}

	// The manifest knows which distro this machine actually has, which matters
	// when it was installed under a custom name.
	p := &provision.Provisioner{Logger: log}
	targetDistro := opts.Distro
	if m, err := p.ReadManifest(opts); err == nil && m.Distro != "" {
		targetDistro = m.Distro
	} else if targetDistro == "" {
		fmt.Fprintf(os.Stderr,
			"hawser: no install found. Run `hawser install` first, "+
				"or pass --distro to relay to an existing distro.\n")
		return exitNotFound
	}

	// Make sure the engine is actually up before serving the pipe.
	//
	// A distro shuts down when its last process exits, so the dockerd that
	// `hawser install` started does not outlive the install. Until the v0.2
	// supervisor exists, the proxy is the long-lived process, so starting the
	// engine is its job — otherwise every client sees an EOF and the bridge
	// looks broken when the engine is merely absent.
	startOpts := opts
	startOpts.Distro = targetDistro
	if err := p.StartEngine(interruptCtx(), startOpts); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	selected, reason := pipeproxy.SelectPipeName(*pipeName)
	dockerHost := pipeproxy.DockerHostFor(selected)

	listener, err := pipeproxy.Listen(selected, *sddl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	defer listener.Close()

	log.Info("serving pipe", "pipe", selected, "reason", reason)
	log.Info("relaying to engine", "distro", targetDistro, "socket", *socketPath)

	// Wiring the context is a convenience, not a precondition: a missing docker
	// CLI must not stop the bridge from running.
	if !*noContext {
		mgr := &dockerctx.Manager{}
		if err := mgr.Ensure(interruptCtx(), dockerHost); err != nil {
			var noCLI *dockerctx.ErrNoDockerCLI
			if errors.As(err, &noCLI) {
				log.Warn("skipping docker context", "reason", err)
			} else {
				log.Warn("could not wire the docker context", "error", err)
			}
		} else {
			log.Info("docker context ready", "context", dockerctx.Name, "host", dockerHost)
		}
	}

	srv := &pipeproxy.Server{
		Dialer: engineDialer(targetDistro, *socketPath, opts.StateDir, log),
		Logger: log,
	}
	if !*noRewrite {
		srv.Handler = pipeproxy.RewriteBinds
	}

	fmt.Fprintf(os.Stderr, `
Bridge is up. In another shell:

  docker --context %s ps
  $env:DOCKER_HOST = "%s"; docker ps

Ctrl-C to stop.

`, dockerctx.Name, dockerHost)

	ctx, stop := interruptible()
	defer stop()

	if err := srv.Serve(ctx, listener); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	log.Info("bridge stopped")
	return exitOK
}
