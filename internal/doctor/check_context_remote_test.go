package doctor

import (
	"testing"
	"time"

	"github.com/zcsizmadia/hawser/internal/remote"
	"github.com/zcsizmadia/hawser/internal/version"
)

// Remote-engine contexts (#138): hawser-<name> is OK when the remote is known
// and its client certificate is healthy; the failure that matters is a
// certificate about to (or already) stop working.
func TestCheckContextRemote(t *testing.T) {
	onRemote := func(name string, remotes ...remote.Info) Facts {
		return Facts{
			Report: report(version.Report{
				Engine:        version.EngineInfo{Installed: true},
				Context:       remote.ContextName(name),
				ContextSource: "docker config.json",
			}),
			Remotes: remotes,
		}
	}
	desk := func(notAfter time.Time) remote.Info {
		return remote.Info{Name: "desk", Host: "tcp://desktop:2376", CertNotAfter: notAfter}
	}

	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"known remote, cert healthy", onRemote("desk", desk(time.Now().Add(400*24*time.Hour))), OK},
		{"known remote, cert expiring soon", onRemote("desk", desk(time.Now().Add(3*24*time.Hour))), Warn},
		{"known remote, cert expired", onRemote("desk", desk(time.Now().Add(-time.Hour))), Fail},
		{"known remote, no expiry recorded", onRemote("desk", desk(time.Time{})), OK},
		{"hawser- context but no such remote", onRemote("ghost", desk(time.Now().Add(400*24*time.Hour))), Warn},
	}
	c := checkContext()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(tc.f).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
