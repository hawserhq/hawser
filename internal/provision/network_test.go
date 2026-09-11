package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/wsl"
)

// capWSL records the shell commands applyNetwork runs in the distro.
type capWSL struct{ execs []string }

func (c *capWSL) Status(context.Context) (wsl.Status, error)           { return wsl.Status{}, nil }
func (c *capWSL) Import(context.Context, string, string, string) error { return nil }
func (c *capWSL) Export(context.Context, string, string) error         { return nil }
func (c *capWSL) Unregister(context.Context, string) error             { return nil }
func (c *capWSL) Terminate(context.Context, string) error              { return nil }
func (c *capWSL) List(context.Context) ([]wsl.Distro, error)           { return nil, nil }
func (c *capWSL) Exec(_ context.Context, _, _ string, args ...string) (string, error) {
	c.execs = append(c.execs, strings.Join(args, " "))
	return "", nil
}
func (c *capWSL) Start(context.Context, string, string, ...string) (func(), error) {
	return func() {}, nil
}

func TestProxyEnv(t *testing.T) {
	if proxyEnv("", "") != "" {
		t.Error("empty proxy should yield an empty env file")
	}
	env := proxyEnv("http://px:8080", "internal.corp")
	for _, want := range []string{
		`export HTTP_PROXY="http://px:8080"`,
		`export https_proxy="http://px:8080"`,
		"internal.corp",
		"localhost,127.0.0.1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("proxy env missing %q:\n%s", want, env)
		}
	}
}

func TestApplyNetworkWritesProxyAndCA(t *testing.T) {
	w := &capWSL{}
	p := &Provisioner{WSL: w}
	opts := Options{Distro: "d", StateDir: t.TempDir(), Network: NetConfig{
		Proxy:     "http://px:8080",
		HostCAPEM: []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
	}}
	p.applyNetwork(context.Background(), opts.withDefaults())

	joined := strings.Join(w.execs, "\n")
	if !strings.Contains(joined, "/etc/hawser/network.env") {
		t.Errorf("network.env not written; execs=%v", w.execs)
	}
	if !strings.Contains(joined, hostCABundle) {
		t.Errorf("host CA not written; execs=%v", w.execs)
	}
	if !strings.Contains(joined, "update-ca-certificates") {
		t.Errorf("update-ca-certificates not run; execs=%v", w.execs)
	}
}

func TestApplyNetworkEmptyClearsCA(t *testing.T) {
	w := &capWSL{}
	p := &Provisioner{WSL: w}
	p.applyNetwork(context.Background(), Options{Distro: "d", StateDir: t.TempDir()}.withDefaults())

	joined := strings.Join(w.execs, "\n")
	if !strings.Contains(joined, "/etc/hawser/network.env") {
		t.Errorf("network.env should still be written; execs=%v", w.execs)
	}
	if !strings.Contains(joined, "rm -f "+hostCAGlob) {
		t.Errorf("host CA should be removed when off; execs=%v", w.execs)
	}
}
