package pipeproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/wslkit/skrog/internal/winpath"
)

// AuditSink observes each proxied request for the audit log (#121). It is a
// structural interface so pipeproxy stays decoupled from the audit package; a
// nil sink disables auditing entirely (the default).
type AuditSink interface {
	Observe(start time.Time, method, path, rawQuery string, status int, err error)
}

// Gate judges a container-create request before it reaches the engine (#120).
// Structural, like AuditSink, so pipeproxy stays decoupled from the policy
// package; a nil gate disables admission control entirely (the default).
//
// It sees the body as the client sent it, before bind paths are translated,
// because a rule about where mounts may come from is written in the Windows
// terms the user typed.
//
// The map is the LIVE request body, passed without copying so that judging a
// request costs nothing on the hot path. Two consequences for an
// implementation: do not mutate it — this interface decides, it does not
// edit — and do not retain it past the call, because bind-path translation
// rewrites the same map immediately afterwards, so a reference kept for a log
// would later read as translated.
type Gate interface {
	DenyCreate(body map[string]any) (reason string, denied bool)
}

// RewriteBinds is a Server.Handler that translates Windows bind paths on their
// way to the engine, then gets out of the way.
//
// Spike A (#2) established why this is necessary and where it must happen: the
// daemon rejects "C:\src:/app" itself, and the CLI forwards the spec untouched,
// so the proxy is the only place left. It must also be surgical — every other
// byte has to pass through unaltered, because Docker hijacks connections for
// exec, attach, and logs, and a hijacked stream is not HTTP at all.
//
// The handler therefore proxies HTTP only until the engine signals a hijack,
// then reverts to a raw byte relay for the life of the connection.
func RewriteBinds(client net.Conn, engine io.ReadWriteCloser) error {
	return rewriteBinds(client, engine, nil, nil, nil)
}

// RewriteBindsAudited is RewriteBinds with an audit sink wired in, for use as a
// Server.Handler when the audit log is enabled.
func RewriteBindsAudited(sink AuditSink) func(net.Conn, io.ReadWriteCloser) error {
	return func(c net.Conn, e io.ReadWriteCloser) error { return rewriteBinds(c, e, sink, nil, nil) }
}

// RewriteBindsGuarded is RewriteBinds with an audit sink and an admission gate
// (#120). Either may be nil.
func RewriteBindsGuarded(sink AuditSink, gate Gate) func(net.Conn, io.ReadWriteCloser) error {
	return func(c net.Conn, e io.ReadWriteCloser) error { return rewriteBinds(c, e, sink, gate, nil) }
}

// SourceTranslator maps one bind source to the path the engine should see.
//
// It exists because the right mapping is a property of the BACKEND, not of
// Docker. For an engine distro a Windows drive path becomes /mnt/<drive>,
// because the distro auto-mounts drives. A wslc session has no /mnt/c at all --
// each Windows folder is its own virtiofs share at /mnt/{GUID} (#321) -- so the
// same translation would hand dockerd a path that does not exist.
//
// What both backends share is the named-pipe case (#164): a pipe bind-mounted
// into a Linux container can only mean "this engine socket", and that is what
// Testcontainers Ryuk and docker-in-docker rely on.
type SourceTranslator func(source string) (string, error)

// RewriteBindsFor is RewriteBindsGuarded with the backend's own source
// translation. A nil translator keeps the engine-distro behaviour.
func RewriteBindsFor(t SourceTranslator, sink AuditSink, gate Gate) func(net.Conn, io.ReadWriteCloser) error {
	return func(c net.Conn, e io.ReadWriteCloser) error { return rewriteBinds(c, e, sink, gate, t) }
}

