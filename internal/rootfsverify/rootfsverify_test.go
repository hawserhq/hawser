package rootfsverify_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/rootfsverify"
)

// fakeFetch serves canned URLs and records what was asked for.
type fakeFetch struct {
	files map[string]string
	asked []string
}

func (f *fakeFetch) Fetch(_ context.Context, url string) (io.ReadCloser, error) {
	f.asked = append(f.asked, url)
	body, ok := f.files[url]
	if !ok {
		return nil, fmt.Errorf("404 %s", url)
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

const rootfsURL = "https://example.invalid/hawser-rootfs-29.7.2-4.tar.gz"

// setup writes a tarball and returns a verifier wired to serve a checksum file
// matching (or, when tamper is set, not matching) it.
func setup(t *testing.T, content string, signedDigest string, runErr error) (*rootfsverify.Verifier, string, *fakeFetch) {
	t.Helper()
	dir := t.TempDir()
	tarball := filepath.Join(dir, "rootfs.tar.gz")
	if err := os.WriteFile(tarball, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if signedDigest == "" {
		sum := sha256.Sum256([]byte(content))
		signedDigest = hex.EncodeToString(sum[:])
	}
	f := &fakeFetch{files: map[string]string{
		rootfsURL + ".sha256":               signedDigest + "  hawser-rootfs-29.7.2-4.tar.gz\n",
		rootfsURL + ".sha256.cosign.bundle": `{"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3"}`,
	}}
	v := &rootfsverify.Verifier{
		Fetch:   f,
		Repo:    "hawserhq/hawser",
		Cosign:  "cosign", // never executed: Run is stubbed
		TempDir: dir,
		Run: func(_ context.Context, _ string, _ ...string) (string, error) {
			if runErr != nil {
				return "Error: no matching signatures", runErr
			}
			return "Verified OK", nil
		},
	}
	return v, tarball, f
}

func TestVerifyHappyPath(t *testing.T) {
	v, tarball, f := setup(t, "rootfs bytes", "", nil)

	res, err := v.Verify(context.Background(), rootfsURL, tarball)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Verified {
		t.Error("Verified is false on success")
	}
	sum := sha256.Sum256([]byte("rootfs bytes"))
	if res.Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("Digest = %s", res.Digest)
	}
	// Both signature files must have been fetched from beside the rootfs.
	want := []string{rootfsURL + ".sha256", rootfsURL + ".sha256.cosign.bundle"}
	if strings.Join(f.asked, ",") != strings.Join(want, ",") {
		t.Errorf("fetched %v, want %v", f.asked, want)
	}
}

func TestVerifyRejectsASignatureForOtherBytes(t *testing.T) {
	// The interesting failure: the signature is valid and attests to a
	// different tarball than the one on disk. A checksum pin alone would pass
	// if the attacker also edited the manifest.
	other := strings.Repeat("a", 64)
	v, tarball, _ := setup(t, "rootfs bytes", other, nil)

	_, err := v.Verify(context.Background(), rootfsURL, tarball)
	var mm *rootfsverify.ErrMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v, want *ErrMismatch", err)
	}
	if mm.Signed != other {
		t.Errorf("Signed = %s", mm.Signed)
	}
	if !strings.Contains(err.Error(), "signed checksum") {
		t.Errorf("the message should say what disagreed: %v", err)
	}
}

func TestVerifyReportsCosignRejection(t *testing.T) {
	v, tarball, _ := setup(t, "rootfs bytes", "", errors.New("exit status 1"))

	_, err := v.Verify(context.Background(), rootfsURL, tarball)
	if err == nil {
		t.Fatal("a cosign failure was reported as success")
	}
	if !strings.Contains(err.Error(), "cosign rejected") ||
		!strings.Contains(err.Error(), "no matching signatures") {
		t.Errorf("err = %v, want cosign's own reason", err)
	}
}

func TestVerifyPinsTheRepositoryIdentity(t *testing.T) {
	// A valid Sigstore signature made by somebody else's workflow -- a fork,
	// say -- must not pass, so the identity regexp has to reach cosign.
	v, tarball, _ := setup(t, "rootfs bytes", "", nil)
	var gotArgs []string
	v.Run = func(_ context.Context, _ string, args ...string) (string, error) {
		gotArgs = args
		return "Verified OK", nil
	}

	if _, err := v.Verify(context.Background(), rootfsURL, tarball); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--certificate-identity-regexp ^https://github.com/hawserhq/hawser/") {
		t.Errorf("identity not pinned: %s", joined)
	}
	if !strings.Contains(joined, "--certificate-oidc-issuer https://token.actions.githubusercontent.com") {
		t.Errorf("issuer not pinned: %s", joined)
	}
}

func TestVerifyReportsMissingMaterialSeparately(t *testing.T) {
	// A release cut before signing existed, or a hand-built rootfs: "cannot
	// check" is a different message from "checked and wrong".
	v, tarball, _ := setup(t, "rootfs bytes", "", nil)
	v.Fetch = &fakeFetch{files: map[string]string{}} // nothing published

	_, err := v.Verify(context.Background(), rootfsURL, tarball)
	var nm *rootfsverify.ErrNoMaterial
	if !errors.As(err, &nm) {
		t.Fatalf("err = %v, want *ErrNoMaterial", err)
	}
	if !strings.Contains(err.Error(), rootfsURL) {
		t.Errorf("the message should name what had no signature: %v", err)
	}
}

func TestVerifyNeedsAVerifier(t *testing.T) {
	// Verification was asked for; silently not doing it is the failure this
	// feature exists to prevent, so an absent cosign is an error.
	v, tarball, _ := setup(t, "rootfs bytes", "", nil)
	v.Cosign = ""
	v.Run = nil
	t.Setenv("PATH", t.TempDir()) // nothing on PATH

	_, err := v.Verify(context.Background(), rootfsURL, tarball)
	if !errors.Is(err, rootfsverify.ErrNoVerifier) {
		t.Fatalf("err = %v, want ErrNoVerifier", err)
	}
}

func TestVerifyRejectsAChecksumFileWithNoHash(t *testing.T) {
	v, tarball, _ := setup(t, "rootfs bytes", "", nil)
	v.Fetch = &fakeFetch{files: map[string]string{
		rootfsURL + ".sha256":               "this is not a checksum file\n",
		rootfsURL + ".sha256.cosign.bundle": "{}",
	}}

	if _, err := v.Verify(context.Background(), rootfsURL, tarball); err == nil {
		t.Fatal("a checksum file with no hash was accepted")
	}
}
