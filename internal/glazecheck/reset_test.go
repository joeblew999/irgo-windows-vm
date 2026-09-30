package glazecheck

import (
	"errors"
	"reflect"
	"testing"
)

// The reset after the programs is the one that names what a program left open,
// so it must run even when the one before failed, and its error must win.
func TestResetAroundRunsBothAndPrefersTheLastError(t *testing.T) {
	quiet := func(string, ...any) {}
	errBefore, errAfter := errors.New("before"), errors.New("after")

	for _, c := range []struct {
		name    string
		results []error // what each reset call returns, in order
		want    error
	}{
		{"both clean", []error{nil, nil}, nil},
		{"before failed", []error{errBefore, nil}, errBefore},
		{"after failed", []error{nil, errAfter}, errAfter},
		{"both failed: the after wins", []error{errBefore, errAfter}, errAfter},
	} {
		var calls []string
		n := 0
		reset := func() error {
			calls = append(calls, "reset")
			err := c.results[n]
			n++
			return err
		}
		got := resetAround(reset, quiet, func() { calls = append(calls, "run") })
		if !errors.Is(got, c.want) || (c.want == nil && got != nil) {
			t.Errorf("%s: resetAround = %v, want %v", c.name, got, c.want)
		}
		if want := []string{"reset", "run", "reset"}; !reflect.DeepEqual(calls, want) {
			t.Errorf("%s: calls = %v, want %v", c.name, calls, want)
		}
	}

	// No reset (the Mac): the programs still run, once.
	runs := 0
	if err := resetAround(nil, quiet, func() { runs++ }); err != nil || runs != 1 {
		t.Errorf("without a reset: err %v, %d runs; want nil and 1", err, runs)
	}
}
