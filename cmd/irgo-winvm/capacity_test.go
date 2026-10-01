package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestCapacitySnapshotFitsTheLedger: the snapshot a capacity event carries
// stays under the 500 bytes the Worker keeps of a detail, with every number
// at its widest, holds no name, and counts running and stale VMs.
//
// Negative control, run by hand: count every VM as running, and the running
// check fails.
func TestCapacitySnapshotFitsTheLedger(t *testing.T) {
	const huge = int64(1) << 62
	r := utmvm.CapacityReport{DiskFree: huge, DiskTotal: huge, Memory: huge, RunningMemory: huge, Promised: huge,
		VMs: []utmvm.VMUsage{
			{Name: "secret-owner-vm", Owner: "someone@host:repo", Status: "started"},
			{Name: "b", Status: "stopped", Stale: true},
			{Name: "c", Status: "paused"},
		},
		Data: []utmvm.DataUsage{{What: "shots", Private: huge}},
		Room: utmvm.Room{Clone: "cannot tell", MoreClones: 1 << 30, MoreRunning: 1 << 30}}
	s := snapshotOf(r)
	if s.Running != 2 || s.Stale != 1 || s.VMs != 3 {
		t.Errorf("counted %d running, %d stale of %d; want 2, 1 of 3", s.Running, s.Stale, s.VMs)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 500 {
		t.Errorf("snapshot is %d bytes, over the Worker's 500: %s", len(b), b)
	}
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "someone") {
		t.Errorf("snapshot carries a name: %s", b)
	}
}
