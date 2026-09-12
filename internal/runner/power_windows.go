//go:build windows

package runner

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// ReadPower reads the active power scheme's AC sleep timeouts (#268).
//
// Via powercfg rather than the registry: the active scheme's GUID has to be
// resolved first either way, powercfg is present on every Windows edition, and
// reading it needs no elevation. It is invoked with an explicit timeout — a
// health check must not hang because a subprocess did.
//
// Failure is reported as "unknown", never as "fine". A probe that did not run
// must not read as a probe that passed, which is the whole reason this finding
// exists: `runner check` was silent about sleep, and silence was taken for
// approval.
func ReadPower() PowerFacts {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	standby, err := powercfgIndex(ctx, "SUB_SLEEP", "STANDBYIDLE")
	if err != nil {
		return PowerFacts{Reason: err.Error()}
	}
	hibernate, err := powercfgIndex(ctx, "SUB_SLEEP", "HIBERNATEIDLE")
	if err != nil {
		// Hibernate can be absent entirely (it is removable, and some VMs ship
		// without it). Absent means "never hibernates", which is what a runner
		// wants — so this is not an unknown reading.
		hibernate = 0
	}

	return PowerFacts{
		Known:          true,
		StandbyAfter:   time.Duration(standby) * time.Second,
		HibernateAfter: time.Duration(hibernate) * time.Second,
		OnBattery:      onBattery(),
	}
}

// acIndexRe pulls the AC power setting index out of `powercfg /q` output. The
// value is hex seconds, and the DC line follows it — hence the anchor on the
// AC label rather than a bare search for "Power Setting Index".
var acIndexRe = regexp.MustCompile(`(?i)Current AC Power Setting Index:\s*0x([0-9a-f]+)`)

// powercfgIndex returns one setting's AC timeout, in seconds.
func powercfgIndex(ctx context.Context, subgroup, setting string) (int64, error) {
	// SCHEME_CURRENT resolves the active scheme, so no GUID has to be parsed
	// out of a separate call.
	out, err := exec.CommandContext(ctx, "powercfg", "/q", "SCHEME_CURRENT", subgroup, setting).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return 0, errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return 0, err
	}
	m := acIndexRe.FindSubmatch(out)
	if m == nil {
		return 0, errors.New("powercfg did not report an AC setting index for " + setting)
	}
	v, err := strconv.ParseInt(string(m[1]), 16, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// systemPowerStatus mirrors the Win32 SYSTEM_POWER_STATUS struct.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

// onBattery reports whether the machine is running on battery.
//
// GetSystemPowerStatus rather than shelling out again: it is one call, it
// cannot hang, and ACLineStatus answers exactly this question. 0 is offline,
// 1 online, 255 unknown -- and unknown is treated as mains, because a desktop
// with no battery reports it and warning such a machine about its battery
// would be noise.
func onBattery() bool {
	var s systemPowerStatus
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	if r == 0 {
		return false // the call failed; do not invent a battery
	}
	return s.ACLineStatus == 0
}
