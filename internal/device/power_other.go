//go:build !darwin

package device

import (
	"errors"
	"runtime"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// Power is unknown off macOS until it is measured there.
func Power() *fleet.DevicePower {
	return &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: fleet.String("not read on " + runtime.GOOS + " yet")}
}

// Assertions is macOS's; there is nothing to read here.
func Assertions() (string, error) {
	return "", errors.New("power assertions are macOS's; not read on " + runtime.GOOS)
}
