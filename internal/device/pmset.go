package device

// Reading macOS's power state from what pmset and ioreg print. The parsers
// are here, without a build constraint, so they are tested on every OS
// against output recorded on a Mac (testdata/).

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

// parseBatt reads `pmset -g batt`:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=37945443)	100%; charged; 0:00 remaining present: true
//
// A Mac with no battery prints only the first line.
func parseBatt(out string) (*fleet.DevicePower, *fleet.DeviceBattery) {
	power := &fleet.DevicePower{Status: fleet.DevicePowerStatusUnknown, Why: fleet.String("pmset -g batt names no power source")}
	if m := drawing.FindStringSubmatch(out); m != nil {
		var src fleet.DevicePowerSource
		switch m[1] {
		case "AC Power":
			src = fleet.DevicePowerSourceAc
		case "Battery Power":
			src = fleet.DevicePowerSourceBattery
		case "UPS Power":
			src = fleet.DevicePowerSourceUps
		}
		if src != "" {
			power = &fleet.DevicePower{Status: fleet.DevicePowerStatusOk, Source: src.Ptr()}
		} else {
			power.Why = fleet.String("pmset -g batt names a power source this tool does not know: " + m[1])
		}
	}

	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "-InternalBattery-") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		if power.Source != nil && *power.Source == fleet.DevicePowerSourceBattery {
			return power, &fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusUnknown, Why: fleet.String("on battery power, and pmset -g batt lists no battery")}
		}
		return power, &fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusNone}
	}
	// One battery is what every Mac has; with more, the first speaks for
	// them and count says how many there are.
	m := battLine.FindStringSubmatch(lines[0])
	if m == nil {
		return power, &fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusUnknown, Why: fleet.String("pmset -g batt: a battery line this tool cannot read: " + clip(strings.TrimSpace(lines[0])))}
	}
	pct, _ := strconv.ParseFloat(m[1], 64)
	var state fleet.DeviceBatteryState
	switch strings.TrimSpace(m[2]) {
	case "charging", "finishing charge":
		state = fleet.DeviceBatteryStateCharging
	case "discharging":
		state = fleet.DeviceBatteryStateDischarging
	case "charged":
		state = fleet.DeviceBatteryStateFull
	case "AC attached":
		state = fleet.DeviceBatteryStateIdle
	default:
		return power, &fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusUnknown, Why: fleet.String("pmset -g batt: a battery state this tool does not know: " + clip(m[2]))}
	}
	// On battery the OS says discharging; anything else on battery is a
	// reading this tool has not seen, and the schema refuses charging there.
	if power.Source != nil && *power.Source == fleet.DevicePowerSourceBattery && state == fleet.DeviceBatteryStateCharging {
		state = fleet.DeviceBatteryStateDischarging
	}
	b := &fleet.DeviceBattery{Status: fleet.DeviceBatteryStatusOk, Count: fleet.Int(len(lines)), Percent: fleet.Float64(min(max(pct, 0), 100)), State: state.Ptr()}
	if r := remaining.FindStringSubmatch(m[3]); r != nil && state != fleet.DeviceBatteryStateFull {
		h, _ := strconv.Atoi(r[1])
		mins, _ := strconv.Atoi(r[2])
		if s := int64(h*3600 + mins*60); s > 0 {
			b.RemainingS = fleet.Int64(s)
		}
	}
	return power, b
}

var (
	drawing   = regexp.MustCompile(`Now drawing from '([^']+)'`)
	battLine  = regexp.MustCompile(`\t(\d+)%;\s*([^;]+);(.*)$`)
	remaining = regexp.MustCompile(`(\d+):(\d\d) remaining`)
)

// settings is what `pmset -g` says is in use now: each setting's first
// word, by name (sleep, displaysleep, SleepDisabled).
func parseSettings(out string) map[string]string {
	got := map[string]string{}
	in := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Currently in use") {
			in = true
			continue
		}
		if !in || !strings.HasPrefix(l, " ") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		// "Sleep On Power Button 1": the name has spaces; the value is last.
		// "sleep 0 (sleep prevented by ...)": the value is second.
		if _, err := strconv.Atoi(f[1]); err == nil || len(f) == 2 {
			got[f[0]] = f[1]
		}
	}
	return got
}

