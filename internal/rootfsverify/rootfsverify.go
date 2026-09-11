// Package rootfsverify checks a rootfs tarball's Sigstore signature before it
// becomes root inside the engine VM (#147).
//
// The SHA-256 pin in internal/release/manifest.json is always enforced and is
// not what this adds. The pin says "these bytes are the bytes this build
// expects"; a signature says "the project's release workflow produced them".
// They fail differently: someone who can alter a download can usually also
// alter a manifest in a fork, and cannot forge a Sigstore certificate for the
// repository's OIDC identity.
//
// The chain is deliberately short, and reuses what the release workflows
// publish (release.yml, rootfs.yml):
//
//	cosign signature over <rootfs>.sha256   ->   that file's digest matches the
//	                                             tarball we just downloaded
//
// Signing the checksum file rather than the tarball keeps the signed artifact
// tiny and covers the same bytes.
//
// Verification is opt-in (`hawser config set install.verify-signature on`) for
// two honest reasons. It needs cosign on PATH -- vendoring sigstore-go would
// multiply the size of a binary budgeted under 15 MB, and shelling out to a
// tool Hawser does not ship cannot be a silent default. And an air-gapped
// install (#75) has no transparency log to reach, so a default-on check would
// break the one install path that most needs to be predictable.
package rootfsverify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
)

// ErrNoVerifier reports that no supported verifier is installed. It is an error
// rather than a skip: verification was asked for, and quietly not doing it is
// the failure mode this whole feature exists to avoid.
var ErrNoVerifier = errors.New("rootfsverify: cosign is not on PATH")

// ErrNoMaterial reports that the signature files are not published beside the
// rootfs -- a release cut before signing existed, or a hand-built rootfs.
type ErrNoMaterial struct{ URL string }

func (e *ErrNoMaterial) Error() string {
	return fmt.Sprintf("rootfsverify: no signature published beside %s", e.URL)
}

// ErrMismatch reports a signed checksum that does not match the tarball. This
// is the interesting failure: the signature verified, and it attests to
// different bytes than the ones on disk.
type ErrMismatch struct {
	Signed string
	Actual string
}

func (e *ErrMismatch) Error() string {
	return fmt.Sprintf("rootfsverify: the signed checksum is %s but the downloaded rootfs is %s",
		e.Signed, e.Actual)
}

// Fetcher retrieves a URL. Injected so the verifier is testable without a
// network, and so it reuses whatever the provisioner already uses.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

// Verifier checks a downloaded rootfs against its published signature.
type Verifier struct {
	// Fetch retrieves the .sha256 and .cosign.bundle files.
	Fetch Fetcher
	// Repo is the GitHub repository whose workflow must have signed it, e.g.
	// "hawserhq/hawser". The certificate identity is required to start with
	// https://github.com/<repo>/, so a signature made by any other repository's
	// workflow -- including a fork's -- is rejected.
	Repo string
	// Cosign overrides the cosign binary (tests).
	Cosign string
	// Run executes the verifier; nil uses exec. Injected for tests.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// TempDir holds the fetched signature material; empty uses os.TempDir.
	TempDir string
}

// Result describes what was verified, for logging and --json.
type Result struct {
	// Verified is true only when the signature checked out AND its checksum
	// matched the tarball.
	Verified bool `json:"verified"`
	// SignedBy is the certificate identity that signed, e.g. the workflow ref.
	SignedBy string `json:"signedBy,omitempty"`
	// Digest is the tarball's SHA-256.
	Digest string `json:"digest,omitempty"`
}

// Verify checks the tarball at path against the signature published beside
// rootfsURL. It returns ErrNoVerifier when cosign is absent and *ErrNoMaterial
// when the release carries no signature, so a caller can tell "cannot check"
// from "checked and wrong" -- they deserve different messages.
func (v *Verifier) Verify(ctx context.Context, rootfsURL, tarball string) (Result, error) {
	cosign := v.Cosign
	if cosign == "" {
		p, err := exec.LookPath("cosign")
		if err != nil {
			return Result{}, ErrNoVerifier
		}
		cosign = p
	}

	sumsURL := rootfsURL + ".sha256"
	bundleURL := sumsURL + ".cosign.bundle"
	dir, err := os.MkdirTemp(v.TempDir, "hawser-verify-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)

	sums := path.Join(dir, "rootfs.sha256")
	bundle := path.Join(dir, "rootfs.sha256.cosign.bundle")
	if err := v.download(ctx, sumsURL, sums); err != nil {
		return Result{}, &ErrNoMaterial{URL: rootfsURL}
	}
	if err := v.download(ctx, bundleURL, bundle); err != nil {
		return Result{}, &ErrNoMaterial{URL: rootfsURL}
	}

	// The identity is pinned to the repository's own workflows: a valid
	// Sigstore signature made by somebody else's workflow must not pass.
	identity := "^https://github.com/" + v.Repo + "/"
	out, err := v.run(ctx, cosign, "verify-blob",
		"--bundle", bundle,
		"--certificate-identity-regexp", identity,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		sums)
	if err != nil {
		return Result{}, fmt.Errorf("rootfsverify: cosign rejected the signature: %w: %s",
			err, firstLine(out))
	}

	signed, err := signedDigest(sums)
	if err != nil {
		return Result{}, err
	}
	actual, err := fileSHA256(tarball)
	if err != nil {
		return Result{}, err
	}
	if !strings.EqualFold(signed, actual) {
		return Result{}, &ErrMismatch{Signed: signed, Actual: actual}
	}
	return Result{Verified: true, SignedBy: identity, Digest: actual}, nil
}

func (v *Verifier) run(ctx context.Context, name string, args ...string) (string, error) {
	if v.Run != nil {
		return v.Run(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func (v *Verifier) download(ctx context.Context, url, dest string) error {
	body, err := v.Fetch.Fetch(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	// Signature material is tiny; a cap stops a hostile or wrong URL from
	// filling the disk before anything is verified.
	if _, err := io.Copy(f, io.LimitReader(body, 1<<20)); err != nil {
		return err
	}
	return nil
}

// signedDigest reads the hash out of a `<hash>  <filename>` checksum file.
func signedDigest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 1 && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("rootfsverify: %s has no sha256 line", path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
