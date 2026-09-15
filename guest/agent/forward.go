//go:build linux

package main

// The forward listener carries published container ports to Windows (#330).
//
// dockerd publishes a port inside the session VM, and the relay that would
// carry it to the host is driven from the Windows side by wslcsession — so a
// bridge that talks straight to the engine socket gets containers but no
// reachable ports. Measured: with a port published through the socket, nothing
// on Windows can reach it, on any address. Skrog therefore runs its own relay,
// and this is its guest half: accept a vsock connection, take one CONNECT line
// naming a TCP endpoint in the VM, and relay.
//
// It is a separate vsock port from the engine relay on purpose. That port's
// contract stays "the engine socket and nothing else", so learning the secret
// cannot turn the working path into a general TCP proxy.

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/wslkit/skrog/internal/vsockproto"
	"golang.org/x/sys/unix"
)

// dialTimeout bounds the connection to the container. The target is inside
// this VM, so anything slower than this is a container that is not listening
// rather than a slow network.
const dialTimeout = 5 * time.Second

func runForward(port uint32, secret string) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket(AF_VSOCK): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		return fmt.Errorf("bind(vsock:%d): %w", port, err)
	}
	if err := unix.Listen(fd, 32); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Printf("%s forward listener on vsock port %d", Identity, port)

	for {
		cfd, peer, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if err != nil {
			switch err {
			case unix.EINTR, unix.ECONNABORTED:
				continue
			case unix.EMFILE, unix.ENFILE, unix.ENOBUFS, unix.ENOMEM:
				log.Printf("forward accept: %v; backing off", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("accept: %w", err)
		}
		vm, ok := peer.(*unix.SockaddrVM)
		if !ok || vm.CID != vsockHostCID {
			log.Printf("forward: rejected connection from non-host peer %+v", peer)
			unix.Close(cfd)
			continue
		}
		conn, err := newVsockConn(cfd)
		if err != nil {
			log.Printf("forward: wrapping connection: %v", err)
			unix.Close(cfd)
			continue
		}
		go serveForward(conn, secret)
	}
}

func serveForward(conn *vsockConn, secret string) {
	defer conn.Close()

	// Same handshake as the engine relay: an unproven peer gets nothing, and
	// in particular cannot name a target.
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	if err := vsockproto.ServerHandshake(conn, Identity, secret); err != nil {
		log.Printf("forward: handshake refused: %v", err)
		return
	}

	target, err := vsockproto.ReadTarget(conn)
	if err != nil {
		log.Printf("forward: %v", err)
		return
	}
	conn.SetReadDeadline(time.Time{})

	backend, err := net.DialTimeout("tcp", target, dialTimeout)
	if err != nil {
		// The host cannot distinguish "container gone" from "relay broken"
		// without this, and a published port whose container has exited is an
		// ordinary race rather than a fault.
		log.Printf("forward: dialing %s: %v", target, err)
		return
	}
	defer backend.Close()

	if err := vsockproto.Relay(conn, backend); err != nil && !ignorable(err) {
		log.Printf("forward relay to %s: %v", target, err)
	}
}
