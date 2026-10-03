package utmvm

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestStartKeepingNeverRestartsUTM: the keeper's start, given a UTM that
// does not answer with every VM stopped (where StartWithDisplay restarts
// it), fails and names the hand restart: it never quits UTM, never takes the
// restart lock, and never asks for the list.
//
// Negative control, run by hand: drop the u.restart == nil case in
// startWithDisplay, and the start panics calling a nil restart after the
// lock and the list.
func TestStartKeepingNeverRestartsUTM(t *testing.T) {
	f := &fakeUTM{starts: []string{sayTimedOut}, entries: stopped("a")}
	s := f.starter()
	s.restart = nil
	err := s.startWithDisplay("a", f.say)
	if !errors.Is(err, errUTMNotAnswering) || !strings.Contains(err.Error(), "not restarting UTM") {
		t.Fatalf("err %v, want errUTMNotAnswering and that UTM was not restarted", err)
	}
	if f.did() != "start a" {
		t.Fatalf("did %q, want the one start and nothing else", f.did())
	}
}

// TestSetKeepRunning: the mark is written and read back, cleared, and
// clearing it again is success; a VM with no record, and a golden image,
// are refused.
//
// Negative control, run by hand: return nil from the !ok branch for keep
// too, and the VM with no record is "marked" with nothing written.
func TestSetKeepRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := writeRecord(VMRecord{Name: "a1", Owner: "claude-rig", Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if changed, err := SetKeepRunning("a1", true); err != nil || !changed {
		t.Fatalf("mark: changed %v, err %v", changed, err)
	}
	if r, _, _ := readRecord("a1"); !r.KeepRunning || r.Owner != "claude-rig" {
		t.Fatalf("record after marking: %+v", r)
	}
	if changed, err := SetKeepRunning("a1", true); err != nil || changed {
		t.Fatalf("mark again: changed %v, err %v; want no change", changed, err)
	}
	for range 2 {
		if _, err := SetKeepRunning("a1", false); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}
	if r, _, _ := readRecord("a1"); r.KeepRunning {
		t.Fatal("still marked after clearing")
	}
	if _, err := SetKeepRunning("irgo-win11", true); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("a VM with no record: %v, want ErrNoRecord", err)
	}
	if _, err := SetKeepRunning("irgo-win11", false); err != nil {
		t.Fatalf("clearing a VM with no record: %v, want success", err)
	}
	if _, err := SetKeepRunning(GoldenVMName, true); err == nil {
		t.Fatal("the golden image was marked keep-running")
	}
}
