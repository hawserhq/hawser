package upgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// releasesJSON is the shape the GitHub API returns, with the two tag families
// this repository actually publishes into one namespace.
const releasesJSON = `[
  {"tag_name":"rootfs-v29.8.0-1","draft":false,"prerelease":true},
  {"tag_name":"v0.3.0","draft":false,"prerelease":true},
  {"tag_name":"rootfs-v29.7.2-4","draft":false,"prerelease":true},
  {"tag_name":"v0.2.0","draft":false,"prerelease":true}
]`

func serve(t *testing.T, status int, body string) *GitHubReleases {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &GitHubReleases{URL: srv.URL, Client: srv.Client()}
}

func TestLatestAppIgnoresRootfsReleases(t *testing.T) {
	// The rootfs versions are numerically far higher than the app's. Taking
	// the newest release of any kind would report that skrog 0.3.0 should
	// upgrade to 29.8.0.
	g := serve(t, http.StatusOK, releasesJSON)
	got, err := g.LatestApp(context.Background())
	if err != nil {
		t.Fatalf("LatestApp: %v", err)
	}
	if got != "0.3.0" {
		t.Errorf("LatestApp = %q, want 0.3.0", got)
	}
}

func TestLatestAppCountsPrereleases(t *testing.T) {
	// Every release so far is a pre-release; skipping them would tell every
	// user they are current, forever.
	g := serve(t, http.StatusOK, `[{"tag_name":"v0.4.0","draft":false,"prerelease":true}]`)
	got, err := g.LatestApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.4.0" {
		t.Errorf("LatestApp = %q, want 0.4.0", got)
	}
}

func TestLatestAppSkipsDrafts(t *testing.T) {
	// A draft is not published; announcing it would point people at a
	// download that does not exist.
	g := serve(t, http.StatusOK,
		`[{"tag_name":"v0.9.0","draft":true,"prerelease":false},
		  {"tag_name":"v0.3.0","draft":false,"prerelease":true}]`)
	got, err := g.LatestApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.3.0" {
		t.Errorf("LatestApp = %q, want 0.3.0 (the draft must be ignored)", got)
	}
}

func TestLatestAppPicksTheNewestNotTheFirst(t *testing.T) {
	g := serve(t, http.StatusOK,
		`[{"tag_name":"v0.2.0","draft":false,"prerelease":true},
		  {"tag_name":"v0.10.0","draft":false,"prerelease":true},
		  {"tag_name":"v0.9.0","draft":false,"prerelease":true}]`)
	got, err := g.LatestApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.10.0" {
		t.Errorf("LatestApp = %q, want 0.10.0 — ordering is numeric, not textual or positional", got)
	}
}

func TestLatestAppNamesRateLimiting(t *testing.T) {
	g := serve(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
	_, err := g.LatestApp(context.Background())
	if err == nil {
		t.Fatal("no error on HTTP 403")
	}
	if !strings.Contains(err.Error(), "rate-limiting") {
		t.Errorf("error = %q; a bare 403 is not actionable", err)
	}
}

func TestLatestAppErrorsWhenNoAppReleaseExists(t *testing.T) {
	// A repository with only rootfs releases must not silently report the
	// rootfs version as the app version.
	g := serve(t, http.StatusOK, `[{"tag_name":"rootfs-v29.8.0-1","draft":false,"prerelease":true}]`)
	if _, err := g.LatestApp(context.Background()); err == nil {
		t.Fatal("no error when the feed holds no app release")
	}
}

func TestAppVersionAcceptsOnlyAppTags(t *testing.T) {
	cases := map[string]string{
		"v0.3.0":           "0.3.0",
		"v1.0.0":           "1.0.0",
		"rootfs-v29.8.0-1": "",
		"engine-v29.8.0":   "",
		"v":                "",
		"vnext":            "",
		"0.3.0":            "",
		"  v0.3.0  ":       "0.3.0",
	}
	for tag, want := range cases {
		got, ok := appVersion(tag)
		if want == "" {
			if ok {
				t.Errorf("appVersion(%q) accepted it as %q", tag, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("appVersion(%q) = %q, %v; want %q, true", tag, got, ok, want)
		}
	}
}
