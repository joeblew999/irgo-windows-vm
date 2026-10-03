//go:build !darwin

package device

import (
	"errors"
	"runtime"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// readPower is unknown off macOS until each is measured there: Linux's
// /sys/class/power_supply and /proc/acpi/button/lid, and systemd-logind's
// settings; Windows' GetSystemPowerStatus and powercfg.
func readPower() (*fleet.DevicePower, *fleet.DeviceBattery, *fleet.DeviceLid, *fleet.DeviceSleep) {
	w := fleet.String("not read on " + runtime.GOOS + " yet")
	return &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: w},
		&fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusUnknown, Why: w},
		&fleet.DeviceLid{Status: fleet.DeviceLidStatusUnknown, Why: w},
		&fleet.DeviceSleep{Status: fleet.DeviceSleepStatusUnknown, Why: w}
}

// Assertions is macOS's; there is nothing to read here.
func Assertions() (string, error) {
	return "", errors.New("power assertions are macOS's; not read on " + runtime.GOOS)
}

func model() string { return "" }

func guest() (bool, bool) { return false, false }

// mountOf is the path itself: off macOS the system and data volumes are
// reported apart, even when they are one.
func mountOf(path string) string { return path }
