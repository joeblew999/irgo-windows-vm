package command

import (
	"strings"
	"testing"
)

// TestSealingIsAlwaysAJob: vm-golden-create has no quick form, so over MCP it
// must start a job whatever it is given; blocking would be abandoned by the
// client while the seal carries on.
//
// Negative control, run by hand: give vm-golden-create Detach "-vm" and the
// no-argument case fails.
func TestSealingIsAlwaysAJob(t *testing.T) {
	c, ok := Find("vm-golden-create")
	if !ok {
		t.Fatal("vm-golden-create is not declared")
	}
	for _, args := range [][]string{nil, {"-vm", "g1"}, {"-h"}} {
		if !c.DetachedBy(args) {
			t.Errorf("vm-golden-create %v runs inline; it is many minutes at best", args)
		}
	}
	// And DetachAlways is not a flag anybody can pass to another command.
	v, _ := Find("vm-create")
	if v.DetachedBy([]string{DetachAlways}) {
		t.Error("vm-create detached on the DetachAlways marker")
	}
}

// TestUsageColumnsFitTheLongestName: a name longer than the column pushed its
// own row out of line, which is how the golden commands first printed.
func TestUsageColumnsFitTheLongestName(t *testing.T) {
	var undo []int
	for _, line := range strings.Split(UsageText(), "\n") {
		for _, c := range All {
			if c.Undo != "" && strings.HasPrefix(line, "  "+c.Name+" ") {
				undo = append(undo, strings.LastIndex(line, c.Undo))
			}
		}
	}
	if len(undo) == 0 {
		t.Fatal("no MAKE rows found in the usage")
	}
	for _, col := range undo[1:] {
		if col != undo[0] {
			t.Fatalf("the UNDO column starts at %v, not in one place:\n%s", undo, UsageText())
		}
	}
}
