package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
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
