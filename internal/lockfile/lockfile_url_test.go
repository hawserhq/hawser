package lockfile_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/bundle"
	"github.com/wslkit/skrog/internal/lockfile"
)

const goodSum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func lockWithURL(u string) lockfile.Lock {
	return lockfile.Lock{
		SchemaVersion: lockfile.SchemaVersion,
		EngineVersion: "29.8.0",
		Rootfs:        lockfile.Rootfs{URL: u, SHA256: goodSum},
	}
}

// A lock is an artifact handed across an air gap, so rootfs.url is input from
// elsewhere. It used to be accepted as any non-empty string, and its basename
// then named a file on disk (#255).
func TestValidateRejectsARootfsURLThatIsNotOne(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"a bare word", "rootfs.tar.gz"},
		{"no scheme", "//example.com/rootfs.tar.gz"},
		{"basename carries separators", `https://example.com/a\..\..\..\evil.tar.gz`},
		{"basename is dotdot", "https://example.com/.."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := lockWithURL(tc.url).Validate(); err == nil {
				t.Errorf("Validate accepted rootfs.url %q", tc.url)
			}
		})
	}
}

// The legitimate shapes must keep working: air-gapped and custom installs
// point at file:// paths and internal mirrors on purpose.
func TestValidateAcceptsTheURLsRealInstallsUse(t *testing.T) {
	for _, u := range []string{
		"https://github.com/wslkit/skrog/releases/download/rootfs-v29.8.0-2/skrog-rootfs-29.8.0-2.tar.gz",
		"file:///C:/air-gap/skrog-rootfs.tar.gz",
		"https://mirror.corp.example/skrog/rootfs.tar.gz",
	} {
		if err := lockWithURL(u).Validate(); err != nil {
			t.Errorf("Validate rejected a legitimate rootfs.url %q: %v", u, err)
		}
	}
}

// And the name written to disk is ours regardless, so even a lock that somehow
// carried a hostile value could not steer the write. This is the property that
// actually contains the class; the validation above is the second layer.
func TestExtractedNameIsAConstantAndStaysInsideItsDirectory(t *testing.T) {
	if strings.ContainsAny(bundle.ExtractedRootfsName, `/\`) {
		t.Fatalf("ExtractedRootfsName %q contains a separator", bundle.ExtractedRootfsName)
	}
	base := filepath.Join("C:", "state", "offline")
	got := filepath.Join(base, bundle.ExtractedRootfsName)
	if !strings.HasPrefix(got, base+string(filepath.Separator)) {
		t.Errorf("the extracted path %q escapes %q", got, base)
	}
}
