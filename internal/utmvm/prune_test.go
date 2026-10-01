package utmvm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSelectByBounds: older than the age goes, past the size bound counting
// from the newest goes, and the newest of each group stays whatever its age
// or size.
//
// Negative controls, run by hand: drop the `newest` case, and "the newest of
// each stage is kept" fails; count total before sorting newest first, and
// "size counts from the newest" fails (run 1 Oct 2026: both failed).
func TestSelectByBounds(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	f := func(name, group string, ago time.Duration, mib int64) pruneFile {
		return pruneFile{path: name, group: group, mod: now.Add(-ago), bytes: mib << 20}
	}
	names := func(items []PruneItem) string {
		var s []string
		for _, it := range items {
			s = append(s, it.Path)
		}
		return strings.Join(s, ",")
	}

	old := selectByBounds("shots", []pruneFile{
		f("ready-new", "ready", 1*day, 1), f("ready-old", "ready", 20*day, 1),
		f("booting-ancient", "booting", 90*day, 1), // the only booting shot: kept
	}, 14*day, 0, now)
	if got := names(old); got != "ready-old" {
		t.Errorf("by age: removed %q, want ready-old (the newest of each stage is kept)", got)
	}

	big := selectByBounds("shots", []pruneFile{
		f("a", "a", 1*time.Hour, 60), f("b", "b", 2*time.Hour, 60),
		f("a2", "a", 3*time.Hour, 60), f("b2", "b", 4*time.Hour, 60),
	}, 0, 150<<20, now)
	if got := names(big); got != "a2,b2" {
		t.Errorf("size counts from the newest: removed %q, want a2,b2 (a and b are the newest of their stage; a2 brings it to 180 MiB of 150)", got)
	}

	if got := selectByBounds("x", nil, day, 1, now); len(got) != 0 {
		t.Errorf("nothing to prune: %v", got)
	}
	if got := selectByBounds("x", []pruneFile{f("1", "1", 99*day, 999), f("2", "2", 99*day, 999)}, 0, 0, now); len(got) != 0 {
		t.Errorf("no bounds removed %v", names(got))
	}
}

// TestPrunePlanAndPrune: on a runtime directory made for the test, prune
// lists exactly what is past its bound, removes it only with force, frees
// what it said, keeps an item whose lock is held, and leaves everything else.
//
// Negative control, run by hand: ignore the Acquire error and remove anyway,
// and the busy caller's binary goes (run 1 Oct 2026: it failed).
func TestPrunePlanAndPrune(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	write := func(p string, mod time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	shots, logs, bin := ShotDir(), LogDir(), stageRoot()
	write(filepath.Join(shots, "a1-20260801-100000-ready.png"), old)
	write(filepath.Join(shots, "a1-20260802-100000-ready.png"), old.Add(time.Hour)) // newest ready: kept
	write(filepath.Join(shots, "a1-20260930-100000-booting-1.png"), now)
	write(filepath.Join(logs, "irgo-winvm.log"), old) // the command log: never
	write(filepath.Join(logs, "glaze-windows-20260801-100000.log"), old)
	write(filepath.Join(logs, "glaze-windows-20260930-100000.log"), now)
	write(filepath.Join(bin, "agent-a", "aaaa.exe"), old)
	write(filepath.Join(bin, "agent-b", "bbbb.exe"), old)
	write(filepath.Join(bin, "agent-b", "cccc.exe"), now)
	write(filepath.Join(bin, "legacy.exe"), old) // the owner's to clear
	write(filepath.Join(GoldenPullDir(), ".parts", "x.zst"), old)
	if err := os.Chtimes(filepath.Join(GoldenPullDir(), ".parts"), old, old); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		filepath.Join(shots, "a1-20260801-100000-ready.png"):     true,
		filepath.Join(logs, "glaze-windows-20260801-100000.log"): true,
		filepath.Join(bin, "agent-a", "aaaa.exe"):                true,
		filepath.Join(bin, "agent-b", "bbbb.exe"):                true,
		filepath.Join(GoldenPullDir(), ".parts"):                 true,
	}
	plan := PrunePlan(DefaultPrunePolicy, now)
	got := map[string]bool{}
	for _, it := range plan {
		got[it.Path] = true
		if !want[it.Path] {
			t.Errorf("selected %s (%s), which is within its bounds", it.Path, it.Why)
		}
	}
	for p := range want {
		if !got[p] {
			t.Errorf("did not select %s", p)
		}
	}

	items, freed := Prune(DefaultPrunePolicy, false, now)
	if freed != 0 {
		t.Fatalf("a dry run freed %d", freed)
	}
	for _, it := range items {
		if _, err := os.Stat(it.Path); err != nil {
			t.Fatalf("a dry run removed %s", it.Path)
		}
	}

	busy := runtime.GOOS == "darwin" // flock: elsewhere there is nothing to hold
	if busy {
		release, err := Acquire(StageLockFor("agent-b"))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	items, freed = Prune(DefaultPrunePolicy, true, now)
	var sum int64
	for _, it := range items {
		_, err := os.Stat(it.Path)
		heldBack := busy && strings.Contains(it.Path, "agent-b")
		switch {
		case heldBack && (it.Done || err != nil):
			t.Errorf("%s was removed while its caller's stage lock was held", it.Path)
		case heldBack && it.Err == "":
			t.Errorf("%s was kept without saying why", it.Path)
		case !heldBack && (!it.Done || err == nil):
			t.Errorf("%s: done=%v err=%q, still there: %v", it.Path, it.Done, it.Err, err == nil)
		}
		if it.Done {
			sum += it.Bytes
		}
	}
	if freed != sum {
		t.Errorf("freed %d, the removed items add up to %d", freed, sum)
	}
	for _, keep := range []string{
		filepath.Join(shots, "a1-20260802-100000-ready.png"), filepath.Join(logs, "irgo-winvm.log"),
		filepath.Join(bin, "agent-b", "cccc.exe"), filepath.Join(bin, "legacy.exe"),
	} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed", keep)
		}
	}
}