func rewriteBinds(client net.Conn, engine io.ReadWriteCloser, audit AuditSink, gate Gate, translate SourceTranslator) error {
	clientR := bufio.NewReader(client)
	engineR := bufio.NewReader(engine)

	responsesRelayed := 0
	for {
		req, err := http.ReadRequest(clientR)
		if err != nil {
			// EOF is the ordinary end of a keep-alive connection.
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read request: %w", err)
		}
		reqStart := time.Now()

		if isContainerCreate(req) {
			denied, err := rewriteCreateBody(req, gate, translate)
			switch {
			case denied != nil:
				// Admission control refused it (#120). 403 rather than 400:
				// the request is well-formed, this machine will not run it.
				// The reason reaches the user verbatim through the CLI.
				observe(audit, reqStart, req, http.StatusForbidden, denied)
				trace("DENY %s %s: %v", req.Method, req.URL.Path, denied)
				return writeError(client, http.StatusForbidden, denied)
			case err != nil:
				// Refusing is better than forwarding a mount the user did not
				// ask for; report it as the API would.
				observe(audit, reqStart, req, http.StatusBadRequest, err)
				return writeError(client, http.StatusBadRequest, err)
			}
		}

		// The body is forwarded eagerly regardless, so Expect: 100-continue is
		// pure interim-response noise downstream — strip it rather than teach
		// every layer about it (#90). Unsolicited 1xx are still handled below.
		req.Header.Del("Expect")

		trace("REQ %s %s", req.Method, req.URL.Path)
		// The body forwards concurrently with reading the response (#90): a
		// daemon that rejects a large upload early (Go servers drain at most
		// 256KB of an abandoned body) stops reading while we still stream —
		// sequential code blocked in Write and the client saw a dropped
		// connection instead of the daemon's 4xx. Reading in parallel salvages
		// that response; writing a request while reading its response on one
		// connection is ordinary HTTP.
		bodySent := make(chan error, 1)
		go func() { bodySent <- req.Write(engine) }()

		resp, err := http.ReadResponse(engineR, req)
		if err != nil {
			if werr := <-bodySent; werr != nil {
				return fmt.Errorf("forward request: %w", werr)
			}
			// EOF on the very first response means the engine never answered —
			// down, or a dead socket — which must surface, not be filtered as
			// an ordinary hang-up (#91).
			if responsesRelayed == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				return fmt.Errorf("engine gave no response to %s: %w", req.URL.Path, ErrEngineUnreachable)
			}
			return fmt.Errorf("read response: %w", err)
		}

		// Interim 1xx responses (#90): each is a complete zero-body response;
		// treating one as THE response desynchronized the loop — the real
		// response was later parsed as if it answered the next request.
		// Forward each head and keep reading; 101 is final by definition (the
		// hijack upgrade).
		for resp.StatusCode >= 100 && resp.StatusCode < 200 && resp.StatusCode != http.StatusSwitchingProtocols {
			trace("RSP %d (interim) for %s", resp.StatusCode, req.URL.Path)
			if werr := writeResponseHead(client, resp); werr != nil {
				engine.Close() // unblock the body writer before leaving
				<-bodySent
				return werr
			}
			if resp, err = http.ReadResponse(engineR, req); err != nil {
				engine.Close()
				<-bodySent
				return fmt.Errorf("read response after interim: %w", err)
			}
		}

		// A final response in hand. Normally the body has long since been
		// forwarded; when the daemon answered early and stopped reading, the
		// writer may be blocked mid-body forever — the response itself is the
		// diagnosis, so a short grace and then the connection is written off:
		// unsent body bytes make its framing unusable for keep-alive anyway.
		bodyDone := false
		select {
		case werr := <-bodySent:
			bodyDone = true
			if werr != nil {
				trace("REQ body aborted by early response (%d): %v", resp.StatusCode, werr)
				resp.Close = true // unsendable remainder: never reuse this connection
			}
		case <-time.After(250 * time.Millisecond):
			trace("REQ body still streaming after early response (%d); abandoning the connection", resp.StatusCode)
			resp.Close = true
			// The engine side is torn down AFTER the response is relayed to
			// the client below; deferring the close here keeps the salvaged
			// body readable. Mark it so.
			defer func() {
				engine.Close()
				<-bodySent
			}()
		}

		// A real response is in hand: the engine is answering, so later EOFs on
		// this connection are ordinary hang-ups, not "engine down" (#91).
		responsesRelayed++
		observe(audit, reqStart, req, resp.StatusCode, nil)

		trace("RSP %d ct=%s cl=%d chunked=%v hijack=%v for %s",
			resp.StatusCode, resp.Header.Get("Content-Type"), resp.ContentLength,
			len(resp.TransferEncoding) > 0, isHijack(resp), req.URL.Path)

		if isHijack(resp) {
			// From here the connection carries a raw multiplexed stream, so
			// hand back the headers verbatim and stop parsing entirely.
			if err := writeResponseHead(client, resp); err != nil {
				resp.Body.Close()
				return err
			}
			return relayBuffered(client, engine, clientR, engineR)
		}

		// resp.Write streams the body as it arrives, which is what keeps
		// `docker logs -f` and pull progress incremental. But a streaming
		// response also needs a watchdog on the client: with a quiet source
		// (docker compose's /events subscription after the stack is down),
		// resp.Write blocks reading the engine and never touches the dead
		// client, so a vanished CLI would pin this relay open forever — found
		// as "idle stop deferred: open client connections" (#41), the same
		// half-open class as #35 on a new surface. The Peek doubles as the
		// wait for the client's next request, so exactly one goroutine reads
		// clientR at any moment.
		// The watchdog must own clientR alone. Arm it only once the body
		// writer has finished with it: on the abandoned-upload path above that
		// goroutine is still reading clientR, and a second reader corrupts the
		// bufio.Reader's internal state — heap corruption that surfaces later,
		// anywhere, including inside winio's IOCP processor (#166). That path
		// already set resp.Close, so the connection ends after this response
		// and there is nothing left for a watchdog to guard.
		var peekc chan error // nil: blocks forever in the select below
		if bodyDone {
			peekc = make(chan error, 1)
			go func() {
				_, err := clientR.Peek(1)
				peekc <- err
			}()
		}
		writec := make(chan error, 1)
		go func() { writec <- resp.Write(client) }()

		var werr error
		peeked := false
		select {
		case werr = <-writec:
		case perr := <-peekc:
			peeked = true
			if perr != nil {
				// Client is gone mid-stream. Closing the engine side unblocks
				// resp.Write; an ordinary disconnect, not a fault.
				engine.Close()
				<-writec
				resp.Body.Close()
				trace("DONE %s (client vanished mid-stream)", req.URL.Path)
				return nil
			}
			// Bytes arrived while the response still streams (a pipelined
			// request): unusual for a docker client, but just wait the write
			// out and loop. Note (#91) the watchdog does not re-arm for the
			// rest of this response — re-Peeking would need a second reader on
			// clientR and break the single-reader invariant. Accepted: the
			// docker CLI never pipelines, so this path is not reached in
			// practice, and a full client disconnect still surfaces as a write
			// error on the next response.
			werr = <-writec
		}
		if werr != nil {
			resp.Body.Close()
			return fmt.Errorf("forward response: %w", werr)
		}
		resp.Body.Close()
		trace("DONE %s (close=%v)", req.URL.Path, resp.Close || req.Close)

		if resp.Close || req.Close {
			// The pending Peek goroutine unblocks when the caller closes the
			// client conn; its channel is buffered, so nothing leaks.
			return nil
		}
		if bodyDone && !peeked {
			// Wait for the client's next move — its next request's first byte,
			// or a close ending the keep-alive conversation — before looping,
			// so ReadRequest never shares clientR with a still-blocked Peek.
			if perr := <-peekc; perr != nil {
				if errors.Is(perr, io.EOF) || errors.Is(perr, net.ErrClosed) {
					return nil
				}
				return fmt.Errorf("await next request: %w", perr)
			}
		}
	}
}

