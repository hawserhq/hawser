package provision

import "strings"

// The project has moved twice: from a personal account to the hawserhq
// organisation (#212), and then to wslkit when Hawser was renamed Skrog (#1).
// An install made before either move recorded the download URL of the day in
// its manifest, and GitHub's redirect is the only reason those still resolve.
//
// That redirect is not a durable guarantee. GitHub stops redirecting the
// moment the old path is occupied again, and an abandoned repository name can
// be claimed by anyone — so a recorded URL pointing at a former home is both a
// broken link waiting to happen and a path an outsider can come to own.
//
// The SHA-256 pin means a substituted rootfs fails verification rather than
// being imported, so this is not an integrity hole. It is a provenance and
// durability one, and the fix is to stop trusting the redirect.
const (
	// currentRootfsHome is where releases are published now.
	currentRootfsHome = "https://github.com/wslkit/skrog/"
)

// formerRootfsHomes are paths this project has published from before, oldest
// first.
//
// Append, never remove — and never rewrite when the project moves again.
// These are historical facts about where bytes actually came from; a rename
// pass that "updated" them would break exactly the old installs they exist to
// rescue. An install can be arbitrarily old.
var formerRootfsHomes = []string{
	"https://github.com/zcsizmadia/hawser/", // the original personal account
	"https://github.com/hawserhq/hawser/",   // the org, before the Skrog rename
}

// CanonicalRootfsURL rewrites a rootfs URL that points at a former home of
// this project, so nothing downstream depends on a redirect that can lapse or
// be taken over. It reports whether the URL was changed.
//
// Only the repository prefix is replaced; the tag and filename are the
// release's own and stay untouched, which is what keeps the checksum in the
// manifest meaningful against the rewritten URL.
//
// An unrecognised URL is returned unchanged. Air-gapped and custom installs
// legitimately point at file:// paths or an internal mirror, and rewriting
// those would break exactly the installs that chose them deliberately.
func CanonicalRootfsURL(u string) (string, bool) {
	for _, old := range formerRootfsHomes {
		if rest, ok := cutPrefixFold(u, old); ok {
			return currentRootfsHome + rest, true
		}
	}
	return u, false
}

// cutPrefixFold is strings.CutPrefix, case-insensitively: a host is not
// case-sensitive, and a URL that arrived through a copy-paste may not match
// byte for byte.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
