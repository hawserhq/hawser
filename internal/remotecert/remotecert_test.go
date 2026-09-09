package remotecert

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

// pool parses a CA bundle into a cert pool for verification.
func pool(t *testing.T, ca Bundle) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("CA PEM did not parse")
	}
	return p
}

// leaf parses the certificate half of a bundle.
func leaf(t *testing.T, b Bundle) *x509.Certificate {
	t.Helper()
	pair, err := tls.X509KeyPair(b.CertPEM, b.KeyPEM)
	if err != nil {
		t.Fatalf("bundle is not a valid cert/key pair: %v", err)
	}
	c, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parsing leaf: %v", err)
	}
	return c
}

func TestServerCertVerifiesAgainstCA(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := GenerateServer(ca, []string{"localhost", "127.0.0.1", "engine.example"})
	if err != nil {
		t.Fatal(err)
	}
	cert := leaf(t, srv)
	roots := pool(t, ca)

	// The name the client dialed must verify — both an IP SAN and a DNS SAN.
	for _, name := range []string{"localhost", "127.0.0.1", "engine.example"} {
		if _, err := cert.Verify(x509.VerifyOptions{
			DNSName:   name,
			Roots:     roots,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("server cert should verify for %q: %v", name, err)
		}
	}

	// A name that is not a SAN must fail — the whole point of pinning.
	if _, err := cert.Verify(x509.VerifyOptions{
		DNSName:   "evil.example",
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err == nil {
		t.Error("server cert should NOT verify for an unlisted host")
	}
}

func TestClientCertVerifiesForClientAuth(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	cl, err := GenerateClient(ca, "ci-runner")
	if err != nil {
		t.Fatal(err)
	}
	cert := leaf(t, cl)
	if cert.Subject.CommonName != "ci-runner" {
		t.Errorf("client CN = %q, want ci-runner", cert.Subject.CommonName)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool(t, ca),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("client cert should verify for client auth: %v", err)
	}
}

// TestForeignCADoesNotVerify proves a client cert from a different CA is
// rejected — the guarantee that gates who can reach the engine.
func TestForeignCADoesNotVerify(t *testing.T) {
	ours, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	cl, err := GenerateClient(stranger, "impostor")
	if err != nil {
		t.Fatal(err)
	}
	cert := leaf(t, cl)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool(t, ours),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err == nil {
		t.Error("a client cert signed by a foreign CA must not verify against ours")
	}
}

func TestCAIsCA(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	cert := leaf(t, ca)
	if !cert.IsCA {
		t.Error("CA cert should have IsCA set")
	}
	// A leaf must not be able to sign further certs.
	srv, err := GenerateServer(ca, []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if leaf(t, srv).IsCA {
		t.Error("server cert must not be a CA")
	}
}
