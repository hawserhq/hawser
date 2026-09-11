package pipeproxy_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// fakeEngine records the requests it receives so tests can assert on exactly
// what the proxy forwarded, and replies with whatever the test needs.
type fakeEngine struct {
	l net.Listener

	mu   sync.Mutex
	reqs []*recordedRequest

	// respond writes the reply for request number n (0-based).
	respond func(n int, w io.Writer, req *http.Request)
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
}

func newFakeEngine(t *testing.T, respond func(n int, w io.Writer, req *http.Request)) *fakeEngine {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	e := &fakeEngine{l: l, respond: respond}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go e.serve(c)
		}
	}()
	t.Cleanup(func() { l.Close() })
	return e
}

func (e *fakeEngine) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for n := 0; ; n++ {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(req.Body)
		req.Body.Close()

		e.mu.Lock()
		e.reqs = append(e.reqs, &recordedRequest{
			Method: req.Method, Path: req.URL.Path, Body: string(body),
		})
		e.mu.Unlock()

		e.respond(n, c, req)
	}
}

func (e *fakeEngine) requests() []*recordedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*recordedRequest, len(e.reqs))
	copy(out, e.reqs)
	return out
}

func (e *fakeEngine) dialer() pipeproxy.Dialer {
	return pipeproxy.DialerFunc(func(ctx context.Context) (io.ReadWriteCloser, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", e.l.Addr().String())
	})
}

// okJSON is the ordinary engine reply: a short JSON body, keep-alive.
func okJSON(body string) func(int, io.Writer, *http.Request) {
	return func(_ int, w io.Writer, _ *http.Request) {
		fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
			len(body), body)
	}
}

// startRewriteProxy runs a Server with the bind-rewriting handler.
func startRewriteProxy(t *testing.T, d pipeproxy.Dialer) (addr string, stop func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &pipeproxy.Server{Dialer: d, Handler: pipeproxy.RewriteBinds}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()
	return l.Addr().String(), func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
		}
	}
}

