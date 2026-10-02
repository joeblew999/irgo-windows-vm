package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestUndoTellsNoVMFromNoAnswer: UTM saying there is no such VM is an undo
// with nothing to do, exit 0; UTM not answering is an error that is neither 0
// nor "no such VM", for vm-delete, app-delete and vm-ssh-delete.
//
// Negative control, run by hand: return (Entry{}, false, nil) for every error
// in findForUndo, which is what both commands did before, and the
// unanswerable cases exit 0 (run 1 Oct 2026: all three failed; 2 Oct 2026,
// with vm-ssh-delete: all four).
func TestUndoTellsNoVMFromNoAnswer(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("vm-delete resolves the bundle in UTM's container first, which exists only on macOS")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("IRGO_WINVM_OWNER", "")
	defer func(f func(string) (utmvm.Entry, error)) { findVM = f }(findVM)

	absent := func(string) (utmvm.Entry, error) { return utmvm.Entry{}, utmvm.ErrNoVM }
	broken := func(string) (utmvm.Entry, error) {
		return utmvm.Entry{}, errors.New("utmctl list: exit status 1")
	}
	for _, cmdline := range [][]string{
		{"vm-delete", "-vm", "z9"},
		{"vm-delete", "-vm", "z9", "-force"},
		{"app-delete", "-vm", "z9"},
		{"vm-ssh-delete", "-vm", "z9"},
	} {
		name := strings.Join(cmdline, " ")
		findVM = absent
		if _, err := utmvm.Capture(func() error { return runTool(cmdline[0], cmdline[1:]) }); err != nil {
			t.Errorf("%s with UTM saying no such VM: %v, want nil", name, err)
		}
		findVM = broken
		_, err := utmvm.Capture(func() error { return runTool(cmdline[0], cmdline[1:]) })
		if code := exitCode(err); code == command.CodeOK || code == command.CodeNoVM {
			t.Errorf("%s with UTM not answering exits %d (%v), want a failure", name, code, err)
		}
	}
}
