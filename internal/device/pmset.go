package device

// Reading macOS's power state from what pmset prints. The parsers are here,
// without a build constraint, so they are tested on every OS against output
// recorded on a Mac (testdata/).

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// parsePower reads the power source from `pmset -g batt`:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=37945443)	100%; charged; 0:00 remaining present: true
func parsePower(out string) *fleet.DevicePower {
	m := drawing.FindStringSubmatch(out)
	if m == nil {
		return &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: fleet.String("pmset -g batt names no power source")}
	}
	var src fleet.DevicePowerSource
	switch m[1] {
	case "AC Power":
		src = fleet.DevicePowerSourceAc
	case "Battery Power":
		src = fleet.DevicePowerSourceBattery
	case "UPS Power":
		src = fleet.DevicePowerSourceUps
	default:
		return &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: fleet.String(clip("pmset -g batt names a power source this tool does not know: " + m[1]))}
	}
	return &fleet.DevicePower{Status: fleet.DevicePowerStatusOk, Source: src.Ptr()}
}

var drawing = regexp.MustCompile(`Now drawing from '([^']+)'`)

// assertion is one row of `pmset -g assertions`' "Listed by owning process".
type assertion struct {
	PID  int
	Type string
}

// parseAssertions reads every process assertion in `pmset -g assertions`.
func parseAssertions(out string) (rows []assertion) {
	for _, l := range strings.Split(out, "\n") {
		if m := owned.FindStringSubmatch(l); m != nil {
			pid, _ := strconv.Atoi(m[1])
			rows = append(rows, assertion{PID: pid, Type: m[2]})
		}
	}
	return rows
}

// owned is "   pid 87769(caffeinate): [0x...] 00:08:33 PreventUserIdleSystemSleep named: ...".
var owned = regexp.MustCompile(`^\s*pid (\d+)\([^)]*\): \[[^\]]*\] [\d:]+ (\w+) named`)

// HoldsAwake reports whether `pmset -g assertions` output shows process pid
// holding the Mac awake: how the keeper checks its own assertion took.
func HoldsAwake(out string, pid int) bool {
	return slices.ContainsFunc(parseAssertions(out), func(a assertion) bool {
		return a.PID == pid && (a.Type == "PreventUserIdleSystemSleep" || a.Type == "PreventSystemSleep")
	})
}
