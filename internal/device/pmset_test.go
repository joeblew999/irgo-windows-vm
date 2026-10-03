package device

import (
	"os"
	"path/filepath"
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

// TestParsePower: this Mac's `pmset -g batt` on AC (recorded 3 Oct 2026),
// on battery, a Mac with no battery, a source this code does not know, and
// nothing printed. What cannot be told is unknown with why, never a guess.
//
// Negative control, run by hand: map "Battery Power" to ac and the battery
// case fails; return ok for an unknown name and that case fails.
func TestParsePower(t *testing.T) {
	for name, c := range map[string]struct {
		out  string
		want fleet.DevicePowerSource
	}{
		"this Mac on AC": {fixture(t, "pmset-batt-ac.txt"), fleet.DevicePowerSourceAc},
		"on battery":     {"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t57%; discharging; 3:05 remaining present: true\n", fleet.DevicePowerSourceBattery},
		"no battery":     {"Now drawing from 'AC Power'\n", fleet.DevicePowerSourceAc},
		"a UPS":          {"Now drawing from 'UPS Power'\n", fleet.DevicePowerSourceUps},
		"unknown source": {"Now drawing from 'Solar'\n", ""},
		"nothing":        {"", ""},
	} {
		p := parsePower(c.out)
		switch {
		case c.want == "" && (p.Status != fleet.DevicePowerStatusUnknown || p.Why == nil || p.Source != nil):
			t.Errorf("%s: %+v, want unknown with why", name, p)
		case c.want != "" && (p.Status != fleet.DevicePowerStatusOk || p.Source == nil || *p.Source != c.want):
			t.Errorf("%s: %+v, want ok %s", name, p, c.want)
		}
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
