package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestLedgerCapacity: a machine's newest capacity snapshot is its capacity in
// the view and on the page; a snapshot that cannot be read is cannot tell,
// never zeros; a machine that sent none has none.
//
// Negative controls, run by hand: keep the first snapshot instead of the last,
// and the "newest" check fails; drop the DiskTotal check in readCapacity, and
// the unreadable one shows as a machine with 0 free (run 1 Oct 2026: both
// failed).
func TestLedgerCapacity(t *testing.T) {
	now := testNow
	h := Handler(ledgerEnv(t, &now))
	snap := func(id string, at time.Time, free int64, clone string) Event {
		e := ev(id, "capacity", "", "", at)
		e.Command = "capacity"
		e.Detail = fmt.Sprintf(`{"disk_free":%d,"disk_total":494384795648,"mem":17179869184,"mem_running":8589934592,`+
			`"vms":2,"running":1,"stale":0,"promised":0,"tool":10000000000,"clone":%q,"more_clones":7,"more_running":0}`, free, clone)
		return e
	}
	older := snap("evt-cap-0001", now.Add(-2*time.Hour), 50<<30, "yes")
	newer := snap("evt-cap-0002", now.Add(-time.Hour), 41<<30, "no")
	bad := ev("evt-cap-0003", "capacity", "", "", now.Add(-time.Hour))
	bad.Machine, bad.Host, bad.Detail = "m0000002", "mac-b", `{"disk_free":1}`
	quiet := ev("evt-run-0009", "start", "op-run-009", "x", now.Add(-time.Hour))
	quiet.Machine, quiet.Host = "m0000003", "mac-c"
	if w := postEvents(h, writeTok, newer, older, bad, quiet); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w := do(h, "GET", "/api/ledger/vms", readTok, nil, "")
	var v ledgerView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body)
	}
	by := map[string]machineView{}
	for _, m := range v.Machines {
		by[m.Host] = m
	}
	if c := by["mac-a"].Capacity; c == nil || c.DiskFree != 41<<30 || c.Clone != "no" || by["mac-a"].CapacityAt != ms(now.Add(-time.Hour)) {
		t.Errorf("mac-a: %+v, want the newest snapshot (41 GiB free, no)", by["mac-a"])
	}
	if m := by["mac-b"]; m.Capacity != nil || m.CapacityErr == "" {
		t.Errorf("mac-b sent an unreadable snapshot: %+v, want cannot tell", m)
	}
	if m := by["mac-c"]; m.Capacity != nil || m.CapacityErr != "" {
		t.Errorf("mac-c sent none: %+v", m)
	}
	page := do(h, "GET", "/api/ledger/", readTok, nil, "").Body.String()
	for _, want := range []string{"<h2>Capacity</h2>", "41.0 GiB", "cannot tell", "7 by disk, 0 to run"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
}
