package main

import (
	"github.com/wslkit/skrog/internal/pipeproxy"
	"github.com/wslkit/skrog/internal/wslc"
)

// combinedGate applies both admission policies on the wslc backend: the
// machine's deployed WSL container policy, and Skrog's own policy.yaml.
//
// They answer different questions and neither substitutes for the other. The
// WSL policy is the administrator's, deployed by GPO or Intune, and Skrog
// stands in for it because a direct docker.sock relay bypasses the enforcement
// in wslcsession (#322). policy.yaml is the machine owner's own rule set, the
// same one the distro backend applies (#120).
//
// Either may refuse. The WSL policy is consulted first so that when both would
// deny, the message names the one the user cannot simply edit.
type combinedGate struct {
	wsl   *wslc.PolicyGate
	skrog pipeproxy.Gate // policy.Watcher; nil when no policy.yaml is in use
}

func (g combinedGate) DenyCreate(body map[string]any) (string, bool) {
	if reason, denied := g.wsl.DenyCreate(body); denied {
		return reason, true
	}
	if g.skrog != nil {
		return g.skrog.DenyCreate(body)
	}
	return "", false
}

// DenyPull and DenyBuild currently consult only the WSL policy.
//
// Skrog's own policy.yaml has an allow-registries rule, and it has the same
// shape of gap this issue closed for the WSL policy: it is evaluated on
// container create, so it stops a blocked image running without stopping it
// being fetched. Extending it to pulls is worth doing — and applies equally to
// the distro backend, which is why it is not bolted on here.
func (g combinedGate) DenyPull(image string) (string, bool) { return g.wsl.DenyPull(image) }

func (g combinedGate) DenyBuild() (string, bool) { return g.wsl.DenyBuild() }
