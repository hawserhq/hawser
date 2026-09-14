package vsockproto

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// ForwardPrefix opens a forwarded connection. It is sent by the host after a
// successful handshake, on the agent's forward port only, and names a TCP
// endpoint inside the guest:
//
//	CONNECT 172.17.0.3:80\n
//
// This exists for published ports (#330). dockerd publishes a container's port
// inside the session VM and WSLC's own relay — the thing that carries it to
// Windows — is driven from the Windows side by wslcsession, so a bridge that
// talks straight to the engine socket gets containers but no reachable ports.
// Skrog therefore runs its own relay, and this line is how the Windows end
// tells the guest end where to connect.
//
// It is deliberately NOT accepted on the engine port. That port's contract is
// "relay to the engine socket and nothing else", and keeping it that way means
// the working path cannot be turned into an arbitrary TCP proxy by a peer that
// learns the secret.
const ForwardPrefix = "CONNECT "

// maxTarget bounds the target line. A host:port is far shorter; anything
// longer is not a peer of ours.
const maxTarget = 256

// WriteTarget sends the forward request.
func WriteTarget(w io.Writer, target string) error {
	if err := ValidTarget(target); err != nil {
		return err
	}
	if _, err := io.WriteString(w, ForwardPrefix+target+"\n"); err != nil {
		return fmt.Errorf("writing forward target: %w", err)
	}
	return nil
}

// ReadTarget reads and validates the forward request. The agent calls this
// after the handshake; a malformed or out-of-range target is refused before
// any connection is attempted.
func ReadTarget(r io.Reader) (string, error) {
	line, err := readLine(r)
	if err != nil {
		return "", fmt.Errorf("reading forward target: %w", err)
	}
	if !strings.HasPrefix(line, ForwardPrefix) {
		return "", fmt.Errorf("expected %q, got %q", strings.TrimSpace(ForwardPrefix), line)
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, ForwardPrefix))
	if err := ValidTarget(target); err != nil {
		return "", err
	}
	return target, nil
}

// ValidTarget rejects anything that is not a plain host:port.
//
// The agent runs as root in the session VM's root namespace, so a forwarded
// connection can reach anything the VM can. The secret is the real gate — an
// unproven peer never gets this far — but validating still matters: it keeps a
// malformed target from becoming a confusing dial error, and it bounds what a
// bug on the host side can ask for.
func ValidTarget(target string) error {
	if target == "" {
		return fmt.Errorf("empty forward target")
	}
	if len(target) > maxTarget {
		return fmt.Errorf("forward target exceeds %d bytes", maxTarget)
	}
	if strings.ContainsAny(target, "\r\n\x00") {
		return fmt.Errorf("forward target contains a control character")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("forward target %q is not host:port: %w", target, err)
	}
	if host == "" {
		return fmt.Errorf("forward target %q has no host", target)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("forward target %q has an invalid port", target)
	}
	return nil
}
