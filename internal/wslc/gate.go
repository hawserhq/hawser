package wslc

import (
	"fmt"
	"strings"
)

// PolicyGate enforces the administrator's WSL container policy at Skrog's pipe,
// standing in for the checks that live in wslcsession and that a direct
// docker.sock relay bypasses (#322).
//
// It implements pipeproxy.Gate and pipeproxy.ImageGate. The rules are WSL's,
// read from WSL's own configuration — the aim is that a deployed allowlist
// means the same thing through this pipe as through `wslc`, not that Skrog
// invents a second policy language next to it. Skrog's own policy.yaml is a
// separate, additional gate; this one exists so an Intune deployment is not
// silently voided by installing Skrog.
type PolicyGate struct {
	Policies Policies
}

// DenyCreate judges `docker create` / `docker run`.
//
// Two checks: the image's registry against the allowlist, and --privileged
// against AllowWSLContainerPrivileged.
func (g *PolicyGate) DenyCreate(body map[string]any) (string, bool) {
	if image, _ := body["Image"].(string); image != "" {
		if server := RegistryServer(image); !g.Policies.RegistryAllowed(server) {
			return fmt.Sprintf(
				"WSLContainerRegistryAllowlist does not permit registry %q (image %q); "+
					"this machine's WSL policy allows: %s",
				server, image, strings.Join(g.Policies.RegistryAllowlist, ", ")), true
		}
	}

	if !g.Policies.PrivilegedAllowed {
		if hc, ok := body["HostConfig"].(map[string]any); ok {
			if priv, _ := hc["Privileged"].(bool); priv {
				return "AllowWSLContainerPrivileged denies privileged containers on this machine", true
			}
		}
	}
	return "", false
}

// DenyPull judges `docker pull`, and the implicit pull inside `docker run`.
//
// This is the check that makes the allowlist mean what an administrator thinks
// it means. Gating only container creation would stop a blocked image running
// while still fetching it onto the machine, which is not the guarantee WSL
// gives: it applies the allowlist when wslcsession handles the pull, before the
// bytes are requested.
func (g *PolicyGate) DenyPull(image string) (string, bool) {
	server := RegistryServer(image)
	if g.Policies.RegistryAllowed(server) {
		return "", false
	}
	return fmt.Sprintf(
		"WSLContainerRegistryAllowlist does not permit pulling from %q (image %q); "+
			"this machine's WSL policy allows: %s",
		server, image, strings.Join(g.Policies.RegistryAllowlist, ", ")), true
}

// DenyBuild judges `docker build`.
//
// Refused outright whenever an allowlist is active, which is deliberate and is
// WSL's own position: a Dockerfile's FROM and any RUN can reach any registry,
// so the traffic cannot be attributed in advance. wslpolicies.h says so for
// `wslc image build` — callers "that cannot attribute traffic to a specific
// registry must therefore refuse the operation whenever any allowlist
// restriction is active". Allowing builds here would be the easiest possible
// way around the allowlist, so Skrog takes the same position rather than a
// weaker one.
func (g *PolicyGate) DenyBuild() (string, bool) {
	if !g.Policies.HasRegistryAllowlist() {
		return "", false
	}
	return "WSLContainerRegistryAllowlist is in force on this machine, and a build " +
		"can pull from any registry, so it cannot be attributed to an allowed one. " +
		"`wslc image build` is refused for the same reason", true
}

// RegistryServer extracts the registry host from an image reference, the way
// WSL's RepositoryReference does before checking the allowlist.
//
// The rule is Docker's: the part before the first slash is a registry only if
// it looks like a host — it contains a dot or a colon, or is "localhost".
// Otherwise the reference is a Docker Hub short name ("busybox",
// "library/busybox").
func RegistryServer(image string) string {
	ref := strings.TrimSpace(image)
	if ref == "" {
		return ""
	}
	first, _, found := strings.Cut(ref, "/")
	if !found {
		return DockerHubServer
	}
	if first == "localhost" || strings.ContainsAny(first, ".:") {
		return first
	}
	return DockerHubServer
}

// DockerHubServer is what an unqualified image reference resolves to. An
// allowlist that does not name it therefore blocks `docker pull busybox`, which
// is the point of deploying one.
const DockerHubServer = "docker.io"
