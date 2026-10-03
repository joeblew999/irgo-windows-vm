//go:build !darwin

package keeper

import (
	"errors"
	"runtime"
)

// Caffeinate is macOS's; off macOS there is no hold yet (systemd-inhibit and
// SetThreadExecutionState are not built), and Hold says so.
type Caffeinate struct{}

func (*Caffeinate) Hold() error {
	return errors.New("keeping the machine awake is not built for " + runtime.GOOS + " yet")
}
func (*Caffeinate) Release() error { return nil }
func (*Caffeinate) Held() bool     { return false }
func (*Caffeinate) Holder() string { return "nothing" }
