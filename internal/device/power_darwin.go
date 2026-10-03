//go:build darwin

package device

import (
	"context"
	"fmt"
	"os/exec"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// Power is the power source, from `pmset -g batt`, which needs no root.
func Power() *fleet.DevicePower {
	out, err := run("/usr/bin/pmset", "-g", "batt")
	if err != nil {
		return &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: why("pmset -g batt", err)}
	}
	return parsePower(out)
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
