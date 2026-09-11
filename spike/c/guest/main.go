//go:build linux

// Spike C guest: an AF_VSOCK echo listener, standing in for the future
// skrog-agent (#40). Listens on the given port for connections from the
// Windows host, sends a banner, then echoes until EOF.
//
// This is the exact socket family the real agent will use; if this accepts a
// connection from the host probe, the transport question of #40 is settled.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	port := uint32(5000)
	if len(os.Args) > 1 {
		p, err := strconv.ParseUint(os.Args[1], 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad port %q: %v\n", os.Args[1], err)
			os.Exit(2)
		}
		port = uint32(p)
	}

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socket(AF_VSOCK): %v\n", err)
		os.Exit(1)
	}
	// VMADDR_CID_ANY: the guest does not need to know its own CID.
	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		fmt.Fprintf(os.Stderr, "bind(vsock:%d): %v\n", port, err)
		os.Exit(1)
	}
	if err := unix.Listen(fd, 8); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("listening on vsock port %d\n", port)

	for {
		cfd, peer, err := unix.Accept(fd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept: %v\n", err)
			os.Exit(1)
		}
		if vm, ok := peer.(*unix.SockaddrVM); ok {
			fmt.Printf("accepted from cid=%d port=%d\n", vm.CID, vm.Port)
		}
		go func(fd int) {
			f := os.NewFile(uintptr(fd), "vsock-conn")
			defer f.Close()
			f.WriteString("skrog-spike-c hello from the guest\n")
			io.Copy(f, f) // echo until the host closes
		}(cfd)
	}
}
