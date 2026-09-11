package enginestats_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/enginestats"
)

// fakeDistro answers the four shell commands the reader runs, and records them
// so a test can prove which ones happened.
type fakeDistro struct {
	info    string
	df      string
	dfOut   string
	meminfo string
	calls   []string
	fail    map[string]error
}

func (f *fakeDistro) Exec(_ context.Context, _, _ string, args ...string) (string, error) {
	cmd := args[len(args)-1]
	f.calls = append(f.calls, cmd)
	pick := func(key, body string) (string, error) {
		if err := f.fail[key]; err != nil {
			return "", err
		}
		return body, nil
	}
	switch {
	case strings.Contains(cmd, "/system/df"):
		return pick("df", httpOK(f.df))
	case strings.Contains(cmd, "/info"):
		return pick("info", httpOK(f.info))
	case strings.Contains(cmd, "df -B1"):
		return pick("guestdf", f.dfOut)
	case strings.Contains(cmd, "meminfo"):
		return pick("meminfo", f.meminfo)
	}
	return "", fmt.Errorf("unexpected command %q", cmd)
}

func httpOK(body string) string {
	return "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\n\r\n" + body
}

type fakeDisk struct {
	size, free uint64
	err        error
}

func (d fakeDisk) SizeOnDisk(string) (uint64, error) {
	return d.size, d.err
}
func (d fakeDisk) Free(string) (uint64, error) { return d.free, nil }

const infoJSON = `{"ServerVersion":"29.7.2","Containers":5,"ContainersRunning":2,
"ContainersPaused":1,"ContainersStopped":2,"Images":9,"NCPU":20,"MemTotal":33481715712}`

// dfJSON has one image in use and one not, a referenced and an unreferenced
// volume, and build cache both in use and not — so the reclaimable arithmetic
// is actually exercised rather than summed.
const dfJSON = `{
  "LayersSize": 1000,
  "Images": [{"Size": 600, "Containers": 1}, {"Size": 400, "Containers": 0}],
  "Volumes": [{"UsageData":{"Size": 50, "RefCount": 1}}, {"UsageData":{"Size": 70, "RefCount": 0}}],
  "BuildCache": [{"Size": 30, "InUse": true}, {"Size": 20, "InUse": false},
                 {"Size": 999, "InUse": false, "Shared": true}]
}`

func reader(d *fakeDistro, disk fakeDisk) *enginestats.Reader {
	return &enginestats.Reader{
		WSL:        d,
		Disk:       disk,
		Configured: map[string]string{"memory": "8GB", "processors": "4"},
	}
}

func TestReadGathersEverything(t *testing.T) {
	d := &fakeDistro{
		info:    infoJSON,
		df:      dfJSON,
		dfOut:   "/dev/sdc 1000000000 300000000 700000000 30% /",
		meminfo: "MemAvailable:   1048576 kB\nSwapTotal:       2097152 kB\n",
	}
	s := reader(d, fakeDisk{size: 500000000, free: 90000000000}).
		Read(context.Background(), "hawser-engine", `C:\d\ext4.vhdx`, true)

	if !s.Probed {
		t.Fatal("Probed is false with a running engine")
	}
	if s.Engine.Version != "29.7.2" || s.Engine.Containers != 5 ||
		s.Engine.Running != 2 || s.Engine.Paused != 1 || s.Engine.Stopped != 2 || s.Engine.Images != 9 {
		t.Errorf("engine = %+v", s.Engine)
	}
	if s.Engine.ImagesBytes != 1000 {
		t.Errorf("ImagesBytes = %d, want LayersSize 1000", s.Engine.ImagesBytes)
	}
	if s.Engine.Volumes != 2 || s.Engine.VolumesBytes != 120 {
		t.Errorf("volumes = %d/%d, want 2/120", s.Engine.Volumes, s.Engine.VolumesBytes)
	}
	// Shared build cache is excluded: it is not this engine's to reclaim.
	if s.Engine.BuildCacheBytes != 50 {
		t.Errorf("BuildCacheBytes = %d, want 50 (30 in use + 20 free, shared excluded)", s.Engine.BuildCacheBytes)
	}
	// 400 (unused image) + 70 (unreferenced volume) + 20 (idle cache).
	if s.Engine.ReclaimableBytes != 490 {
		t.Errorf("ReclaimableBytes = %d, want 490", s.Engine.ReclaimableBytes)
	}
	if s.Disk.SizeOnDiskBytes != 500000000 || s.Disk.GuestUsedBytes != 300000000 {
		t.Errorf("disk = %+v", s.Disk)
	}
	if s.Disk.ReclaimableBytes != 200000000 {
		t.Errorf("disk reclaimable = %d, want size-on-disk minus guest-used", s.Disk.ReclaimableBytes)
	}
	if s.VM.CPUs != 20 || s.VM.MemTotalBytes != 33481715712 {
		t.Errorf("vm = %+v", s.VM)
	}
	if s.VM.MemAvailableBytes != 1048576*1024 || s.VM.SwapTotalBytes != 2097152*1024 {
		t.Errorf("vm memory = %+v (meminfo is in kB)", s.VM)
	}
	if s.VM.ConfiguredMemory != "8GB" || s.VM.ConfiguredProcessors != "4" {
		t.Errorf("configured sizing not carried through: %+v", s.VM)
	}
	if len(s.Errors) != 0 {
		t.Errorf("errors on a healthy read: %v", s.Errors)
	}
}

