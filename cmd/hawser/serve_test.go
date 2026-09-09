package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zcsizmadia/hawser/internal/remotecert"
)

// mintInto writes a CA, server, and client bundle into dir the way
// `hawser serve cert` does, and returns the client bundle for dialing.
func mintInto(t *testing.T, dir string, hosts []string) remotecert.Bundle {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ca, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := remotecert.GenerateServer(ca, hosts)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(dir, "server", srv); err != nil {
		t.Fatal(err)
	}
	cl, err := remotecert.GenerateClient(ca, "test-client")
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// clientConfig builds the docker-equivalent client side: trust our CA, present
// our client cert.
func clientConfig(t *testing.T, dir string, cl remotecert.Bundle, present bool) *tls.Config {
	t.Helper()
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("client could not load CA")
	}
	cfg := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
	if present {
		pair, err := tls.X509KeyPair(cl.CertPEM, cl.KeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg
}

// TestServeMutualTLSHandshake stands up a real TLS listener with the production
// serverTLSConfig and proves a client with the signed cert completes the
// handshake while one without it is refused — the live check the e2e suite runs
// against the engine, reduced to the transport so it runs in CI.
func TestServeMutualTLSHandshake(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	cl := mintInto(t, dir, []string{"127.0.0.1", "localhost"})

	cfg, err := serverTLSConfig(dir)
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Echo one byte so a completed handshake is observable end to end.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1)
				if _, err := io.ReadFull(c, buf); err == nil {
					c.Write(buf)
				}
			}(c)
		}
	}()
	addr := ln.Addr().String()

	// With the client cert: handshake succeeds and the byte round-trips.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr,
		clientConfig(t, dir, cl, true))
	if err != nil {
		t.Fatalf("authorized client should connect: %v", err)
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil || buf[0] != 'x' {
		t.Fatalf("echo failed: %v %q", err, buf)
	}
	conn.Close()

	// Without a client cert the server must refuse. Under TLS 1.3 the client's
	// Dial can return before the server rejects the (absent) client cert, so the
	// refusal surfaces on the first I/O — exactly as a real `docker version`
	// (which does I/O) would see it. So attempt a round-trip and require failure.
	bad, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr,
		clientConfig(t, dir, cl, false))
	if err == nil {
		bad.SetDeadline(time.Now().Add(5 * time.Second))
		_, wErr := bad.Write([]byte("x"))
		buf := make([]byte, 1)
		_, rErr := io.ReadFull(bad, buf)
		bad.Close()
		if wErr == nil && rErr == nil {
			t.Fatal("a client with no certificate exchanged data; mutual TLS not enforced")
		}
	}
}

// TestServerTLSConfigMissingMaterial reports a helpful error, not a panic, when
// `serve cert` has not been run.
func TestServerTLSConfigMissingMaterial(t *testing.T) {
	if _, err := serverTLSConfig(t.TempDir()); err == nil {
		t.Fatal("expected an error when the TLS material is absent")
	}
}

// TestServeCertReusesCA proves a second `serve cert` keeps the same CA, so
// previously issued client certs keep working.
func TestServeCertReusesCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CertPEM) != string(second.CertPEM) {
		t.Fatal("second loadOrCreateCA regenerated the CA; existing clients would break")
	}
}
