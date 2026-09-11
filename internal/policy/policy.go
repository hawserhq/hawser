// Package policy is local, scriptable admission control for the docker API
// (#120): a small set of rules evaluated on each container-affecting call,
// denying the ones a machine's owner has ruled out.
//
// Hawser already sits in the request path — every `docker` call crosses the
// named-pipe bridge, which rewrites bind-mount paths — so the same seam can
// judge a request before it reaches the engine. That is what makes this cheap:
// no elevation, no engine change, no daemon plugin.
//
// # What this is not
//
// Not a security boundary against a hostile local user. They own the machine:
// they can edit the rules file, point DOCKER_HOST elsewhere, or talk to the
// engine directly. It is a guardrail against mistakes and a policy surface for
// shared and CI machines, and claiming more would be dishonest.
//
// Not an OPA/Rego engine either. The rules are a fixed, small vocabulary
// chosen so that a reader can tell at a glance what is forbidden. A rule
// language is a much bigger promise than this needs to make.
//
// # Failure direction
//
// A rule set that cannot be read is an error the caller surfaces, not an
// empty policy: silently allowing everything because a file has a typo is the
// one failure mode a guardrail must not have. An empty or absent file, by
// contrast, means exactly what it says — no rules, allow everything — because
// that is the default state of a machine nobody has configured.
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the rule set's name inside the state dir.
const FileName = "policy.yaml"

// Path is where the rule set lives for a given install.
func Path(stateDir string) string { return filepath.Join(stateDir, FileName) }

// Rules is the declarative rule set, in the same kebab-case YAML shape as
// hawser.yaml so the two read alike.
type Rules struct {
	// DenyPrivileged refuses `--privileged`, which disables essentially every
	// container isolation at once.
	DenyPrivileged bool `yaml:"deny-privileged,omitempty" json:"denyPrivileged,omitempty"`

	// DenyAddedCapabilities refuses any `--cap-add`. Capabilities are the
	// piecemeal version of --privileged, so a rule set that denies one and
	// ignores the other is not worth much.
	DenyAddedCapabilities bool `yaml:"deny-added-capabilities,omitempty" json:"denyAddedCapabilities,omitempty"`

	// DenyCapabilities refuses only these, for a rule set that wants
	// SYS_ADMIN gone without banning the harmless ones. Names match however
	// docker spells them; comparison is case-insensitive and ignores a CAP_
	// prefix, since both spellings are common.
	DenyCapabilities []string `yaml:"deny-capabilities,omitempty" json:"denyCapabilities,omitempty"`

	// DenyHostNamespaces refuses --network=host, --pid=host, --ipc=host and
	// --uts=host, each of which reaches straight out of the container.
	DenyHostNamespaces bool `yaml:"deny-host-namespaces,omitempty" json:"denyHostNamespaces,omitempty"`

	// AllowBindSources limits where a bind mount may come from. Empty means
	// no restriction. Paths are Windows-side as the user typed them, matched
	// as path prefixes, because that is what a person writing this rule is
	// thinking about.
	AllowBindSources []string `yaml:"allow-bind-sources,omitempty" json:"allowBindSources,omitempty"`

	// AllowRegistries limits which registries an image may come from. Empty
	// means no restriction. A bare name like "ubuntu" is Docker Hub, so
	// allowing only an internal registry also stops Hub pulls.
	AllowRegistries []string `yaml:"allow-registries,omitempty" json:"allowRegistries,omitempty"`

	// RequireDigest refuses an image reference that is not pinned by digest,
	// which is the only reference that cannot change under you.
	RequireDigest bool `yaml:"require-digest,omitempty" json:"requireDigest,omitempty"`
}

// Empty reports whether the rule set forbids nothing, so callers can skip the
// work and say "no policy" rather than "policy that allows everything".
func (r Rules) Empty() bool {
	return !r.DenyPrivileged && !r.DenyAddedCapabilities && len(r.DenyCapabilities) == 0 &&
		!r.DenyHostNamespaces && len(r.AllowBindSources) == 0 &&
		len(r.AllowRegistries) == 0 && !r.RequireDigest
}