// observe hands one completed request to the audit sink, if any. The sink
// decides what is worth recording; a nil sink is a no-op.
func observe(sink AuditSink, start time.Time, req *http.Request, status int, err error) {
	if sink == nil {
		return
	}
	sink.Observe(start, req.Method, req.URL.Path, req.URL.RawQuery, status, err)
}

// trace is debug logging for the relay and HTTP loop, enabled with
// SKROG_TRACE=1. It earned its keep diagnosing the docker-run-never-exits
// hang (a winio pipe cannot half-close), so it stays.
func trace(format string, args ...any) {
	if os.Getenv("SKROG_TRACE") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "TRACE "+format+"\n", args...)
}

// containerCreatePath matches /containers/create with or without the version
// prefix the CLI normally sends (/v1.55/containers/create).
var containerCreatePath = regexp.MustCompile(`^(/v[0-9.]+)?/containers/create$`)

func isContainerCreate(req *http.Request) bool {
	return req.Method == http.MethodPost && containerCreatePath.MatchString(req.URL.Path)
}

// isHijack reports whether the connection stops being HTTP after this response.
//
// Only 101 Switching Protocols qualifies. An earlier version also treated the
// docker stream content-types (application/vnd.docker.raw-stream,
// .multiplexed-stream) as hijacks — and that corrupted `docker logs`: those
// responses are ordinary HTTP whose *chunked body* carries the multiplexed
// frames, so relaying them raw forwards the chunk framing as if it were
// payload. The CLI then reads a chunk-size line as a demux header and dies
// with "unrecognized stream: 102" — 102 being ASCII 'f', the chunk-size line
// of a 15-byte log frame. Found by the e2e suite's logs -f stage.
//
// A 200-with-stream-content-type response (attach without an Upgrade header)
// is forwarded as normal HTTP instead: the engine→client stream works, and
// the client→engine direction of a non-upgraded attach is knowingly
// unsupported — the docker CLI always upgrades for interactive use.
func isHijack(resp *http.Response) bool {
	return resp.StatusCode == http.StatusSwitchingProtocols
}