// postCreate sends a container-create request through the proxy and returns the
// response body.
func postCreate(t *testing.T, addr, path, body string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	fmt.Fprintf(c, "POST %s HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		path, len(body), body)

	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(resp)
}

func TestRewriteTranslatesBinds(t *testing.T) {
	engine := newFakeEngine(t, okJSON(`{"Id":"abc"}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	postCreate(t, addr, "/v1.55/containers/create",
		`{"Image":"alpine","HostConfig":{"Binds":["C:\\src:/app","myvol:/data"]}}`)

	reqs := engine.requests()
	if len(reqs) != 1 {
		t.Fatalf("engine saw %d requests, want 1", len(reqs))
	}

	var got struct {
		Image      string
		HostConfig struct{ Binds []string }
	}
	if err := json.Unmarshal([]byte(reqs[0].Body), &got); err != nil {
		t.Fatalf("engine received invalid JSON %q: %v", reqs[0].Body, err)
	}
	want := []string{"/mnt/c/src:/app", "myvol:/data"}
	if len(got.HostConfig.Binds) != len(want) {
		t.Fatalf("got binds %v, want %v", got.HostConfig.Binds, want)
	}
	for i := range want {
		if got.HostConfig.Binds[i] != want[i] {
			t.Errorf("bind %d = %q, want %q", i, got.HostConfig.Binds[i], want[i])
		}
	}
	// Unrelated fields must survive the round-trip.
	if got.Image != "alpine" {
		t.Errorf("Image = %q, want alpine", got.Image)
	}
}

func TestRewriteTranslatesMountSource(t *testing.T) {
	engine := newFakeEngine(t, okJSON(`{"Id":"abc"}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	postCreate(t, addr, "/v1.55/containers/create",
		`{"HostConfig":{"Mounts":[`+
			`{"Type":"bind","Source":"C:\\data","Target":"/data"},`+
			`{"Type":"volume","Source":"myvol","Target":"/vol"}]}}`)

	var got struct {
		HostConfig struct {
			Mounts []struct {
				Type, Source, Target string
			}
		}
	}
	if err := json.Unmarshal([]byte(engine.requests()[0].Body), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.HostConfig.Mounts[0].Source != "/mnt/c/data" {
		t.Errorf("bind mount source = %q, want /mnt/c/data", got.HostConfig.Mounts[0].Source)
	}
	// A volume's Source is a name, not a path — translating it would convert
	// the volume into a bind mount.
	if got.HostConfig.Mounts[1].Source != "myvol" {
		t.Errorf("volume source = %q, want myvol (untouched)", got.HostConfig.Mounts[1].Source)
	}
}

func TestRewritePreservesUnknownFields(t *testing.T) {
	// The engine API grows every release; dropping a field we don't model
	// would be a silent behavior change for the user.
	engine := newFakeEngine(t, okJSON(`{}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	postCreate(t, addr, "/v1.55/containers/create",
		`{"HostConfig":{"Binds":["C:\\src:/app"],"SomeFutureOption":{"nested":true},"CpuShares":1024},"NewTopLevel":42}`)

	var got map[string]any
	if err := json.Unmarshal([]byte(engine.requests()[0].Body), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["NewTopLevel"] == nil {
		t.Error("unknown top-level field was dropped")
	}
	hc := got["HostConfig"].(map[string]any)
	if hc["SomeFutureOption"] == nil {
		t.Error("unknown HostConfig field was dropped")
	}
	// Numbers must not gain a float representation (1024 not 1.024e3).
	if !strings.Contains(engine.requests()[0].Body, "1024") {
		t.Errorf("numeric literal mangled: %s", engine.requests()[0].Body)
	}
}

func TestRewriteLeavesOtherRequestsAlone(t *testing.T) {
	engine := newFakeEngine(t, okJSON(`{"Version":"29.7.2"}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	body := `{"Binds":["C:\\src:/app"]}` // same shape, but not a create call
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /v1.55/containers/other HTTP/1.1\r\nHost: d\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(body), body)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.ReadAll(c)

	if got := engine.requests()[0].Body; got != body {
		t.Errorf("non-create body altered:\n got %s\nwant %s", got, body)
	}
}

func TestRewriteHandlesKeepAlive(t *testing.T) {
	// The docker CLI pools connections, so a second create can arrive on a
	// connection the proxy has already handled one request on.
	engine := newFakeEngine(t, func(_ int, w io.Writer, _ *http.Request) {
		fmt.Fprint(w, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
	})
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	br := bufio.NewReader(c)

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"HostConfig":{"Binds":["C:\\src%d:/app"]}}`, i)
		fmt.Fprintf(c, "POST /v1.55/containers/create HTTP/1.1\r\nHost: d\r\nContent-Length: %d\r\n\r\n%s",
			len(body), body)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("request %d: read response: %v", i, err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	reqs := engine.requests()
	if len(reqs) != 3 {
		t.Fatalf("engine saw %d requests, want 3", len(reqs))
	}
	for i, r := range reqs {
		want := fmt.Sprintf("/mnt/c/src%d:/app", i)
		if !strings.Contains(r.Body, want) {
			t.Errorf("request %d body %s, want it to contain %q", i, r.Body, want)
		}
	}
}

func TestRewriteRejectsUNCBind(t *testing.T) {
	// A UNC path cannot be bind-mounted; refusing with a real API error beats
	// forwarding it and letting the daemon fail less legibly.
	engine := newFakeEngine(t, okJSON(`{}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	resp := postCreate(t, addr, "/v1.55/containers/create",
		`{"HostConfig":{"Binds":["\\\\server\\share:/app"]}}`)

	if !strings.Contains(resp, "400") {
		t.Errorf("expected a 400 response, got:\n%s", resp)
	}
	if !strings.Contains(resp, "UNC") {
		t.Errorf("error message should mention UNC, got:\n%s", resp)
	}
	if len(engine.requests()) != 0 {
		t.Errorf("bad request was forwarded to the engine: %+v", engine.requests())
	}
}

func TestRewriteFallsBackToRawOn101(t *testing.T) {
	// docker exec with an upgrade: after 101 the connection is not HTTP, so
	// the proxy must stop parsing and copy bytes.
	engine := newFakeEngine(t, func(_ int, w io.Writer, _ *http.Request) {
		fmt.Fprint(w, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		// Now behave like a shell: echo whatever arrives.
		if c, ok := w.(net.Conn); ok {
			io.Copy(c, c)
		}
	})
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	fmt.Fprint(c, "POST /v1.55/containers/abc/attach?stream=1 HTTP/1.1\r\nHost: d\r\nUpgrade: tcp\r\nConnection: Upgrade\r\nContent-Length: 0\r\n\r\n")
	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))

	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status = %q, want 101", status)
	}
	// Drain headers.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	// The stream is raw from here.
	if _, err := io.WriteString(c, "echo-me"); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "echo-me" {
		t.Errorf("raw stream = %q, want %q", buf, "echo-me")
	}
}

func TestRewriteStreamContentTypeIsNotAHijack(t *testing.T) {
	// The `docker logs` shape: a 200 whose Content-Type is a docker stream
	// type but whose body is ordinary chunked HTTP carrying the multiplexed
	// frames. Treating it as a hijack forwards the chunk framing as payload,
	// which the CLI reads as a demux header and dies with
	// "unrecognized stream: 102" ('f', a chunk-size line). The response must
	// go through HTTP handling so the client's own dechunking works — and it
	// must still arrive incrementally, since this is `logs -f`.
	engine := newFakeEngine(t, func(_ int, w io.Writer, _ *http.Request) {
		fmt.Fprint(w, "HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		for i := 0; i < 3; i++ {
			// A realistic multiplexed frame: stdout, 7-byte payload.
			frame := append([]byte{1, 0, 0, 0, 0, 0, 0, 7}, []byte(fmt.Sprintf("line-%d\n", i))...)
			fmt.Fprintf(w, "%x\r\n%s\r\n", len(frame), frame)
			time.Sleep(20 * time.Millisecond)
		}
		fmt.Fprint(w, "0\r\n\r\n")
	})
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET /v1.55/containers/abc/logs?follow=1&stdout=1 HTTP/1.1\r\nHost: d\r\n\r\n")

	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	// Reading through the HTTP body must yield clean frames: the first byte of
	// the first frame is the stream id (1 = stdout), NOT a chunk-size digit.
	first := make([]byte, 8)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("read first frame header: %v", err)
	}
	if first[0] != 1 {
		t.Fatalf("first body byte = %#x, want 0x1 (stdout frame id); chunk framing leaked through", first[0])
	}
	payload := make([]byte, 7)
	if _, err := io.ReadFull(resp.Body, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != "line-0\n" {
		t.Errorf("payload = %q, want %q", payload, "line-0\n")
	}
}

func TestRewriteStreamsChunkedResponse(t *testing.T) {
	// docker logs -f arrives chunked; it must reach the client incrementally
	// rather than being buffered until the response completes.
	engine := newFakeEngine(t, func(_ int, w io.Writer, _ *http.Request) {
		fmt.Fprint(w, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n")
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "7\r\nline-%d\n\r\n", i)
			time.Sleep(20 * time.Millisecond)
		}
		fmt.Fprint(w, "0\r\n\r\n")
	})
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET /v1.55/containers/abc/logs?follow=1 HTTP/1.1\r\nHost: d\r\n\r\n")

	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 7)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if string(buf) != "line-0\n" {
		t.Errorf("first chunk = %q, want %q", buf, "line-0\n")
	}
}

func TestRewriteHandlesEmptyAndNonJSONBodies(t *testing.T) {
	engine := newFakeEngine(t, okJSON(`{}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	// Malformed JSON must be forwarded unchanged so the engine renders the
	// authoritative error rather than the proxy guessing.
	garbage := `not json at all`
	postCreate(t, addr, "/v1.55/containers/create", garbage)
	if got := engine.requests()[0].Body; got != garbage {
		t.Errorf("body = %q, want it forwarded unchanged", got)
	}
}

func TestRewriteNoHostConfig(t *testing.T) {
	engine := newFakeEngine(t, okJSON(`{}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	body := `{"Image":"alpine"}`
	postCreate(t, addr, "/v1.55/containers/create", body)
	if got := engine.requests()[0].Body; got != body {
		t.Errorf("body = %q, want %q unchanged", got, body)
	}
}

func TestRewriteUnversionedPath(t *testing.T) {
	// DOCKER_API_VERSION can be unset, in which case the CLI omits the prefix.
	engine := newFakeEngine(t, okJSON(`{}`))
	addr, stop := startRewriteProxy(t, engine.dialer())
	defer stop()

	postCreate(t, addr, "/containers/create", `{"HostConfig":{"Binds":["C:\\src:/app"]}}`)
	if got := engine.requests()[0].Body; !strings.Contains(got, "/mnt/c/src:/app") {
		t.Errorf("unversioned path not rewritten: %s", got)
	}
}

func TestClientVanishingDuringQuietStreamEndsRelay(t *testing.T) {
	// The docker-compose /events shape: the engine sends chunked headers and
	// then nothing (no events happening), and the client exits. The relay is
	// blocked reading the engine, so only watching the client can notice —
	// without it, this connection pins the bridge open forever (it held the
	// idle-stop veto in #41, and leaked silently since v0.1).
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- pipeproxy.RewriteBinds(bridgeClient, bridgeEngine) }()

	// Engine: accept the request, answer with a chunked stream that never
	// sends a chunk.
	go func() {
		br := bufio.NewReader(engineSide)
		http.ReadRequest(br)
		engineSide.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n"))
		// ...and then silence: no events are happening.
	}()

	req, _ := http.NewRequest("GET", "http://d/events", nil)
	if err := req.Write(client); err != nil {
		t.Fatal(err)
	}
	// Read the response head so the stream is established client-side.
	br := bufio.NewReader(client)
	if _, err := http.ReadResponse(br, req); err != nil {
		t.Fatal(err)
	}

	// The client goes away without ever cancelling politely (process exit).
	client.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("relay returned %v; a vanished client is ordinary shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay still pinned open 5s after the client vanished; the #41 idle veto leak")
	}
}
