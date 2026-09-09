// Package audit records the container-affecting docker API calls that cross the
// bridge (#121): image pulls, container create/start/stop/remove, exec, builds.
// Because Hawser proxies the API at the pipe, it can log what actually happened
// — "what did that compose file pull and mount?" — which Docker Desktop exposes
// nowhere.
//
// It is deliberately privacy-light: an event is derived from the request line
// and query string only, never the request body, so credentials and payloads
// are never written. Output is JSON-lines, one event per call, for `hawser
// audit tail` and any log pipeline.
package audit

import (
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"sync"
	"time"
)

// timeFormat is RFC3339 with milliseconds, sortable and log-friendly.
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// Event is one audited API call. Fields absent for a given action are omitted.
type Event struct {
	Time      string `json:"time"`
	Action    string `json:"action"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Image     string `json:"image,omitempty"`
	Name      string `json:"name,omitempty"`
	Container string `json:"container,omitempty"`
	Status    int    `json:"status,omitempty"`
	Millis    int64  `json:"ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// verPrefix is the optional /v1.44 API-version prefix the CLI sends.
const verPrefix = `^(?:/v[0-9.]+)?`

var (
	reImagesCreate  = regexp.MustCompile(verPrefix + `/images/create$`)
	reImagePush     = regexp.MustCompile(verPrefix + `/images/(.+)/push$`)
	reBuild         = regexp.MustCompile(verPrefix + `/build$`)
	reContCreate    = regexp.MustCompile(verPrefix + `/containers/create$`)
	reContAction    = regexp.MustCompile(verPrefix + `/containers/([^/]+)/(start|stop|kill|restart|pause|unpause)$`)
	reContRemove    = regexp.MustCompile(verPrefix + `/containers/([^/]+)$`)
	reExecCreate    = regexp.MustCompile(verPrefix + `/containers/([^/]+)/exec$`)
	reExecStart     = regexp.MustCompile(verPrefix + `/exec/([^/]+)/start$`)
	reVolumeCreate  = regexp.MustCompile(verPrefix + `/volumes/create$`)
	reNetworkCreate = regexp.MustCompile(verPrefix + `/networks/create$`)
)

// Classify maps a request to an audit action and the fields extractable from the
// path and query alone. A blank action means the call is not container-affecting
// and should be dropped — that filter is what keeps the log meaningful and free
// of `/_ping` and `/containers/json` poll noise.
func Classify(method, path, rawQuery string) (action string, ev Event) {
	q, _ := url.ParseQuery(rawQuery)
	ev = Event{Method: method, Path: path}

	switch method {
	case "POST":
		switch {
		case reImagesCreate.MatchString(path):
			img := q.Get("fromImage")
			if img != "" && q.Get("tag") != "" {
				img += ":" + q.Get("tag")
			}
			ev.Image = img
			return "image-pull", ev
		case reBuild.MatchString(path):
			return "image-build", ev
		case m(reImagePush, path) != "":
			ev.Image = m(reImagePush, path)
			return "image-push", ev
		case reContCreate.MatchString(path):
			// The image is in the body (not read, for privacy); the name is in
			// the query.
			ev.Name = q.Get("name")
			return "container-create", ev
		case reExecCreate.MatchString(path):
			ev.Container = m(reExecCreate, path)
			return "exec-create", ev
		case reExecStart.MatchString(path):
			return "exec-start", ev
		case reContAction.MatchString(path):
			sub := reContAction.FindStringSubmatch(path)
			ev.Container = sub[1]
			return "container-" + sub[2], ev
		case reVolumeCreate.MatchString(path):
			return "volume-create", ev
		case reNetworkCreate.MatchString(path):
			return "network-create", ev
		}
	case "DELETE":
		if c := m(reContRemove, path); c != "" {
			ev.Container = c
			return "container-remove", ev
		}
	}
	return "", ev
}

// m returns the first submatch of re against s, or "".
func m(re *regexp.Regexp, s string) string {
	sub := re.FindStringSubmatch(s)
	if len(sub) > 1 {
		return sub[1]
	}
	return ""
}

// Logger writes audited events as JSON lines to an io.Writer (typically a
// rotating file). It is safe for concurrent use and best-effort: a write error
// is never propagated to the proxied request.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// New returns a Logger writing to w.
func New(w io.Writer) *Logger { return &Logger{w: w, now: time.Now} }

// Observe classifies a completed request and, when it is container-affecting,
// writes one event. start is when the request was read, so the record carries
// how long the call took.
func (l *Logger) Observe(start time.Time, method, path, rawQuery string, status int, err error) {
	action, ev := Classify(method, path, rawQuery)
	if action == "" {
		return
	}
	now := l.now()
	ev.Action = action
	ev.Time = now.UTC().Format(timeFormat)
	ev.Status = status
	if !start.IsZero() {
		ev.Millis = now.Sub(start).Milliseconds()
	}
	if err != nil {
		ev.Error = err.Error()
	}

	b, mErr := json.Marshal(ev)
	if mErr != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.Write(append(b, '\n'))
}