// rewriteCreateBody translates bind paths in a container-create request.
//
// The body is decoded into a generic map rather than a typed struct so that
// fields this code does not know about survive untouched — the engine API grows
// every release, and silently dropping a caller's option would be far worse
// than not translating a path. json.Number likewise preserves numeric literals
// exactly instead of round-tripping them through float64.
func rewriteCreateBody(req *http.Request, gate Gate, translate SourceTranslator) (denied, err error) {
	if req.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read create body: %w", err)
	}
	if len(raw) == 0 {
		req.Body = io.NopCloser(bytes.NewReader(raw))
		return nil, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		// Not JSON we understand: pass it through and let the engine judge it.
		req.Body = io.NopCloser(bytes.NewReader(raw))
		return nil, nil
	}

	// Admission control runs BEFORE translation (#120), so a rule about where
	// bind mounts may come from sees the Windows path the user typed rather
	// than the /mnt/c form the engine will get.
	if gate != nil {
		if reason, no := gate.DenyCreate(body); no {
			return errors.New(reason), nil
		}
	}

	changed, err := translateHostConfig(body, translate)
	if err != nil {
		return nil, err
	}
	if !changed {
		req.Body = io.NopCloser(bytes.NewReader(raw))
		return nil, nil
	}

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("re-encode create body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(out))
	req.ContentLength = int64(len(out))
	// The rewrite changes the length, so a stale Content-Length header would
	// desynchronize the stream. Drop any chunked framing for the same reason.
	req.Header.Del("Content-Length")
	req.TransferEncoding = nil
	return nil, nil
}

