package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestRepairChecked: the suite runs before and after the repair, in that
// order, and the report names what the repair fixed and what broke; a repair
// that failed is still checked afterwards, and its error wins.
//
// Negative control, run by hand: swapping before and after in the Changed
// call reports TestHibernationOff as broken instead of fixed.
func TestRepairChecked(t *testing.T) {
	sec := func(results ...glazecheck.Result) glazecheck.Section { return glazecheck.Section{Results: results} }
	runs := []glazecheck.Section{
		sec(glazecheck.Result{Name: "TestHibernationOff", Outcome: glazecheck.Fail}, glazecheck.Result{Name: "TestA", Outcome: glazecheck.Pass}),
		sec(glazecheck.Result{Name: "TestHibernationOff", Outcome: glazecheck.Pass}, glazecheck.Result{Name: "TestA", Outcome: glazecheck.Fail}),
	}
	for _, repairErr := range []error{nil, errors.New("repair script exited 1")} {
		var order []string
		var said []string
		n := 0
		check := func(string, utmvm.Entry, func(string, ...any)) (glazecheck.Section, error) {
			order = append(order, "check")
			s := runs[n]
			n++
			return s, nil
		}
		repair := func() error { order = append(order, "repair"); return repairErr }
		err := repairChecked("/root", utmvm.Entry{Name: "vc1"}, repair, check, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
		if strings.Join(order, " ") != "check repair check" {
			t.Errorf("order %v", order)
		}
		out := strings.Join(said, "\n")
		if !strings.Contains(out, "fixed by the repair: TestHibernationOff") || !strings.Contains(out, "broken since the repair: TestA") {
			t.Errorf("report:\n%s", out)
		}
		if !errors.Is(err, repairErr) {
			t.Errorf("err %v, want %v", err, repairErr)
		}
	}
}
