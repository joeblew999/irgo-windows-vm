package device

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParseBatt: this Mac's `pmset -g batt` on AC, charged (recorded 3 Oct
// 2026), and the other shapes pmset prints. A battery state this code does
// not know is unknown with the line, never a zero.
//
// Negative control, run by hand: map "charged" to discharging and the AC
// case fails; drop the -InternalBattery- filter and the desktop case
// reports a battery.
func TestParseBatt(t *testing.T) {
	power, b := parseBatt(fixture(t, "pmset-batt-ac.txt"))
	if power.Status != fleet.DevicePowerStatusOk || *power.Source != fleet.DevicePowerSourceAc {
		t.Fatalf("power %+v, want ok ac", power)
	}
	if b.Status != fleet.DeviceBatteryStatusOk || *b.Percent != 100 || *b.State != fleet.DeviceBatteryStateFull || *b.Count != 1 || b.RemainingS != nil {
		t.Fatalf("battery %+v, want ok, 1, 100%%, full, no remaining", b)
	}

	_, b = parseBatt("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t57%; discharging; 3:05 remaining present: true\n")
	if *b.State != fleet.DeviceBatteryStateDischarging || *b.Percent != 57 || *b.RemainingS != 3*3600+5*60 {
		t.Fatalf("on battery: %+v", b)
	}
	_, b = parseBatt("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t80%; AC attached; not charging present: true\n")
	if *b.State != fleet.DeviceBatteryStateIdle {
		t.Fatalf("AC attached, not charging: %v, want idle", *b.State)
	}
	_, b = parseBatt("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t80%; (no estimate); weird present: true\n")
	if b.Status != fleet.DeviceBatteryStatusUnknown || b.Why == nil || b.Percent != nil {
		t.Fatalf("an unknown state: %+v, want unknown with why and no values", b)
	}
	power, b = parseBatt("Now drawing from 'AC Power'\n")
	if power.Status != fleet.DevicePowerStatusOk || b.Status != fleet.DeviceBatteryStatusNone {
		t.Fatalf("a Mac with no battery: %+v %+v, want ac and none", power, b)
	}
	power, _ = parseBatt("")
	if power.Status != fleet.DevicePowerStatusUnknown || power.Why == nil {
		t.Fatalf("nothing printed: %+v, want unknown with why", power)
	}
}

// TestSleepSection: this Mac's `pmset -g` (sleep 0 on AC, set by hand) and
// assertions, recorded 3 Oct 2026; and the lid action when sleep is
// disabled.
//
// Negative control, run by hand: read the value as the last field and
// "sleep 0 (sleep prevented by ...)" is lost: the section is unknown.
func TestSleepSection(t *testing.T) {
	settings := parseSettings(fixture(t, "pmset-g.txt"))
	s := sleepSection(settings, fixture(t, "pmset-assertions.txt"), true)
	if s.Status != fleet.DeviceSleepStatusOk || *s.IdleS != 0 || *s.DisplayS != 1800 || !*s.Inhibited {
		t.Fatalf("sleep %+v, want ok, idle 0, display 1800, inhibited", s)
	}
	if !slices.Equal(s.Inhibitors, []string{"caffeinate", "powerd"}) {
		t.Fatalf("inhibitors %v, want caffeinate and powerd once each, not WindowServer's UserIsActive", s.Inhibitors)
	}
	if *s.LidAction != fleet.DeviceSleepLidActionSleep {
		t.Fatalf("lid action %v, want sleep", *s.LidAction)
	}
	settings["SleepDisabled"] = "1"
	if s := sleepSection(settings, "", true); *s.LidAction != fleet.DeviceSleepLidActionNothing || *s.Inhibited {
		t.Fatalf("sleep disabled: %+v, want lid action nothing, nothing inhibiting", s)
	}
	if s := sleepSection(settings, "", false); s.LidAction != nil {
		t.Fatal("a lid action on a Mac with no lid")
	}
}

// TestHoldsAwake: the keeper's check that its caffeinate took finds that
// pid's PreventUserIdleSystemSleep, and not another process's or another
// kind of assertion.
//
// Negative control, run by hand: drop the pid comparison and pid 1 is found
// holding it.
func TestHoldsAwake(t *testing.T) {
	out := fixture(t, "pmset-assertions.txt")
	if !HoldsAwake(out, 614) || !HoldsAwake(out, 1097) {
		t.Fatal("caffeinate's assertions not found")
	}
	if HoldsAwake(out, 635) || HoldsAwake(out, 1) {
		t.Fatal("a process that holds nothing awake was found holding it")
	}
}

// TestParseClamshell: this Mac's lid, open; closed; and a Mac with none.
func TestParseClamshell(t *testing.T) {
	if closed, ok := parseClamshell(fixture(t, "ioreg-clamshell.txt")); !ok || closed {
		t.Fatalf("this Mac: closed %v, lid %v; want open, a lid", closed, ok)
	}
	if closed, ok := parseClamshell(`"AppleClamshellState" = Yes`); !ok || !closed {
		t.Fatal("a closed lid read as open")
	}
	if _, ok := parseClamshell(""); ok {
		t.Fatal("a lid on a Mac with none")
	}
}
