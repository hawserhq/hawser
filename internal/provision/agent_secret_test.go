package provision_test

import (
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/provision"
)

// Every agent must be told where the secret is, not left to its own default.
//
// The default moved with the rename: an agent built before it looks in
// /etc/hawser/agent-secret, and rootfs 29.7.2 -- a live target for
// `--engine-version` and `engine rollback` -- ships exactly that agent. The
// host writes the secret to the new path and then demands the authenticated
// handshake; an agent reading the old path finds nothing, offers v1, and is
// refused as a downgrade. Silently, because startAgent is never fatal: vsock
// gone, socat at ~165ms per connection instead of ~0.6ms (#239).
//
// The existing fake in provision_test.go matched the script by the substring
// "agent-secret", which is true of either path -- so nothing failed.
func TestAgentStartPassesTheSecretPathToEveryAgent(t *testing.T) {
	cmd := provision.AgentStartCmdForTest()

	for _, bin := range []string{"skrog-agent", "hawser-agent"} {
		i := strings.Index(cmd, "exec "+bin+" ")
		if i < 0 {
			t.Fatalf("no exec clause for %s in:\n%s", bin, cmd)
		}
		clause := cmd[i:]
		if j := strings.Index(clause, "; fi;"); j >= 0 {
			clause = clause[:j]
		}
		want := "-secret-file " + provision.DistroAgentSecret
		if !strings.Contains(clause, want) {
			t.Errorf("%s is started without %q, so it falls back to its own default:\n  %s",
				bin, want, clause)
		}
	}

	// The path the host provisions and the path the agent is told to read are
	// the same string by construction; assert the value too, so a change here
	// has to be deliberate rather than incidental.
	if provision.DistroAgentSecret != "/etc/skrog/agent-secret" {
		t.Errorf("DistroAgentSecret = %q; the provisioning script writes it, so both must move together",
			provision.DistroAgentSecret)
	}
}
