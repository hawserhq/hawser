package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/hawserhq/hawser/internal/pipeproxy"
	"github.com/hawserhq/hawser/internal/provision"
)

// engineDialer builds the transport to the engine socket: the vsock agent
// (#40) first, the socat relay as automatic per-connection fallback — one
// dialer serves a new rootfs with the agent, an old rootfs without it, and an
// agent mid-restart, with no configuration.
//
// The per-install secret (#81), when present in the state dir, authenticates
// the agent: the vsock handshake then refuses any listener that cannot prove
// it (a squatting sibling distro), falling back to socat rather than trusting
// a stranger. Absent (an install that predates auth), the handshake is the
// pre-#81 identity-only form.
//
// HAWSER_NO_VSOCK=1 pins the socat path, as the support lever for a machine
// where the fast path misbehaves.
func engineDialer(distro, socketPath, stateDir string, log *slog.Logger) pipeproxy.Dialer {
	socat := &pipeproxy.WSLDialer{Distro: distro, SocketPath: socketPath}
	if os.Getenv("HAWSER_NO_VSOCK") == "1" {
		log.Info("vsock transport disabled by HAWSER_NO_VSOCK")
		return socat
	}
	secret := ""
	if stateDir != "" {
		if b, err := os.ReadFile(provision.AgentSecretPath(stateDir)); err == nil {
			secret = strings.TrimSpace(string(b))
		}
	}
	return &pipeproxy.FallbackDialer{
		Primary:   &pipeproxy.VsockDialer{Secret: secret},
		Secondary: socat,
		Logger:    log,
	}
}
