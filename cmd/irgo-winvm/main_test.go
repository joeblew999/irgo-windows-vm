package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/utmvm"

	"github.com/joeblew999/irgo-windows-vm/command"
)

// TestHelpIsNotAnError covers the whole point of swallowing flag.ErrHelp: -h
// asks a question, and answering it is not a failure.
//
// Four commands used flag.ContinueOnError and returned ErrHelp straight out, so
// `irgo-winvm vm-create -h` printed the flags and then `error: flag: help
// requested` and exited 1. The other three used flag.ExitOnError and exited 0.
// One question, two answers, decided by which command you happened to ask.
//
// Every command is covered by reading the table rather than a list written out
// here, so a command added later cannot quietly skip this.
//
// Negative control, run by hand when this was written: dropping the
// `!errors.Is(err, flag.ErrHelp)` guard in run() fails this for all seven
// commands that parse flags.
func TestHelpIsNotAnError(t *testing.T) {
	for _, c := range commands {
		t.Run(c.Name, func(t *testing.T) {
			if err := run([]string{c.Name, "-h"}); err != nil {
				t.Errorf("%s -h returned %v, want nil", c.Name, err)
			}
		})
	}
}

// TestRealErrorsStillPropagate is the other half, and the reason the guard
// tests errors.Is rather than "did anything come back".
//
// Swallowing every error would make -h pass and make every genuine failure
// exit 0 with it. A tool that reports success when the VM is missing is worse
// than one that is rude about help.
//
// It goes through app-create with no binary named, NOT through an unknown
// subcommand. The first version of this test used an unknown subcommand and
// PASSED against a mutation that swallowed every real error — because that path
// returns before the guard is ever reached, so the test never touched the code
// it was written to protect. A test that cannot fail is not a test.
//
// app-create with no positional argument fails in its own flag handling, before
// it looks for a VM, so this needs no UTM and no guest.
//
// Negative control, run by hand: inverting the guard to
// `errors.Is(err, flag.ErrHelp)` — swallowing everything that is not a help
// request — fails this.
func TestRealErrorsStillPropagate(t *testing.T) {
	err := run([]string{"app-create"})
	if err == nil {
		t.Fatal("app-create with no binary returned nil, want an error")
	}
	if errors.Is(err, flag.ErrHelp) {
		t.Errorf("a usage error was reported as a help request: %v", err)
	}
	if !strings.Contains(err.Error(), "app-create") {
		t.Errorf("error does not name what was wrong: %v", err)
	}
}

