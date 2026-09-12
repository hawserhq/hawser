package provision

import "context"

// EnsureAgentSecretForTest exposes ensureAgentSecret to the external test
// package, which verifies the #81 version gating.
func (p *Provisioner) EnsureAgentSecretForTest(ctx context.Context, opts Options) {
	p.ensureAgentSecret(ctx, opts)
}

// AgentStartCmdForTest exposes the generated agent launch command, which is
// otherwise unexported and only ever handed to the distro's shell.
func AgentStartCmdForTest() string { return agentStartCmd() }
