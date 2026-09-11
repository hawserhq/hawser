// Package remotecert mints the TLS material for exposing the engine over the
// network with mutual TLS (#123): a private CA, a server certificate, and
// client certificates. Only holders of a client cert the CA signed can connect,
// so the engine is never open to the network at large.
//
// Keys are ECDSA P-256 — small, fast, and what a modern docker client expects.
package remotecert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Bundle is a certificate and its private key, PEM-encoded.
type Bundle struct {
	CertPEM []byte
	KeyPEM  []byte
}

// validity is long enough that certs are not a maintenance treadmill, short
// enough that a leaked one does not last forever. Ten years for the CA, since
// rotating it invalidates every client.
const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 2 * 365 * 24 * time.Hour
)

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func encode(certDER []byte, key *ecdsa.PrivateKey) (Bundle, error) {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// GenerateCA creates a self-signed CA for signing the server and client certs.
func GenerateCA() (Bundle, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, err
	}
	sn, err := serial()
	if err != nil {
		return Bundle{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "Skrog Engine CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Bundle{}, err
	}
	return encode(der, key)
}

func parseCA(ca Bundle) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(ca.CertPEM)
	kb, _ := pem.Decode(ca.KeyPEM)
	if cb == nil || kb == nil {
		return nil, nil, fmt.Errorf("invalid CA bundle")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// GenerateServer signs a server certificate for the given hostnames/IPs (SANs).
// A client verifies the address it dialed against these, so every name or IP the
// engine will be reached by must be included.
func GenerateServer(ca Bundle, hosts []string) (Bundle, error) {
	caCert, caKey, err := parseCA(ca)
	if err != nil {
		return Bundle{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, err
	}
	sn, err := serial()
	if err != nil {
		return Bundle{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "skrog-engine"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return Bundle{}, err
	}
	return encode(der, key)
}

// GenerateClient signs a client certificate. name is a label (the CN), for
// telling clients apart in a revocation-by-reissue scheme.
func GenerateClient(ca Bundle, name string) (Bundle, error) {
	caCert, caKey, err := parseCA(ca)
	if err != nil {
		return Bundle{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, err
	}
	sn, err := serial()
	if err != nil {
		return Bundle{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return Bundle{}, err
	}
	return encode(der, key)
}
