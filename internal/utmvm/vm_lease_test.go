package utmvm

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestDecideReap is the reaping rule, one case per reason, each with the fact
// that decides it. Deleting is the answer for exactly one case: a clone whose
// lease has run out, that UTM lists, that nothing is using.
//
// Negative controls, run by hand: move the protectedVM case below the lease
// check and irgo-win11 is deleted; drop the lockBusy case and a VM in use is
// deleted; treat findErr like "not found" and a VM UTM could not be asked
// about is forgotten.
func TestDecideReap(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	lease := 24 * time.Hour
	old := VMRecord{Name: "z1", Owner: "agent", Created: now.Add(-72 * time.Hour), LastUsed: now.Add(-48 * time.Hour)}
	fresh := VMRecord{Name: "z2", Owner: "agent", Created: now.Add(-72 * time.Hour), LastUsed: now.Add(-time.Hour)}
	never := VMRecord{Name: "z3", Owner: "agent", Created: now.Add(-30 * time.Hour)}
	making := old
	making.CreatingPID = 4242

	exists := reapFacts{exists: true}
	cases := []struct {
		name string
		r    VMRecord
		f    reapFacts
		want ReapAction
	}{
		{"expired and idle", old, exists, ReapDelete},
		{"never used since made, made long ago", never, exists, ReapDelete},
		{"in lease", fresh, exists, ReapKeep},
		{"expired but a command holds its lock", old, reapFacts{exists: true, lockBusy: true}, ReapKeep},
		{"expired but its lock cannot be read", old, reapFacts{exists: true, lockErr: errors.New("EIO")}, ReapKeep},
		{"expired but UTM cannot be asked", old, reapFacts{findErr: errors.New("utmctl: exit 1")}, ReapKeep},
		{"expired and still being made", making, reapFacts{exists: true, creatorUp: true}, ReapKeep},
		{"UTM has no such VM", old, reapFacts{}, ReapForget},
		{"the owner's VM, whatever its record says", VMRecord{Name: "IRGO-WIN11", Created: old.Created}, exists, ReapKeep},
		{"the golden image", VMRecord{Name: GoldenVMName, Created: old.Created}, exists, ReapKeep},
		{"the golden image's verification clone", VMRecord{Name: windowsGuest.goldenVerify(), Created: old.Created}, exists, ReapKeep},
		{"the Linux golden image", VMRecord{Name: "IRGO-GOLDEN-LINUX", Created: old.Created}, exists, ReapKeep},
		{"the Linux golden image's verification clone", VMRecord{Name: linuxGuest.goldenVerify(), Created: old.Created}, exists, ReapKeep},
		{"a name that only starts like a golden image", VMRecord{Name: "irgo-golden-linux2", Created: old.Created}, exists, ReapDelete},
	}
	for _, c := range cases {
		d := decideReap(c.r, c.f, lease, now)
		if d.Action != c.want {
			t.Errorf("%s: action %d (%s), want %d", c.name, d.Action, d.Why, c.want)
		}
		if d.Why == "" {
			t.Errorf("%s: no reason given", c.name)
		}
	}
}

// TestRecordsRoundTripAndTouch: a record written is read back, TouchVM moves
// only its last use, a VM without a record is not given one by being used,
// and ForgetVM can run twice.
//
// Negative control, run by hand: have TouchVM write a record when !ok and the
// "no record" check fails.
func TestRecordsRoundTripAndTouch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	made := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := writeRecord(VMRecord{Name: "Z1", Owner: "agent", OwnerSource: SourceMCP, Created: made, LastUsed: made}); err != nil {
		t.Fatal(err)
	}
	if err := TouchVM("z1"); err != nil {
		t.Fatal(err)
	}
	r, ok, err := readRecord("z1")
	if err != nil || !ok {
		t.Fatalf("record of z1: ok=%v err=%v", ok, err)
	}
	if r.Owner != "agent" || !r.Created.Equal(made) || !r.LastUsed.After(made) {
		t.Fatalf("after a touch the record is %+v; want the owner and creation kept and last use moved", r)
	}

	if err := TouchVM("unrecorded"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := readRecord("unrecorded"); ok {
		t.Fatal("using a VM with no record gave it one")
	}

	records, bad, err := VMRecords()
	if err != nil || len(bad) != 0 || len(records) != 1 || records[0].Name != "Z1" {
		t.Fatalf("VMRecords = %+v, bad %v, err %v", records, bad, err)
	}
	for i := 0; i < 2; i++ {
		if err := ForgetVM("z1"); err != nil {
			t.Fatalf("forget #%d: %v", i+1, err)
		}
	}
	if _, ok, _ := readRecord("z1"); ok {
		t.Fatal("the record survived ForgetVM")
	}
}

// TestUnreadableRecordIsReportedNotDropped: a record that does not parse
// must not make its VM look unowned, so it is listed as bad.
//
// Negative control, run by hand: `continue` without appending to bad in
// VMRecords and bad is empty.
func TestUnreadableRecordIsReportedNotDropped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(RecordsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath("broken"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, bad, err := VMRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 || len(bad) != 1 {
		t.Fatalf("records %v, bad %v; want the broken one reported as bad", records, bad)
	}
}