// translateHostConfig rewrites HostConfig.Binds and the source of any bind-type
// entry in HostConfig.Mounts, reporting whether anything changed.
func translateHostConfig(body map[string]any, translate SourceTranslator) (bool, error) {
	if translate == nil {
		translate = winpath.ToWSL
	}
	hc, ok := body["HostConfig"].(map[string]any)
	if !ok {
		return false, nil
	}

	var changed bool

	if rawBinds, ok := hc["Binds"].([]any); ok {
		binds := make([]string, 0, len(rawBinds))
		for _, b := range rawBinds {
			s, ok := b.(string)
			if !ok {
				return false, fmt.Errorf("HostConfig.Binds contains a non-string entry")
			}
			binds = append(binds, s)
		}
		translated, err := translateBindList(binds, translate)
		if err != nil {
			return false, err
		}
		for i := range translated {
			if translated[i] != binds[i] {
				changed = true
			}
		}
		if changed {
			next := make([]any, len(translated))
			for i, s := range translated {
				next[i] = s
			}
			hc["Binds"] = next
		}
	}

	// --mount syntax, and what compose emits for long-form volumes.
	if mounts, ok := hc["Mounts"].([]any); ok {
		for _, m := range mounts {
			mount, ok := m.(map[string]any)
			if !ok {
				continue
			}
			// Only bind mounts name a host path; a volume's Source is a name.
			// An npipe mount (`--mount type=npipe,src=\\.\pipe\docker_engine`)
			// is the Windows spelling of "bind me the engine socket" (#164).
			t, _ := mount["Type"].(string)
			if t != "bind" && t != "npipe" {
				continue
			}
			src, ok := mount["Source"].(string)
			if !ok || src == "" {
				continue
			}
			translated, err := translate(src)
			if err != nil {
				return false, err
			}
			if t == "npipe" {
				mount["Type"] = "bind"
				changed = true
			}
			if translated != src {
				mount["Source"] = translated
				changed = true
			}
		}
	}

	return changed, nil
}

// writeResponseHead emits a status line and headers without touching the body,
// used when the connection is about to become a raw stream.
func writeResponseHead(w io.Writer, resp *http.Response) error {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	if err := resp.Header.Write(&b); err != nil {
		return err
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write hijack response head: %w", err)
	}
	return nil
}

// writeError reports a proxy-side refusal in the shape the Docker API uses, so
// the CLI prints a real message instead of "error during connect".
func writeError(w io.Writer, status int, cause error) error {
	payload, err := json.Marshal(map[string]string{"message": cause.Error()})
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	b.WriteString("Content-Type: application/json\r\n")
	fmt.Fprintf(&b, "Content-Length: %d\r\n", len(payload))
	b.WriteString("Connection: close\r\n\r\n")
	b.Write(payload)
	if _, werr := io.WriteString(w, b.String()); werr != nil {
		return werr
	}
	return nil
}

// relayBuffered switches to a raw byte relay, first flushing whatever the HTTP
// readers already pulled off the wire. Skipping that would silently swallow the
// first bytes of a hijacked stream — the kind of bug that looks like a hung
// `docker exec` rather than a lost buffer.
func relayBuffered(client net.Conn, engine io.ReadWriteCloser, clientR, engineR *bufio.Reader) error {
	if n := engineR.Buffered(); n > 0 {
		buf, err := engineR.Peek(n)
		if err != nil {
			return err
		}
		if _, err := client.Write(buf); err != nil {
			return err
		}
		engineR.Discard(n)
	}
	if n := clientR.Buffered(); n > 0 {
		buf, err := clientR.Peek(n)
		if err != nil {
			return err
		}
		if _, err := engine.Write(buf); err != nil {
			return err
		}
		clientR.Discard(n)
	}
	return Relay(client, engine)
}

// translateBindList applies the backend's source translation to each entry of
// HostConfig.Binds, reusing winpath's spec parsing so the delicate parts --
// a Windows drive designator being part of the source rather than a separator,
// and a named volume never becoming a bind -- stay in one place.
func translateBindList(binds []string, translate SourceTranslator) ([]string, error) {
	out := make([]string, len(binds))
	for i, b := range binds {
		t, err := winpath.TranslateBindWith(b, translate)
		if err != nil {
			return nil, fmt.Errorf("bind %q: %w", b, err)
		}
		out[i] = t
	}
	return out, nil
}
