// Package device reads what the keeper decides on: the power source (it
// holds the Mac awake on AC power only) and whether a process holds the Mac
// awake (how it checks its own hold took). The machine's report to fleet-api
// is claude-rig's, which reads the rest.
//
// The power source answers ok with the source, or unknown with the reason,
// never a guess: on macOS from `pmset -g batt`, elsewhere unknown until it is
// measured there. It builds on every OS.
package device

import (
	"fmt"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// why is the reason the power source is unknown, at most 200 bytes as
// fleet-api's schema allows.
func why(what string, err error) *string {
	s := what + ": no answer"
	if err != nil {
		s = fmt.Sprintf("%s: %v", what, err)
	}
	return fleet.String(clip(s))
}

// clip keeps a string inside the schema's 200 bytes.
func clip(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}

// commandTimeout bounds each program run to read the machine.
const commandTimeout = 5 * time.Second