// Load reads the rule set for an install. A missing file is an empty rule set,
// not an error: no policy is the default state.
func Load(stateDir string) (Rules, error) {
	b, err := os.ReadFile(Path(stateDir))
	if os.IsNotExist(err) {
		return Rules{}, nil
	}
	if err != nil {
		return Rules{}, fmt.Errorf("policy: reading %s: %w", Path(stateDir), err)
	}
	return Parse(b)
}

// Parse decodes a rule set. Unknown fields are refused rather than ignored: a
// misspelled rule that silently does nothing is the worst outcome a guardrail
// can have, and it would look identical to a rule that is working.
func Parse(b []byte) (Rules, error) {
	var r Rules
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		// An empty document decodes to io.EOF; that is an empty rule set.
		if err.Error() == "EOF" {
			return Rules{}, nil
		}
		return Rules{}, fmt.Errorf("policy: %w", err)
	}
	return r, nil
}

// Decision is the verdict on one request.
type Decision struct {
	// Denied is whether the request must be refused.
	Denied bool
	// Reason is shown to the user verbatim, through the docker CLI, so it
	// names the rule and what tripped it rather than saying "denied".
	Reason string
	// Rule is the rule that denied, for the log.
	Rule string
}

// Allow is the verdict when nothing objects.
var Allow = Decision{}

func deny(rule, format string, args ...any) Decision {
	return Decision{Denied: true, Rule: rule, Reason: fmt.Sprintf(format, args...)}
}

// EvaluateCreate judges a container-create body, decoded as the bridge decodes
// it: a generic map, so that fields this code does not know about are neither
// required nor disturbed.
//
// The first rule to object wins. Reporting only one reason is deliberate — a
// list of everything wrong with a request is harder to act on than the first
// thing to fix, and the user re-runs anyway.
func (r Rules) EvaluateCreate(body map[string]any) Decision {
	if r.Empty() {
		return Allow
	}
	hc, _ := body["HostConfig"].(map[string]any)

	if r.DenyPrivileged && truthy(hc["Privileged"]) {
		return deny("deny-privileged",
			"policy denies --privileged: it turns off container isolation wholesale")
	}

	if added := stringsOf(hc["CapAdd"]); len(added) > 0 {
		if r.DenyAddedCapabilities {
			return deny("deny-added-capabilities",
				"policy denies added capabilities (--cap-add %s)", strings.Join(added, ", "))
		}
		for _, c := range added {
			for _, bad := range r.DenyCapabilities {
				if normalizeCap(c) == normalizeCap(bad) {
					return deny("deny-capabilities",
						"policy denies the %s capability", strings.ToUpper(normalizeCap(c)))
				}
			}
		}
	}

	if r.DenyHostNamespaces {
		for _, ns := range []struct{ field, flag string }{
			{"NetworkMode", "--network=host"},
			{"PidMode", "--pid=host"},
			{"IpcMode", "--ipc=host"},
			{"UTSMode", "--uts=host"},
		} {
			if s, _ := hc[ns.field].(string); strings.EqualFold(s, "host") {
				return deny("deny-host-namespaces",
					"policy denies %s: it shares the host namespace with the container", ns.flag)
			}
		}
	}

	if len(r.AllowBindSources) > 0 {
		for _, src := range bindSources(hc) {
			if !underAny(src, r.AllowBindSources) {
				return deny("allow-bind-sources",
					"policy does not allow bind mounts from %s (allowed: %s)",
					src, strings.Join(r.AllowBindSources, ", "))
			}
		}
	}

	image, _ := body["Image"].(string)
	if image != "" {
		if len(r.AllowRegistries) > 0 {
			reg := registryOf(image)
			if !matchesAny(reg, r.AllowRegistries) {
				return deny("allow-registries",
					"policy does not allow images from %s (allowed: %s)",
					reg, strings.Join(r.AllowRegistries, ", "))
			}
		}
		if r.RequireDigest && !strings.Contains(image, "@sha256:") {
			return deny("require-digest",
				"policy requires an image pinned by digest; %s is not (use image@sha256:...)", image)
		}
	}

	return Allow
}

// truthy reads a JSON boolean that may have arrived as a bool or, from a
// hand-written body, as a string.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	}
	return false
}

// stringsOf reads a JSON array of strings, tolerating a single string.
func stringsOf(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

// normalizeCap makes NET_ADMIN, net_admin and CAP_NET_ADMIN the same name.
func normalizeCap(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "CAP_"))
}

