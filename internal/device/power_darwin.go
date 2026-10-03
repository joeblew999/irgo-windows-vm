//go:build darwin

package device

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
	"golang.org/x/sys/unix"
)

// readPower reads power, battery, lid and sleep from pmset and ioreg, which
// need no root. Each program that fails makes its sections unknown, with
// what it said.
func readPower() (*fleet.DevicePower, *fleet.DeviceBattery, *fleet.DeviceLid, *fleet.DeviceSleep) {
	var power *fleet.DevicePower
	var battery *fleet.DeviceBattery
	if out, err := run("/usr/bin/pmset", "-g", "batt"); err != nil {
		power = &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: why("pmset -g batt", err)}
		battery = &fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusUnknown, Why: why("pmset -g batt", err)}
	} else {
		power, battery = parseBatt(out)
	}

	lid := &fleet.DeviceLid{Status: fleet.DeviceLidStatusNone}
	hasLid := false
	if out, err := run("/usr/sbin/ioreg", "-r", "-k", "AppleClamshellState", "-d", "1"); err != nil {
		lid = &fleet.DeviceLid{Status: fleet.DeviceLidStatusUnknown, Why: why("ioreg AppleClamshellState", err)}
	} else if closed, ok := parseClamshell(out); ok {
		hasLid = true
		lid = &fleet.DeviceLid{Status: fleet.DeviceLidStatusOk, Closed: fleet.Bool(closed)}
	}

	settings, sErr := run("/usr/bin/pmset", "-g")
	assertions, aErr := run("/usr/bin/pmset", "-g", "assertions")
	var sleep *fleet.DeviceSleep
	switch {
	case sErr != nil:
		sleep = &fleet.DeviceSleep{Status: fleet.DeviceSleepStatusUnknown, Why: why("pmset -g", sErr)}
	case aErr != nil:
		sleep = &fleet.DeviceSleep{Status: fleet.DeviceSleepStatusUnknown, Why: why("pmset -g assertions", aErr)}
	default:
		sleep = sleepSection(parseSettings(settings), assertions, hasLid)
	}
	return power, battery, lid, sleep
}

// Assertions is what `pmset -g assertions` prints now.
func Assertions() (string, error) { return run("/usr/bin/pmset", "-g", "assertions") }

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("no answer in %s", commandTimeout)
	}
	return string(out), err
}

// model is the hardware model, Mac14,10. Never a serial number.
func model() string {
	m, err := unix.Sysctl("hw.model")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(m)
}

// guest is whether this macOS runs in a virtual machine.
func guest() (bool, bool) {
	v, err := unix.SysctlUint32("kern.hv_vmm_present")
	if err != nil {
		return false, false
	}
	return v == 1, true
}

// mountOf is where the volume holding path is mounted. On macOS the system
// volume is mounted at / and the home directories' at /System/Volumes/Data.
func mountOf(path string) string {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return path
	}
	return unix.ByteSliceToString(st.Mntonname[:])
}
