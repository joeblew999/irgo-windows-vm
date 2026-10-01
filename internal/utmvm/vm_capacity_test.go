package utmvm

import (
	"errors"
	"strings"
	"testing"
)

// TestDecideCapacity: yes, no and cannot tell, on the machine this was written
// on (16 GiB, VMs of 8 GiB) and a larger one. Every case that cannot be
// answered must come out as cannot tell, never yes.
//
// Negative controls, run by hand: skip VMs whose MiB is 0 instead of
// returning cannot tell, and "a running VM UTM will not size" says yes;
// compare left with 0 instead of hostMemoryReserveBytes, and "16 GiB, one
// running" says yes (run 1 Oct 2026: it, the paused and the pending cases
// failed); ignore freeErr and "disk unreadable" says yes.
func TestDecideCapacity(t *testing.T) {
	const gib = 1 << 30
	running := vmMemory{Name: "irgo-win11", Status: "started", MiB: 8192}
	paused := vmMemory{Name: "a1", Status: "paused", MiB: 8192}
	stopped := vmMemory{Name: "irgo-golden", Status: "stopped", MiB: 8192}
	newClone := CapacityPlan{VM: "z1", Disk: diskForClone}
	plenty := int64(100 * gib)

	cases := []struct {
		name string
		f    capacityFacts
		want Answer
	}{
		{"16 GiB, nothing running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{stopped}, free: plenty}, AnswerYes},
		{"16 GiB, one running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{running, stopped}, free: plenty}, AnswerNo},
		{"16 GiB, one paused (UTM keeps its memory)", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{paused}, free: plenty}, AnswerNo},
		{"16 GiB, another vm-create making one", capacityFacts{plan: newClone, host: 16 * gib, pending: []string{"z0"}, free: plenty}, AnswerNo},
		{"32 GiB, one running", capacityFacts{plan: newClone, host: 32 * gib, vms: []vmMemory{running}, free: plenty}, AnswerYes},
		{"32 GiB, three running", capacityFacts{plan: newClone, host: 32 * gib, vms: []vmMemory{running, paused, {Name: "b", Status: "started", MiB: 8192}}, free: plenty}, AnswerNo},
		{"memory unreadable", capacityFacts{plan: newClone, hostErr: errors.New("sysctl"), free: plenty}, AnswerCannotTell},
		{"UTM unreadable", capacityFacts{plan: newClone, host: 64 * gib, vmsErr: errors.New("osascript"), free: plenty}, AnswerCannotTell},
		{"a running VM UTM will not size", capacityFacts{plan: newClone, host: 64 * gib, vms: []vmMemory{{Name: "x", Status: "started"}}, free: plenty}, AnswerCannotTell},
		{"a stopped VM UTM will not size does not matter", capacityFacts{plan: newClone, host: 64 * gib, vms: []vmMemory{{Name: "x", Status: "stopped"}}, free: plenty}, AnswerYes},
		{"disk short for a clone", capacityFacts{plan: newClone, host: 64 * gib, free: 15 * gib}, AnswerNo},
		{"disk enough for a clone", capacityFacts{plan: newClone, host: 64 * gib, free: 21 * gib}, AnswerYes},
		{"disk enough for a clone, short for an install", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForInstall}, host: 64 * gib, free: 21 * gib}, AnswerNo},
		{"disk unreadable", capacityFacts{plan: newClone, host: 64 * gib, freeErr: errors.New("statfs")}, AnswerCannotTell},
		{"booting an existing stopped VM needs no disk", capacityFacts{plan: CapacityPlan{VM: "z1", Exists: true}, host: 16 * gib, vms: []vmMemory{{Name: "z1", Status: "stopped", MiB: 8192}}, freeErr: errors.New("statfs")}, AnswerYes},
		{"an existing VM already running starts nothing", capacityFacts{plan: CapacityPlan{VM: "z1", Exists: true}, host: 16 * gib, vms: []vmMemory{running, {Name: "Z1", Status: "started", MiB: 8192}}}, AnswerYes},
		{"an existing VM UTM does not list", capacityFacts{plan: CapacityPlan{VM: "z1", Exists: true}, host: 64 * gib}, AnswerCannotTell},
		{"16 GiB, one running, -overcommit", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForClone, Overcommit: true}, host: 16 * gib, vms: []vmMemory{running}, free: plenty}, AnswerYes},
		{"-overcommit does not excuse the disk", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForClone, Overcommit: true}, host: 16 * gib, vms: []vmMemory{running}, free: 15 * gib}, AnswerNo},
		{"-overcommit does not answer what UTM would not", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForClone, Overcommit: true}, host: 16 * gib, vmsErr: errors.New("osascript"), free: plenty}, AnswerCannotTell},
		{"16 GiB, a 4 GiB VM running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{{Name: "s", Status: "started", MiB: 4096}}, free: plenty}, AnswerYes},
		{"16 GiB, a 6 GiB VM running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{{Name: "s", Status: "started", MiB: 6144}}, free: plenty}, AnswerNo},
	}
	for _, c := range cases {
		got, why := decideCapacity(c.f)
		if got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, got, why, c.want)
		}
		if why == "" {
			t.Errorf("%s: no reason given", c.name)
		}
	}
}

// TestTheRefusalNamesWhoIsRunning: a no is only useful if it says which VMs
// hold the memory, so the caller knows whose to ask about.
func TestTheRefusalNamesWhoIsRunning(t *testing.T) {
	_, why := decideCapacity(capacityFacts{
		plan: CapacityPlan{VM: "z1", Disk: diskForClone}, host: 16 << 30,
		vms: []vmMemory{{Name: "irgo-win11", Status: "started", MiB: 8192}}, free: 100 << 30,
	})
	if !strings.Contains(why, "irgo-win11") {
		t.Fatalf("the refusal does not name the running VM: %s", why)
	}
}

// TestParseMemoryTable reads UTM's answer, and refuses one it does not
// understand rather than skipping a line that might be a running VM.
//
// Negative control, run by hand: `continue` on a short line instead of
// returning an error, and the malformed case passes.
func TestParseMemoryTable(t *testing.T) {
	out := "38791348-ED91-41A4-810C-DD08C04FD65C\tstarted\t8192\tirgo-win11\n" +
		"ACAE56D7-C174-4107-B126-EBBFF633A5F9\tstopped\t?\tirgo golden copy\n"
	vms, err := parseMemoryTable(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 2 || vms[0].MiB != 8192 || vms[0].Status != "started" || vms[1].MiB != 0 || vms[1].Name != "irgo golden copy" {
		t.Fatalf("parsed %+v", vms)
	}
	if _, err := parseMemoryTable("garbage without tabs\n"); err == nil {
		t.Fatal("a line that is not four fields was accepted")
	}
	if vms, err := parseMemoryTable(""); err != nil || len(vms) != 0 {
		t.Fatalf("no VMs: %v %v", vms, err)
	}
}