// bindSources collects every host path a create request would mount, from both
// spellings the API accepts: the legacy HostConfig.Binds strings and the
// structured HostConfig.Mounts entries.
func bindSources(hc map[string]any) []string {
	var out []string
	for _, b := range stringsOf(hc["Binds"]) {
		// "src:dst[:opts]" — and src may be a Windows path with a drive
		// letter, so the split cannot simply take the first field.
		//
		// A source with no separator is a NAMED VOLUME, not a host path
		// ("myvol:/data"), and has nothing for a bind rule to restrict.
		// Treating it as a path would deny every volume mount on a machine
		// with an allowlist — which is not what the rule says.
		if src := bindSource(b); src != "" && isHostPath(src) {
			out = append(out, src)
		}
	}
	if mounts, ok := hc["Mounts"].([]any); ok {
		for _, m := range mounts {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := mm["Type"].(string); !strings.EqualFold(t, "bind") {
				continue
			}
			if s, _ := mm["Source"].(string); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// bindSource extracts the host side of a "src:dst[:opts]" bind. A Windows
// source carries its own colon (C:\src), so the split cannot simply take the
// first field.
func bindSource(b string) string {
	parts := strings.Split(b, ":")
	switch {
	case len(parts) < 2:
		return ""
	case len(parts[0]) == 1 && len(parts) >= 3:
		// Drive letter: "C:\src:/dst" -> "C:\src".
		return parts[0] + ":" + parts[1]
	default:
		return parts[0]
	}
}

// underAny reports whether a path sits under one of the allowed roots. Compared
// case-insensitively with separators normalized, because these are Windows
// paths written by hand and "C:/src" and `c:\src` mean the same directory.
func underAny(path string, roots []string) bool {
	p := normPath(path)
	for _, root := range roots {
		r := strings.TrimSuffix(normPath(root), "/")
		if r == "" {
			continue
		}
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func normPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
}

// registryOf extracts the registry host from an image reference, applying
// docker's own rule: the first component is a registry only if it looks like a
// host (contains a dot or colon, or is "localhost"). Everything else is Docker
// Hub, which is why a rule set allowing only an internal registry also stops
// `docker run ubuntu`.
func registryOf(image string) string {
	first, _, found := strings.Cut(image, "/")
	if !found {
		return "docker.io"
	}
	if first == "localhost" || strings.ContainsAny(first, ".:") {
		return first
	}
	return "docker.io"
}

// matchesAny compares a registry against the allowlist, case-insensitively,
// supporting a leading "*." wildcard for a whole domain.
func matchesAny(reg string, allowed []string) bool {
	r := strings.ToLower(strings.TrimSpace(reg))
	// A registry written without a port means that host on any port: the port
	// is a deployment detail, and a rule set that allows
	// registry.example.com but not registry.example.com:5000 would surprise
	// everyone. An entry that DOES name a port is matched exactly.
	host := r
	if h, _, found := strings.Cut(r, ":"); found {
		host = h
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		target := r
		if !strings.Contains(a, ":") {
			target = host
		}
		switch {
		case a == target:
			return true
		case strings.HasPrefix(a, "*."):
			if strings.HasSuffix(target, a[1:]) {
				return true
			}
		}
	}
	return false
}

// DecodeCreateBody parses a container-create body the way the bridge does, so
// `hawser policy test` judges exactly what the bridge would.
func DecodeCreateBody(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("policy: parsing the create body: %w", err)
	}
	return body, nil
}

// DenyCreate adapts Rules to the bridge's structural Gate interface, so
// internal/pipeproxy never has to import this package — the same shape the
// audit sink uses, and for the same reason.
func (r Rules) DenyCreate(body map[string]any) (reason string, denied bool) {
	d := r.EvaluateCreate(body)
	return d.Reason, d.Denied
}

// isHostPath distinguishes a bind source from a named volume in the legacy
// "src:dst" form. Docker's rule: a source with no separator is a volume name.
// A Windows drive letter counts as a path even though it carries a colon.
func isHostPath(s string) bool {
	if strings.ContainsAny(s, `/\`) {
		return true
	}
	// "C:" on its own, i.e. a bare drive root.
	return len(s) == 2 && s[1] == ':'
}
