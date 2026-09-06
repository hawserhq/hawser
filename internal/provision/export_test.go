package provision

import "context"

// EnsureAgentSecretForTest exposes ensureAgentSecret to the external test
// package, which verifies the #81 version gating.
func (p *Provisioner) EnsureAgentSecretForTest(ctx context.Context, opts Options) {
	p.ensureAgentSecret(ctx, opts)
}
