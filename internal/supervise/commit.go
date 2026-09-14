package supervise

import (
	"fmt"
	"os"
	"time"
)

// writeRetries and writeBackoff bound how long a commit keeps trying.
//
// Windows will not replace a file another process has open: os.Rename is
// MoveFileEx(REPLACE_EXISTING), and os.ReadFile opens without
// FILE_SHARE_DELETE. Readers of this package's state files are frequent and by
// design -- `skrog status` and `doctor` read all of them, the supervisor's
// reconcile loop reads the desired state every tick, and the VS Code extension
// polls status every few seconds -- so a single attempt loses writes to a race
// that is guaranteed to happen eventually (#288, #314).
//
// A reader holds a file for microseconds. A handful of tries over a fraction of
// a second covers that without being noticeable in any case that matters.
const (
	writeRetries = 10
	writeBackoff = 20 * time.Millisecond
)

// commit writes body to path atomically, retrying the rename past a concurrent
// reader.
//
// Atomic so a reader sees a whole file or the previous one; retried so the
// *writer* is not the one defeated by the reader. The first half was always
// here; the second was missing from every file in this package except
// endpoint.json, which is what #314 found when `skrog stop` failed in CI.
//
// A commit that never lands removes its temp file rather than leaving it in the
// state directory a support bundle collects, where it looks like state.
func commit(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	var err error
	for attempt := 0; ; attempt++ {
		if err = os.Rename(tmp, path); err == nil {
			return nil
		}
		if attempt >= writeRetries-1 {
			break
		}
		time.Sleep(writeBackoff)
	}
	os.Remove(tmp)
	return fmt.Errorf("after %d attempts: %w", writeRetries, err)
}

// remove deletes path, retrying past a concurrent reader the same way.
// An absent file is success: the caller wanted it gone.
func remove(path string) error {
	var err error
	for attempt := 0; attempt < writeRetries; attempt++ {
		err = os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(writeBackoff)
	}
	return err
}
