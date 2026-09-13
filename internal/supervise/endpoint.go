package supervise

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Endpoint is what the running supervisor actually bound (#273).
//
// It is *recorded* rather than recomputed, and that is the whole point. Which
// pipe Skrog serves depends on whether anything else held the default one at
// the moment the supervisor started -- see pipeproxy.SelectPipeName -- and
// Docker Desktop can start or stop afterwards. A `skrog status` that asked the
// selector again would confidently name a pipe nothing is listening on, which
// is the worst possible answer to "where do I point DOCKER_HOST".
type Endpoint struct {
	// Pipe is the named pipe being served, in `\\.\pipe\name` form.
	Pipe string `json:"pipe"`
	// Reason is SelectPipeName's account of the choice: that the default pipe
	// was free, or that something else already had it. This is what answers
	// "why am I on skrog_engine", the question behind most of the DOCKER_HOST
	// confusion (#272).
	Reason string `json:"reason,omitempty"`
}

func endpointPath(stateDir string) string {
	return filepath.Join(stateDir, "endpoint.json")
}

// writeRetries and writeBackoff bound how long WriteEndpoint keeps trying.
//
// Windows will not replace a file another process has open: os.Rename is
// MoveFileEx(REPLACE_EXISTING), and os.ReadFile opens without
// FILE_SHARE_DELETE. A reader is not rare -- the VS Code extension polls
// `skrog status --json` every few seconds, and status and doctor both read this
// record -- so a single attempt could lose the write for the whole life of the
// supervisor, leaving status with nothing to report and doctor back on the name
// comparison #283 removed (#288). A reader holds the file for microseconds, so
// a handful of tries over a fraction of a second covers it without delaying
// startup in any case that matters.
const (
	writeRetries = 10
	writeBackoff = 20 * time.Millisecond
)

// WriteEndpoint records the binding atomically, so a reader sees a whole record
// or none, and retries when a concurrent reader blocks the commit.
//
// Failure is still the caller's to log and carry on with: a status field is not
// worth taking the bridge down for. But it is worth trying more than once.
func WriteEndpoint(stateDir string, e Endpoint) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := endpointPath(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing endpoint record: %w", err)
	}
	for attempt := 0; ; attempt++ {
		err = os.Rename(tmp, endpointPath(stateDir))
		if err == nil {
			return nil
		}
		if attempt >= writeRetries-1 {
			break
		}
		time.Sleep(writeBackoff)
	}
	// The temp file would otherwise sit in the state dir a support bundle
	// collects, looking like a record.
	os.Remove(tmp)
	return fmt.Errorf("committing endpoint record after %d attempts: %w", writeRetries, err)
}

// ReadEndpoint returns the recorded binding. ok is false when there is none, or
// when the file cannot be parsed -- both mean "nothing to report", and neither
// is a fault worth an error return in a status path.
//
// The record can outlive the process that wrote it: a supervisor killed hard
// runs no cleanup. So a caller asking "what is being served right now" must
// gate on Held. The lock is the liveness signal; this file only says what the
// holder chose.
func ReadEndpoint(stateDir string) (Endpoint, bool) {
	b, err := os.ReadFile(endpointPath(stateDir))
	if err != nil {
		return Endpoint{}, false
	}
	var e Endpoint
	if err := json.Unmarshal(b, &e); err != nil || e.Pipe == "" {
		return Endpoint{}, false
	}
	return e, true
}

// ClearEndpoint removes the record, so a supervisor that exits cleanly leaves
// nothing behind to be misread. Belt to Held's braces; absence of the file is
// not the guarantee, only tidiness.
func ClearEndpoint(stateDir string) error {
	var err error
	for attempt := 0; attempt < writeRetries; attempt++ {
		err = os.Remove(endpointPath(stateDir))
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		// Same Windows sharing rule as the commit in WriteEndpoint: a reader
		// with the file open blocks the delete (#288).
		time.Sleep(writeBackoff)
	}
	return err
}
