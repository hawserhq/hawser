package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every listener that serves the engine must install the guarded handler.
//
// Only the supervisor did. `skrog serve --tcp` — whose own help text says it
// exists so "a teammate or a CI runner" can drive this engine — used the bare
// rewriter, so every rule the machine's owner wrote was unenforced for remote
// clients and none of their calls were audited (#257). `skrog proxy` dropped
// auditing the same way.
//
// This is a source check rather than a behavioural one on purpose: the failure
// was a listener being *added* without the gate, and what needs pinning is
// that no file wires a Server.Handler to the unguarded rewriter. A test that
// drove each listener would prove today's three correct and say nothing about
// the fourth.
func TestNoListenerUsesTheUnguardedRewriter(t *testing.T) {
	// `Handler: pipeproxy.RewriteBinds` or `srv.Handler = pipeproxy.RewriteBinds`,
	// but not RewriteBindsGuarded / RewriteBindsAudited.
	bare := regexp.MustCompile(`Handler\s*[:=]\s*pipeproxy\.RewriteBinds\b`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d installs the unguarded rewriter, so this listener enforces no policy and audits nothing:\n  %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