// assertion is one row of `pmset -g assertions`' "Listed by owning process".
type assertion struct {
	PID     int
	Process string
	Type    string
}

// parseAssertions reads `pmset -g assertions`: whether idle sleep is held off
// now (the system-wide PreventUserIdleSystemSleep or PreventSystemSleep), and
// every process assertion.
func parseAssertions(out string) (inhibited bool, rows []assertion) {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && (f[0] == "PreventUserIdleSystemSleep" || f[0] == "PreventSystemSleep") && f[1] == "1" {
			inhibited = true
		}
		if m := owned.FindStringSubmatch(l); m != nil {
			pid, _ := strconv.Atoi(m[1])
			rows = append(rows, assertion{PID: pid, Process: m[2], Type: m[3]})
		}
	}
	return inhibited, rows
}

// owned is "   pid 87769(caffeinate): [0x...] 00:08:33 PreventUserIdleSystemSleep named: ...".
var owned = regexp.MustCompile(`^\s*pid (\d+)\(([^)]*)\): \[[^\]]*\] [\d:]+ (\w+) named`)

// preventsSleep is whether an assertion of this type holds the Mac awake.
func preventsSleep(typ string) bool {
	return typ == "PreventUserIdleSystemSleep" || typ == "PreventSystemSleep"
}

// HoldsAwake reports whether `pmset -g assertions` output shows process pid
// holding the Mac awake: how the keeper checks its own assertion took.
func HoldsAwake(out string, pid int) bool {
	_, rows := parseAssertions(out)
	return slices.ContainsFunc(rows, func(a assertion) bool { return a.PID == pid && preventsSleep(a.Type) })
}

// sleepSection is the sleep section from `pmset -g` and `pmset -g
// assertions`. Closing the lid sleeps the Mac unless sleep is disabled
// (`pmset -a disablesleep 1`, SleepDisabled 1): that is all this reads, and
// what else changes it (an external display, for one) has not been measured.
func sleepSection(settings map[string]string, assertions string, hasLid bool) *fleet.DeviceSleep {
	idle, err := strconv.ParseInt(settings["sleep"], 10, 64)
	if err != nil || idle < 0 {
		return &fleet.DeviceSleep{Status: fleet.DeviceSleepStatusUnknown, Why: fleet.String("pmset -g gives no sleep setting in use")}
	}
	s := &fleet.DeviceSleep{Status: fleet.DeviceSleepStatusOk, IdleS: fleet.Int64(idle * 60)}
	if d, err := strconv.ParseInt(settings["displaysleep"], 10, 64); err == nil && d >= 0 {
		s.DisplayS = fleet.Int64(d * 60)
	}
	inhibited, rows := parseAssertions(assertions)
	s.Inhibited = fleet.Bool(inhibited)
	if inhibited {
		for _, a := range rows {
			if preventsSleep(a.Type) && a.Process != "" && !slices.Contains(s.Inhibitors, a.Process) && len(s.Inhibitors) < 8 {
				s.Inhibitors = append(s.Inhibitors, clip(a.Process))
			}
		}
	}
	if hasLid {
		action := fleet.DeviceSleepLidActionSleep
		if settings["SleepDisabled"] == "1" {
			action = fleet.DeviceSleepLidActionNothing
		}
		s.LidAction = action.Ptr()
	}
	return s
}

// parseClamshell reads `ioreg -r -k AppleClamshellState -d 1`: whether the
// lid is closed, and whether there is a lid at all (a Mac mini has none).
func parseClamshell(out string) (closed, hasLid bool) {
	m := clamshell.FindStringSubmatch(out)
	if m == nil {
		return false, false
	}
	return m[1] == "Yes", true
}

var clamshell = regexp.MustCompile(`"AppleClamshellState" = (Yes|No)`)

// clip keeps a string inside the schema's 200 bytes.
func clip(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}
