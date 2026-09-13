//go:build windows && livepath

package dockercli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wslkit/skrog/internal/dockercli"
)

// Guarded by a build tag: this reads the machine's real PATH registry values,
// which is exactly what makes it worth running by hand and unfit for CI.
//
//	go test -tags livepath ./internal/dockercli/ -run LivePathScope -v
func TestLivePathScope(t *testing.T) {
	local := os.Getenv("LOCALAPPDATA")
	for _, tc := range []struct {
		dir  string
		want dockercli.Scope
	}{
		{`C:\Windows\system32`, dockercli.ScopeMachine},
		{filepath.Join(local, `Skrog\bin`), dockercli.ScopeUser},
		{`C:\definitely\not\on\path`, dockercli.ScopeUnknown},
	} {
		got := dockercli.PathScopeOf(tc.dir)
		if got != tc.want {
			t.Errorf("PathScopeOf(%q) = %v, want %v", tc.dir, got, tc.want)
		}
		t.Logf("%-45s scope=%v\n    %s", tc.dir, got, dockercli.ShadowAdvice(got, tc.dir, ""))
	}
}