func TestReadStartsNothingWhenTheEngineIsDown(t *testing.T) {
	// The property that makes `status --stats` safe: statistics never boot a
	// stopped distro (#82), so a down engine costs zero WSL calls.
	d := &fakeDistro{info: infoJSON, df: dfJSON}
	s := reader(d, fakeDisk{size: 1}).
		Read(context.Background(), "hawser-engine", `C:\d\ext4.vhdx`, false)

	if s.Probed {
		t.Error("Probed true for a stopped engine")
	}
	if len(d.calls) != 0 {
		t.Errorf("a stopped engine was queried anyway: %v", d.calls)
	}
	if s.Engine.Containers != 0 || s.Disk.SizeOnDiskBytes != 0 {
		t.Errorf("stats reported for a stopped engine: %+v", s)
	}
}

func TestPartialFailuresAreReportedNotFatal(t *testing.T) {
	// Losing /system/df (the slow call on a large engine) must not cost the
	// container counts, and the failure has to be visible rather than a zero.
	d := &fakeDistro{
		info:    infoJSON,
		df:      dfJSON,
		dfOut:   "/dev/sdc 1000 500 500 50% /",
		meminfo: "MemAvailable: 1024 kB\n",
		fail:    map[string]error{"df": fmt.Errorf("timed out")},
	}
	s := reader(d, fakeDisk{size: 100}).
		Read(context.Background(), "hawser-engine", `C:\d\ext4.vhdx`, true)

	if s.Engine.Containers != 5 {
		t.Errorf("counts lost with df failing: %+v", s.Engine)
	}
	if s.Engine.ImagesBytes != 0 {
		t.Errorf("ImagesBytes = %d, want 0 when df failed", s.Engine.ImagesBytes)
	}
	if len(s.Errors) != 1 || !strings.Contains(s.Errors[0], "disk usage") {
		t.Errorf("errors = %v, want the df failure named", s.Errors)
	}
}

func TestACancelledRequestIsNotAnEmptyEngine(t *testing.T) {
	// dockerd answers a cancelled request with a literal `null` body, which
	// unmarshals into zeroes and would read as "no containers, no images".
	// That is the bug socat's shut-none fixes, and it must surface as an error
	// if it ever comes back.
	d := &fakeDistro{info: "null", df: dfJSON, dfOut: "/dev/sdc 1 1 0 100% /", meminfo: ""}
	s := reader(d, fakeDisk{size: 1}).
		Read(context.Background(), "hawser-engine", `C:\d\ext4.vhdx`, true)

	if len(s.Errors) == 0 {
		t.Fatal("a null /info body was accepted as an empty engine")
	}
	if !strings.Contains(strings.Join(s.Errors, " "), "engine info") {
		t.Errorf("errors = %v", s.Errors)
	}
}

func TestNonSuccessStatusIsAnError(t *testing.T) {
	// The 499 dockerd returns for a cancelled /system/df has a JSON-ish body;
	// treating the status line as authoritative is what stops it reading as
	// zeroes.
	r := &enginestats.Reader{WSL: &statusDistro{code: 499, info: infoJSON}, Disk: fakeDisk{size: 1}}
	s := r.Read(context.Background(), "hawser-engine", `C:\d\ext4.vhdx`, true)
	joined := strings.Join(s.Errors, " ")
	if !strings.Contains(joined, "499") {
		t.Errorf("errors = %v, want the 499 surfaced", s.Errors)
	}
}

// statusDistro answers /info normally and everything else with an HTTP status
// the reader must refuse.
type statusDistro struct {
	code int
	info string
}

func (s *statusDistro) Exec(_ context.Context, _, _ string, args ...string) (string, error) {
	cmd := args[len(args)-1]
	switch {
	case strings.Contains(cmd, "/info"):
		return httpOK(s.info), nil
	case strings.Contains(cmd, "/system/df"):
		return fmt.Sprintf("HTTP/1.0 %d status code %d\r\nContent-Length: 0\r\n\r\n", s.code, s.code), nil
	}
	return "", fmt.Errorf("no answer for %q", cmd)
}
