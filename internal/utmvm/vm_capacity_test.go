package utmvm

import (
	"errors"
	"strings"
	"testing"
)

// TestDecideCapacity: yes, no and cannot tell, on the machine this was written
// on (16 GiB; irgo-win11 and installs 8 GiB, clones 4) and a larger one.
// Every case that cannot be answered must come out as cannot tell, never yes.
//
// Negative controls, run by hand: skip VMs whose MiB is 0 instead of
// returning cannot tell, and "a running VM UTM will not size" says yes;
// compare left with 0 instead of hostMemoryReserveBytes, and "irgo-win11 and
// a clone running" says yes; count a new clone at vmMemoryMiB, and "one 4 GiB
// clone fits" says no; ignore freeErr and "disk unreadable" says yes.
func TestDecideCapacity(t *testing.T) {
	const gib = 1 << 30
	running := vmMemory{Name: "irgo-win11", Status: "started", MiB: 8192}
	paused := vmMemory{Name: "a1", Status: "paused", MiB: 4096}
	clone := vmMemory{Name: "a2", Status: "started", MiB: 4096}
	install := CapacityPlan{VM: "z1", Disk: diskForInstall}
	stopped := vmMemory{Name: "irgo-golden", Status: "stopped", MiB: 8192}
	newClone := CapacityPlan{VM: "z1", Disk: diskForClone}
	plenty := int64(100 * gib)

	cases := []struct {
		name string
		f    capacityFacts
		want Answer
	}{
		{"16 GiB, nothing running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{stopped}, free: plenty}, AnswerYes},
		{"16 GiB, irgo-win11 running: one 4 GiB clone fits", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{running, stopped}, free: plenty}, AnswerYes},
		{"16 GiB, irgo-win11 and a clone running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{running, clone}, free: plenty}, AnswerNo},
		{"16 GiB, irgo-win11 running and a clone paused (UTM keeps its memory)", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{running, paused}, free: plenty}, AnswerNo},
		{"16 GiB, irgo-win11 running, an install wants 8", capacityFacts{plan: install, host: 16 * gib, vms: []vmMemory{running}, free: plenty}, AnswerNo},
		{"16 GiB, irgo-win11 running and another vm-create making one", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{running}, pending: []string{"z0"}, free: plenty}, AnswerNo},
		{"32 GiB, one running", capacityFacts{plan: newClone, host: 32 * gib, vms: []vmMemory{running}, free: plenty}, AnswerYes},
		{"32 GiB, 8 + 8 + 8 + 4 running", capacityFacts{plan: newClone, host: 32 * gib, vms: []vmMemory{running, clone, {Name: "b", Status: "started", MiB: 8192}, {Name: "c", Status: "started", MiB: 8192}}, free: plenty}, AnswerNo},
		{"memory unreadable", capacityFacts{plan: newClone, hostErr: errors.New("sysctl"), free: plenty}, AnswerCannotTell},
		{"UTM unreadable", capacityFacts{plan: newClone, host: 64 * gib, vmsErr: errors.New("osascript"), free: plenty}, AnswerCannotTell},
		{"a running VM UTM will not size", capacityFacts{plan: newClone, host: 64 * gib, vms: []vmMemory{{Name: "x", Status: "started"}}, free: plenty}, AnswerCannotTell},
		{"a stopped VM UTM will not size does not matter", capacityFacts{plan: newClone, host: 64 * gib, vms: []vmMemory{{Name: "x", Status: "stopped"}}, free: plenty}, AnswerYes},
		{"disk short for a clone", capacityFacts{plan: newClone, host: 64 * gib, free: 13 * gib}, AnswerNo},
		{"disk enough for a clone alone, short with what is promised to the others", capacityFacts{plan: newClone, host: 64 * gib, free: 15 * gib, promised: 4 * gib}, AnswerNo},
		{"what is promised to the others unknown", capacityFacts{plan: newClone, host: 64 * gib, free: plenty, promisedErr: errors.New("getattrlist")}, AnswerCannotTell},
		{"disk enough for a clone", capacityFacts{plan: newClone, host: 64 * gib, free: 15 * gib}, AnswerYes},
		{"disk enough for a clone, short for an install", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForInstall}, host: 64 * gib, free: 21 * gib}, AnswerNo},
		{"disk unreadable", capacityFacts{plan: newClone, host: 64 * gib, freeErr: errors.New("statfs")}, AnswerCannotTell},
		{"booting an existing stopped VM needs no disk", capacityFacts{plan: CapacityPlan{VM: "z1", Exists: true}, host: 16 * gib, vms: []vmMemory{{Name: "z1", Status: "stopped", MiB: 8192}}, freeErr: errors.New("statfs")}, AnswerYes},
		{"an existing VM already running starts nothing", capacityFacts{plan: CapacityPlan{VM: "z1", Exists: true}, host: 16 * gib, vms: []vmMemory{running, {Name: "Z1", Status: "started", MiB: 8192}}}, AnswerYes},
		{"an existing VM UTM does not list", capacityFacts{plan: CapacityPlan{VM: "z1", Exists: true}, host: 64 * gib}, AnswerCannotTell},
		{"16 GiB, one running, -overcommit", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForClone, Overcommit: true}, host: 16 * gib, vms: []vmMemory{running}, free: plenty}, AnswerYes},
		{"-overcommit does not excuse the disk", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForClone, Overcommit: true}, host: 16 * gib, vms: []vmMemory{running}, free: 13 * gib}, AnswerNo},
		{"-overcommit does not answer what UTM would not", capacityFacts{plan: CapacityPlan{VM: "z1", Disk: diskForClone, Overcommit: true}, host: 16 * gib, vmsErr: errors.New("osascript"), free: plenty}, AnswerCannotTell},
		{"16 GiB, a 4 GiB VM running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{{Name: "s", Status: "started", MiB: 4096}}, free: plenty}, AnswerYes},
		{"16 GiB, a 6 GiB VM running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{{Name: "s", Status: "started", MiB: 6144}}, free: plenty}, AnswerYes},
		{"16 GiB, a 10 GiB VM running", capacityFacts{plan: newClone, host: 16 * gib, vms: []vmMemory{{Name: "s", Status: "started", MiB: 10240}}, free: plenty}, AnswerNo},
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

// TestDecideQuota: an owner at its VM count or its disk is refused, one who
// cannot be counted is cannot tell, and a plan with no owner (capacity's
// "could anyone?") skips the quota.
//
// Negative controls, run by hand: compare ownerVMs+1 >= VMs instead of >, and
// "one of two" says no; drop the ownerErr case, and "records unreadable" says
// yes (run 1 Oct 2026: both failed as described).
func TestDecideQuota(t *testing.T) {
	const gib = 1 << 30
	q := Quota{VMs: 2, Bytes: 16 * gib}
	owned := func(vms int, heldGiB int64) capacityFacts {
		return capacityFacts{plan: CapacityPlan{VM: "z9", Disk: diskForClone, Owner: "agent-a"}, host: 64 * gib,
			free: 100 * gib, quota: q, ownerVMs: vms, ownerHeld: heldGiB * gib}
	}
	cases := []struct {
		name string
		f    capacityFacts
		want Answer
	}{
		{"none yet", owned(0, 0), AnswerYes},
		{"one of two", owned(1, 4), AnswerYes},
		{"two of two", owned(2, 8), AnswerNo},
		{"one VM, but it holds 13 GiB of 16", owned(1, 13), AnswerNo},
		{"quota unreadable", func() capacityFacts { f := owned(0, 0); f.quotaErr = errors.New("bad"); return f }(), AnswerCannotTell},
		{"records unreadable", func() capacityFacts { f := owned(0, 0); f.ownerErr = errors.New("bad"); return f }(), AnswerCannotTell},
		{"no limit", func() capacityFacts { f := owned(9, 90); f.quota = Quota{}; return f }(), AnswerYes},
		{"no owner: the quota is not asked", func() capacityFacts { f := owned(9, 90); f.plan.Owner = ""; return f }(), AnswerYes},
		{"an existing VM is not a new one", func() capacityFacts {
			f := owned(2, 8)
			f.plan.Exists, f.plan.Disk = true, diskNone
			f.vms = []vmMemory{{Name: "z9", Status: "stopped", MiB: 8192}}
			return f
		}(), AnswerYes},
	}
	for _, c := range cases {
		got, why := decideCapacity(c.f)
		if got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}

// TestParseQuota: defaults, overrides, 0 for no limit, and a typo refused
// rather than read as no limit.
func TestParseQuota(t *testing.T) {
	if q, err := parseQuota("", ""); err != nil || q.VMs != defaultQuotaVMs || q.Bytes != defaultQuotaBytes {
		t.Fatalf("defaults: %+v %v", q, err)
	}
	if q, err := parseQuota("0", "2.5"); err != nil || q.VMs != 0 || q.Bytes != 5<<29 {
		t.Fatalf("overrides: %+v %v", q, err)
	}
	for _, bad := range [][2]string{{"two", ""}, {"", "lots"}, {"-1", ""}} {
		if _, err := parseQuota(bad[0], bad[1]); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestGatherDiskFacts: every listed VM but the one being made counts what it
// is still promised, the golden image nothing; the owner's VMs are counted
// against its quota from their records; one VM that cannot be measured makes
// the whole answer cannot tell.
//
// Negative control, run by hand: skip a VM whose measure fails instead of
// setting promisedErr, and the last check fails.
func TestGatherDiskFacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(QuotaVMsEnv, "")
	t.Setenv(QuotaGiBEnv, "")
	const gib = 1 << 30
	for _, r := range []VMRecord{{Name: "a1", Owner: "agent-a"}, {Name: "b1", Owner: "agent-b"}} {
		if err := writeRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	disks := map[string]vmDisk{
		"irgo-win11":  {Private: 20 * gib},    // past its reserve: nothing promised
		"irgo-golden": {Private: 0},           // never grows
		"a1":          {Private: 1 * gib},     // 3 GiB of its 4 still promised
		"b1":          {Private: gib / 2},     // 3.5 promised
		"z9":          {Err: errors.New("x")}, // the VM being made is never measured
	}
	measure := func(n string) vmDisk { return disks[n] }
	f := capacityFacts{plan: CapacityPlan{VM: "z9", Disk: diskForClone, Owner: "AGENT-A"},
		vms:     []vmMemory{{Name: "irgo-win11"}, {Name: "irgo-golden"}, {Name: "a1"}, {Name: "b1"}, {Name: "z9"}},
		pending: []string{"c1"}}
	gatherDiskFacts(&f, measure)
	if f.promisedErr != nil || f.ownerErr != nil || f.quotaErr != nil {
		t.Fatalf("errors: %v %v %v", f.promisedErr, f.ownerErr, f.quotaErr)
	}
	if want := int64(3*gib + 3.5*gib + cloneReserveBytes); f.promised != want {
		t.Errorf("promised %d, want %d", f.promised, want)
	}
	if f.ownerVMs != 1 || f.ownerHeld != cloneReserveBytes {
		t.Errorf("agent-a has %d VMs holding %d, want 1 holding %d", f.ownerVMs, f.ownerHeld, int64(cloneReserveBytes))
	}
	disks["b1"] = vmDisk{Err: errors.New("getattrlist: permission denied")}
	f = capacityFacts{plan: f.plan, vms: f.vms}
	gatherDiskFacts(&f, measure)
	if a, why := decideCapacity(capacityFacts{plan: f.plan, host: 64 * gib, free: 100 * gib, promised: f.promised,
		promisedErr: f.promisedErr, quota: f.quota}); a != AnswerCannotTell {
		t.Errorf("an unmeasurable VM: %s (%s), want cannot tell", a, why)
	}
}

// TestTheRefusalNamesWhoIsRunning: a no is only useful if it says which VMs
// hold the memory, so the caller knows whose to ask about.
func TestTheRefusalNamesWhoIsRunning(t *testing.T) {
	_, why := decideCapacity(capacityFacts{
		plan: CapacityPlan{VM: "z1", Disk: diskForClone}, host: 16 << 30,
		vms: []vmMemory{{Name: "irgo-win11", Status: "started", MiB: 8192}, {Name: "a2", Status: "started", MiB: 4096}}, free: 100 << 30,
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
