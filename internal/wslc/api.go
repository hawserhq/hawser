package wslc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// APIVersion is the Docker API version the port watcher pins its requests to.
//
// The engine in a wslc session is 25.0.3, whose API is 1.44 with a minimum of
// 1.24 (#317). Asking for something it does not have would fail the whole
// watcher, and the watcher only needs endpoints that have been stable for
// years, so it asks for the floor rather than the ceiling.
const APIVersion = "v1.24"

// apiGet performs one GET against the engine over a fresh connection and
// returns the body. Connection: close keeps the framing trivial — there is no
// keep-alive state to manage and the dialer hands out a new connection per
// request anyway.
func apiGet(ctx context.Context, dial func(context.Context) (io.ReadWriteCloser, error), path string) ([]byte, error) {
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := io.WriteString(conn,
		"GET "+path+" HTTP/1.1\r\nHost: docker\r\nConnection: close\r\n\r\n"); err != nil {
		return nil, fmt.Errorf("writing GET %s: %w", path, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return nil, fmt.Errorf("reading GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading GET %s body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return body, nil
}

// apiStream performs a GET whose response never ends — /events — and calls fn
// for each JSON object as it arrives.
//
// It returns when the context is cancelled, the engine closes the stream, or
// fn's connection breaks. The caller is expected to reconnect: on this backend
// the engine goes away whenever the session VM idle-terminates, so a stream
// ending is routine rather than exceptional.
func apiStream(ctx context.Context, dial func(context.Context) (io.ReadWriteCloser, error), path string, fn func([]byte)) error {
	conn, err := dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Closing the connection is what unblocks the read below; the engine has
	// no other way to be told we are done.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	if _, err := io.WriteString(conn,
		"GET "+path+" HTTP/1.1\r\nHost: docker\r\n\r\n"); err != nil {
		return fmt.Errorf("writing GET %s: %w", path, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return fmt.Errorf("reading GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decoding the event stream: %w", err)
		}
		fn(raw)
	}
}

// containerEvent is the slice of a Docker event the port watcher acts on.
type containerEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	ID     string `json:"id"`
	Actor  struct {
		ID string `json:"ID"`
	} `json:"Actor"`
}

// id prefers the Actor form, which is where modern engines put it; the
// top-level "id" is the pre-1.22 spelling and still arrives from some paths.
func (e containerEvent) id() string {
	if e.Actor.ID != "" {
		return e.Actor.ID
	}
	return e.ID
}

// inspectPorts is the part of a container inspect the watcher reads.
type inspectPorts struct {
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Ports     map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ip returns the address the guest relay should dial.
//
// A container on a user-defined network leaves the top-level IPAddress empty
// and reports per-network addresses instead, which is what `docker network
// create` plus `--network` produces — and what Compose does for every project
// by default, so this is the common case rather than the exotic one.
func (i inspectPorts) ip() string {
	if i.NetworkSettings.IPAddress != "" {
		return i.NetworkSettings.IPAddress
	}
	for _, n := range i.NetworkSettings.Networks {
		if n.IPAddress != "" {
			return n.IPAddress
		}
	}
	return ""
}