// TestUnknownSubcommandIsAnError covers the other rejection path, which returns
// before dispatch and so is not covered by the test above.
func TestUnknownSubcommandIsAnError(t *testing.T) {
	err := run([]string{"no-such-command"})
	if err == nil {
		t.Fatal("an unknown subcommand returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "no-such-command") {
		t.Errorf("error does not name what was wrong: %v", err)
	}
}

// TestEveryCommandIsReachable asserts the table and the dispatcher agree.
//
// find() is what run() uses, so a table entry it cannot resolve is a command
// that is listed in the usage and then reports "unknown subcommand".
func TestEveryCommandIsReachable(t *testing.T) {
	for _, c := range commands {
		got, ok := find(c.Name)
		if !ok {
			t.Errorf("%s is in the table but find() does not resolve it", c.Name)
			continue
		}
		if got.Name != c.Name {
			t.Errorf("find(%q) resolved to %q", c.Name, got.Name)
		}
		if c.Run == nil {
			t.Errorf("%s has no handler", c.Name)
		}
		if c.Summary == "" {
			t.Errorf("%s has no summary, so the usage would print a blank row", c.Name)
		}
	}
	for _, alias := range []string{"-h", "--help"} {
		if got, ok := find(alias); !ok || got.Name != "help" {
			t.Errorf("find(%q) = %q, %v; want help", alias, got.Name, ok)
		}
	}
}

// TestUsageListsEveryCommand is what keeps the generated usage honest.
//
// The usage used to be a hand-typed const beside the dispatch switch, which is
// how it came to promise "Each takes -h for its flags" while version and doctor
// ignored flags entirely.
func TestUsageListsEveryCommand(t *testing.T) {
	got := usageText()
	for _, c := range commands {
		if !strings.Contains(got, c.Name) {
			t.Errorf("usage does not mention %q", c.Name)
		}
	}
	// Each undo is printed beside the command it reverses, not on its own row.
	for _, c := range commands {
		if c.Undo == "" {
			continue
		}
		if _, ok := find(c.Undo); !ok {
			t.Errorf("%s names %q as its undo, which is not a command", c.Name, c.Undo)
		}
	}
}

// TestExitCode covers the classification, one case per code.
//
// Every failure used to exit 1, so a script could not tell "the program I asked
// you to run failed" from "that VM does not exist" from "the agent is busy" —
// and the last is the one worth retrying. It matters here more than in most
// tools because utmctl itself exits 0 on failure, so this CLI is the only
// honest signal a caller gets.
//
// Wrapped errors, not bare sentinels, because that is what the call sites
// produce and errors.Is has to see through fmt.Errorf's %w.
//
// Negative control, run by hand: reordering the switch so the ErrNoVM case sits
// after the default fails the no-VM case; deleting the ErrNoAgent case makes it
// return 1 and fails that one.
func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want command.Code
	}{
		{"nil is success", nil, command.CodeOK},
		{"help is not a failure", fmt.Errorf("parsing: %w", flag.ErrHelp), command.CodeOK},
		{"an unclassified error is the guest's", errors.New("boom"), command.CodeFailed},
		{"the guest program failed", fmt.Errorf("probe.exe exited 3 in the guest"), command.CodeFailed},
		{"called wrongly", fmt.Errorf("%w: needs a binary", errUsage), command.CodeUsage},
		{"no such VM", fmt.Errorf("%w: %q", utmvm.ErrNoVM, "nope"), command.CodeNoVM},
		{"agent not answering", fmt.Errorf("%w: busy", utmvm.ErrNoAgent), command.CodeNoAgent},
		{"refused without -force", fmt.Errorf("would delete things (%w)", errRefused), command.CodeNeedForce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestExitCodesAreDistinct is the property that makes the contract worth
// having: two different failures must not share a code, or a caller still
// cannot tell them apart.
//
// It walks command.Outcomes rather than listing the codes here. The list used
// to be written out in this test, which meant a seventh code could be added and
// this would go on checking the six it knew about — a test that cannot fail for
// the case it was written to catch.
func TestExitCodesAreDistinct(t *testing.T) {
	if len(command.Outcomes) == 0 {
		t.Fatal("no outcomes declared; this test would pass vacuously")
	}
	seen := map[command.Code]string{}
	for _, o := range command.Outcomes {
		if prev, dup := seen[o.Code]; dup {
			t.Errorf("%s and %s both exit %d", prev, o.Name, o.Code)
		}
		seen[o.Code] = o.Name
	}
	t.Logf("%d distinct outcomes", len(seen))
}

// TestEveryCodeExitCodeReturnsIsDeclared closes the gap the move opened.
//
// exitCode maps this program's errors to codes; command.Outcomes describes what
// each code means to an agent. A code returned here but not declared there
// reaches an MCP client as "unknown", which is worse than the old situation,
// where at least the number was the whole contract.
func TestEveryCodeExitCodeReturnsIsDeclared(t *testing.T) {
	for _, err := range []error{
		nil,
		fmt.Errorf("parsing: %w", flag.ErrHelp),
		errors.New("boom"),
		fmt.Errorf("%w: needs a binary", errUsage),
		fmt.Errorf("%w: %q", utmvm.ErrNoVM, "nope"),
		fmt.Errorf("%w: busy", utmvm.ErrNoAgent),
		fmt.Errorf("would delete things (%w)", errRefused),
	} {
		code := exitCode(err)
		if _, ok := command.Classify(code); !ok {
			t.Errorf("exitCode(%v) returned %d, which package command does not declare", err, code)
		}
	}
}

// TestEveryHandlerIsADeclaredCommand is the direction the split created.
//
// The list of commands lives in package command so the MCP server can import
// it; the functions that run them stay here. That is one list and one wiring
// map, and the map is keyed by strings — so a typo, or a handler for a command
// that was renamed, silently wires nothing.
//
// The other direction is already covered: a declared command with no handler
// leaves Run nil, and TestTableIsWellFormed fails on that. This is the half
// that would otherwise be invisible, because an unused map entry compiles,
// vets, lints and does nothing.
//
// Negative control, run by hand: adding "iso-crate": runISOCreate to handlers
// fails this and names iso-crate.
func TestEveryHandlerIsADeclaredCommand(t *testing.T) {
	if len(handlers) == 0 {
		t.Fatal("no handlers; this test would pass vacuously")
	}
	for name := range handlers {
		if _, ok := command.Find(name); !ok {
			t.Errorf("handlers has %q, which package command does not declare — "+
				"it is wired to nothing and can never run", name)
		}
	}
	// Sizes agree, so neither list can carry an entry the loop above misses.
	if len(handlers) != len(command.All) {
		t.Errorf("%d handlers against %d declared commands", len(handlers), len(command.All))
	}
	t.Logf("%d handlers, all declared", len(handlers))
}
