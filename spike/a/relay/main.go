// Spike A throwaway relay: \\.\pipe\skrog_spike -> skrog-spike distro's docker.sock,
// one wsl.exe+socat process per connection. Measures what issue #2 asks for; not product code.
package main

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/Microsoft/go-winio"
)

const pipeName = `\\.\pipe\skrog_spike`

func main() {
	l, err := winio.ListenPipe(pipeName, nil)
	if err != nil {
		log.Fatalf("listen %s: %v", pipeName, err)
	}
	log.Printf("relaying %s -> skrog-spike /var/run/docker.sock", pipeName)
	log.Printf(`try: $env:DOCKER_HOST = "npipe:////./pipe/skrog_spike"; docker version`)
	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		go handle(c)
	}
}

func handle(c net.Conn) {
	start := time.Now()
	defer c.Close()

	cmd := exec.Command("wsl.exe", "-d", "skrog-spike", "-u", "root", "--exec",
		"socat", "STDIO", "UNIX-CONNECT:/var/run/docker.sock")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("stdin pipe: %v", err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("stdout pipe: %v", err)
		return
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("spawn wsl.exe: %v", err)
		return
	}
	log.Printf("conn open: relay process up in %v", time.Since(start))

	// client -> engine; when the client half-closes, propagate EOF via stdin.
	go func() {
		io.Copy(stdin, c)
		stdin.Close()
	}()

	// engine -> client; when the engine is done, half-close our write side
	// so the client can still drain (hijacked streams rely on this).
	io.Copy(c, stdout)
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	cmd.Wait()
	log.Printf("conn closed after %v", time.Since(start))
}
