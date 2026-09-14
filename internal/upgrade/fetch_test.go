package upgrade_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/upgrade"
)

// releaseZip builds a zip shaped like the one the release workflow publishes.
func releaseZip(t *testing.T, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range bodies {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRelease serves a SHA256SUMS and a zip the way the releases CDN does.
// sumsBody nil means "generate a correct one".
func fakeRelease(t *testing.T, version, arch string, zipBytes []byte, sumsBody *string) *httptest.Server {
	t.Helper()
	asset := upgrade.AssetName(version, arch)
	sum := sha256.Sum256(zipBytes)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	if sumsBody != nil {
		sums = *sumsBody
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			w.Write([]byte(sums))
		case strings.HasSuffix(r.URL.Path, asset):
			w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestStageVerifiesAndExtractsTheBinaries(t *testing.T) {
	z := releaseZip(t, map[string]string{
		"skrog.exe": "new-skrog", "skrogw.exe": "new-w", "skrogtray.exe": "new-tray",
		"LICENSE": "apache", "README.md": "readme",
	})
	srv := fakeRelease(t, "0.4.2", "amd64", z, nil)
	defer srv.Close()

	dir := t.TempDir()
	f := &upgrade.Fetcher{Base: srv.URL, Client: srv.Client()}
	if err := f.Stage(context.Background(), "0.4.2", "amd64", dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"skrog.exe": "new-skrog", "skrogw.exe": "new-w", "skrogtray.exe": "new-tray",
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q (err %v), want %q", name, b, err, want)
		}
	}
	// LICENSE and README belong to the installer, not to an upgrade: writing
	// them over the installed copies is not this command's business.
	for _, name := range []string{"LICENSE", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was extracted; only the binaries should be", name)
		}
	}
	// The downloaded zip must not be left in the staging directory, where the
	// swap would then try to install it as a binary.
	if _, err := os.Stat(filepath.Join(dir, upgrade.AssetName("0.4.2", "amd64"))); err == nil {
		t.Error("the zip was left behind in the staging directory")
	}
}

// The property the whole design rests on: nothing reaches disk unless the hash
// matches. This is what makes an in-process upgrade defensible while the
// binaries are unsigned.
func TestStageRefusesAZipThatFailsVerification(t *testing.T) {
	good := releaseZip(t, map[string]string{"skrog.exe": "legitimate"})
	tampered := releaseZip(t, map[string]string{"skrog.exe": "malicious"})

	// SHA256SUMS lists the good zip; the server serves the tampered one.
	sum := sha256.Sum256(good)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), upgrade.AssetName("0.4.2", "amd64"))
	srv := fakeRelease(t, "0.4.2", "amd64", tampered, &sums)
	defer srv.Close()

	dir := t.TempDir()
	f := &upgrade.Fetcher{Base: srv.URL, Client: srv.Client()}
	err := f.Stage(context.Background(), "0.4.2", "amd64", dir)
	if err == nil {
		t.Fatal("Stage accepted a zip whose hash did not match SHA256SUMS")
	}
	if !strings.Contains(err.Error(), "failed verification") {
		t.Errorf("error should name the failure plainly, got: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a failed verification left %d file(s) in the staging directory", len(entries))
	}
}

func TestStageRefusesWhenTheAssetIsNotListed(t *testing.T) {
	z := releaseZip(t, map[string]string{"skrog.exe": "x"})
	sums := "deadbeef  some_other_file.zip\n"
	srv := fakeRelease(t, "0.4.2", "amd64", z, &sums)
	defer srv.Close()

	f := &upgrade.Fetcher{Base: srv.URL, Client: srv.Client()}
	err := f.Stage(context.Background(), "0.4.2", "amd64", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Errorf("want a clear 'not listed' error, got: %v", err)
	}
}

// Releases up to v0.4.0 shipped SHA256SUMS with CRLF (#248). Someone pinning an
// older version must still be able to verify it.
func TestSumsParsingToleratesCRLFAndBinaryMarkers(t *testing.T) {
	z := releaseZip(t, map[string]string{"skrog.exe": "x"})
	sum := sha256.Sum256(z)
	asset := upgrade.AssetName("0.4.0", "amd64")
	sums := fmt.Sprintf("%s *%s\r\nsomething else\r\n", hex.EncodeToString(sum[:]), asset)
	srv := fakeRelease(t, "0.4.0", "amd64", z, &sums)
	defer srv.Close()

	f := &upgrade.Fetcher{Base: srv.URL, Client: srv.Client()}
	if err := f.Stage(context.Background(), "0.4.0", "amd64", t.TempDir()); err != nil {
		t.Errorf("CRLF SHA256SUMS with a binary marker should still verify: %v", err)
	}
}

// A zip entry naming ../ must not write outside the staging directory.
func TestStageIgnoresPathTraversalInZipEntries(t *testing.T) {
	z := releaseZip(t, map[string]string{
		"../../skrog.exe": "escaped",
		"skrogw.exe":      "fine",
	})
	srv := fakeRelease(t, "0.4.2", "amd64", z, nil)
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, "staging")
	f := &upgrade.Fetcher{Base: srv.URL, Client: srv.Client()}
	if err := f.Stage(context.Background(), "0.4.2", "amd64", dir); err != nil {
		t.Fatal(err)
	}
	// The traversing entry is written by its base name, inside dir.
	if b, err := os.ReadFile(filepath.Join(dir, "skrog.exe")); err != nil || string(b) != "escaped" {
		t.Errorf("entry should land inside the staging dir by base name, got %q err %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "skrog.exe")); err == nil {
		t.Error("a ../ entry escaped the staging directory")
	}
}

func TestStageRefusesAZipWithNoBinaries(t *testing.T) {
	z := releaseZip(t, map[string]string{"LICENSE": "apache"})
	srv := fakeRelease(t, "0.4.2", "amd64", z, nil)
	defer srv.Close()

	f := &upgrade.Fetcher{Base: srv.URL, Client: srv.Client()}
	err := f.Stage(context.Background(), "0.4.2", "amd64", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "contained none of") {
		t.Errorf("want a clear error for a zip with no binaries, got: %v", err)
	}
}
