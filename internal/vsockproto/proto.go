// Package vsockproto is the tiny wire protocol between the Windows host and
// the in-distro hawser-agent (#40), shared by both ends.
//
// The connection starts line-oriented — the client says hello, the agent
// answers with its identity — and then goes fully transparent, a byte relay to
// the engine socket. The handshake exists because the WSL2 utility VM's vsock
// port space is shared by every distro in it (Docker Desktop's included): a
// dial that reaches the wrong listener must fail closed, not speak Docker HTTP
// at a stranger.
//
// When both ends hold a per-install secret, the handshake is mutually
// authenticated (#81): while the engine distro is idle-stopped a hostile
// sibling distro can bind the shared vsock port and answer, but it cannot read
// the secret (root-only inside the engine distro) and so cannot prove itself —
// the host refuses to send a byte of docker traffic to an unproven agent, and
// the agent refuses an unproven caller. The scheme is backward compatible: the
// opening line is unchanged, a secret-less side speaks the original v1
// handshake, and a side that HOLDS a secret refuses to downgrade to it.
package vsockproto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Port is the vsock port the agent listens on: ASCII "haws". Vsock ports are
// a full 32-bit space with no well-known registry, so an implausible value is
// the collision strategy.
const Port uint32 = 0x68617773

const (
	hello     = "HAWSER/1\n"
	okPrefix  = "OK "  // v1 (no auth) banner
	ok2Prefix = "OK/2" // v2 banner: server advertises it holds a secret
	authPref  = "AUTH "
	proofPref = "OK2 "
	// maxLine bounds handshake reads; anything longer is not our peer.
	maxLine = 256
)

// readLine reads up to and including '\n' one byte at a time. Byte-wise on
// purpose: a buffered reader could swallow bytes that belong to the
// transparent phase that follows.
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < maxLine {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(buf[0])
	}
	return "", fmt.Errorf("handshake line exceeds %d bytes", maxLine)
}

func nonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// proof is HMAC(secret, domain+":"+nonce). The domain separates the client's
// proof from the server's so neither can be replayed as the other.
func proof(secret, domain, nonce string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(domain + ":" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

func proofsEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ServerHandshake validates the client hello and authenticates the connection.
// When secret is non-empty the agent advertises auth and requires the client
// to prove the secret before the relay begins; with an empty secret it speaks
// the v1 identity-only handshake. On any failure the error is returned and the
// caller closes — a stranger learns nothing and reaches no docker traffic.
func ServerHandshake(c io.ReadWriter, identity, secret string) error {
	line, err := readLine(c)
	if err != nil {
		return fmt.Errorf("reading hello: %w", err)
	}
	if line+"\n" != hello {
		return fmt.Errorf("unexpected hello %q", line)
	}

	if secret == "" {
		// v1: identity only.
		_, err := c.Write([]byte(okPrefix + identity + "\n"))
		return err
	}

	// v2: advertise auth with a fresh server nonce.
	sn, err := nonce()
	if err != nil {
		return err
	}
	if _, err := c.Write([]byte(ok2Prefix + " " + identity + " " + sn + "\n")); err != nil {
		return fmt.Errorf("writing banner: %w", err)
	}

	// Client authenticates itself first: AUTH <clientNonce> <proof over sn>.
	authLine, err := readLine(c)
	if err != nil {
		return fmt.Errorf("reading auth: %w", err)
	}
	if !strings.HasPrefix(authLine, authPref) {
		return fmt.Errorf("client did not authenticate")
	}
	fields := strings.Fields(strings.TrimPrefix(authLine, authPref))
	if len(fields) != 2 {
		return fmt.Errorf("malformed auth line")
	}
	clientNonce, clientProof := fields[0], fields[1]
	if !proofsEqual(clientProof, proof(secret, "C", sn)) {
		return fmt.Errorf("client failed authentication")
	}

	// Then the server proves itself over the client's nonce.
	if _, err := c.Write([]byte(proofPref + proof(secret, "S", clientNonce) + "\n")); err != nil {
		return fmt.Errorf("writing server proof: %w", err)
	}
	return nil
}

// ClientHandshake sends the hello, authenticates the peer, and returns the
// agent's identity. When secret is non-empty the client requires the agent to
// advertise and prove the secret — a plain v1 banner is refused as a downgrade
// (a squatting sibling distro cannot know the secret). With an empty secret it
// accepts the v1 banner, the pre-#81 behavior.
func ClientHandshake(c io.ReadWriter, secret string) (string, error) {
	if _, err := c.Write([]byte(hello)); err != nil {
		return "", fmt.Errorf("writing hello: %w", err)
	}
	banner, err := readLine(c)
	if err != nil {
		return "", fmt.Errorf("reading banner: %w", err)
	}

	if secret == "" {
		if !strings.HasPrefix(banner, okPrefix) {
			return "", fmt.Errorf("peer is not a hawser agent: %q", banner)
		}
		return strings.TrimPrefix(banner, okPrefix), nil
	}

	// We hold a secret: the agent must advertise v2, or we refuse to talk (a
	// v1 banner here is either an old rootfs — take the socat fallback — or an
	// impersonator dodging auth).
	if !strings.HasPrefix(banner, ok2Prefix+" ") {
		return "", fmt.Errorf("agent did not offer authentication (refusing downgrade): %q", banner)
	}
	fields := strings.Fields(strings.TrimPrefix(banner, ok2Prefix+" "))
	if len(fields) != 2 {
		return "", fmt.Errorf("malformed auth banner")
	}
	identity, serverNonce := fields[0], fields[1]

	cn, err := nonce()
	if err != nil {
		return "", err
	}
	if _, err := c.Write([]byte(authPref + cn + " " + proof(secret, "C", serverNonce) + "\n")); err != nil {
		return "", fmt.Errorf("writing auth: %w", err)
	}

	proofLine, err := readLine(c)
	if err != nil {
		return "", fmt.Errorf("reading server proof: %w", err)
	}
	if !strings.HasPrefix(proofLine, proofPref) {
		return "", fmt.Errorf("agent failed authentication")
	}
	if !proofsEqual(strings.TrimPrefix(proofLine, proofPref), proof(secret, "S", cn)) {
		return "", fmt.Errorf("agent failed authentication")
	}
	return identity, nil
}
