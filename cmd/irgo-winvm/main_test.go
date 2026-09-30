package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestHelpIsNotAnError: -h asks a question, so it exits 0 on every command.
//
// Negative control: dropping the `!errors.Is(err, flag.ErrHelp)` guard in run
// fails this for every command that parses flags.
func TestHelpIsNotAnError(t *testing.T) {
	for _, c := range commands {
		t.Run(c.Name, func(t *testing.T) {
			if err := run([]string{c.Name, "-h"}); err != nil {
				t.Errorf("%s -h returned %v, want nil", c.Name, err)
			}
		})
	}
}

// TestRealErrorsStillPropagate is the other half: the help guard must not
// swallow real errors. It uses app-create with no binary, which fails in its
// own argument check before looking for a VM. An unknown subcommand would not
// do: that returns before the guard is reached.
//
// Negative control: inverting the guard to swallow everything that is not a
// help request fails this.
func TestRealErrorsStillPropagate(t *testing.T) {
	err := run([]string{"app-create"})
	if err == nil {
		t.Fatal("app-create with no binary returned nil, want an error")
	}
	if errors.Is(err, flag.ErrHelp) {
		t.Errorf("a usage error was reported as a help request: %v", err)
	}
	if !errors.Is(err, errUsage) {
		t.Errorf("app-create with no binary is not classified as a usage error: %v", err)
	}
	if !strings.Contains(err.Error(), "app-create") {
		t.Errorf("error does not name what was wrong: %v", err)
	}
}

func TestUnknownSubcommandIsAnError(t *testing.T) {
	err := run([]string{"no-such-command"})
	if err == nil {
		t.Fatal("an unknown subcommand returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "no-such-command") {
		t.Errorf("error does not name what was wrong: %v", err)
	}
}

// TestEveryCommandIsReachable checks that find, which run uses, resolves every
// command in the table, and that -h and --help mean help.
func TestEveryCommandIsReachable(t *testing.T) {
	if len(commands) != len(command.All) {
		t.Fatalf("%d commands in the table, %d declared", len(commands), len(command.All))
	}
	for _, c := range commands {
		got, ok := find(c.Name)
		if !ok || got.Name != c.Name {
			t.Errorf("find(%q) = %q, %v", c.Name, got.Name, ok)
		}
		if c.run == nil {
			t.Errorf("%s has no run func", c.Name)
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

// TestJoinRejectsMismatch checks both directions of the check init relies on
// to pair package command's list with this package's implementations.
//
// Negative control: disabling join's second loop fails the undeclared case.
func TestJoinRejectsMismatch(t *testing.T) {
	noop := impl{run: func(values, []string) error { return nil }}
	all := []command.Command{{Name: "iso-create"}, {Name: "vm-create"}}

	if _, err := join(all, map[string]impl{"iso-create": noop, "vm-create": noop}); err != nil {
		t.Fatalf("a matching table was rejected: %v", err)
	}
	if _, err := join(all, map[string]impl{"iso-create": noop}); err == nil {
		t.Error("a declared command with no implementation was accepted")
	}
	if _, err := join(all, map[string]impl{"iso-create": noop, "vm-create": noop, "iso-crate": noop}); err == nil {
		t.Error("an implementation for an undeclared command was accepted")
	}
	if _, err := join(all, map[string]impl{"iso-create": noop, "vm-create": {}}); err == nil {
		t.Error("an implementation with no run func was accepted")
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	got := command.UsageText()
	for _, c := range commands {
		if !strings.Contains(got, c.Name) {
			t.Errorf("usage does not mention %q", c.Name)
		}
		if c.Undo == "" {
			continue
		}
		if _, ok := find(c.Undo); !ok {
			t.Errorf("%s names %q as its undo, which is not a command", c.Name, c.Undo)
		}
	}
}

// TestExitCode covers the classification, one case per code, with wrapped
// errors because that is what call sites produce.
//
// Negative control: moving the ErrNoVM case after the default fails the no-VM
// case; deleting the ErrNoAgent case fails that one.
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
		{"another mutation holds the lock", fmt.Errorf("%w: someone else", utmvm.ErrMutationInProgress), command.CodeBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := exitCode(tc.err)
			if got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
			// Every code returned must be declared, or an MCP client sees "unknown".
			if _, ok := command.Classify(got); !ok {
				t.Errorf("exitCode(%v) returned %d, which package command does not declare", tc.err, got)
			}
		})
	}
}

// TestExitCodesAreDistinct: two failures sharing a code cannot be told apart.
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
}

// TestFlagDefaultsRoundTrip: the MCP schema advertises each flag's DefValue,
// so passing every default back must parse and give the same value.
func TestFlagDefaultsRoundTrip(t *testing.T) {
	withFlags := 0
	for _, c := range commands {
		if c.flags == nil {
			continue
		}
		withFlags++
		var argv []string
		c.flags().VisitAll(func(f *flag.Flag) {
			argv = append(argv, fmt.Sprintf("-%s=%s", f.Name, f.DefValue))
		})
		fresh := c.flags()
		fresh.SetOutput(io.Discard)
		if err := fresh.Parse(argv); err != nil {
			t.Errorf("%s: the defaults the schema advertises do not parse back: %v", c.Name, err)
			continue
		}
		fresh.VisitAll(func(f *flag.Flag) {
			if got := f.Value.String(); got != f.DefValue {
				t.Errorf("%s -%s: passing its own default %q back gave %q", c.Name, f.Name, f.DefValue, got)
			}
		})
	}
	if withFlags == 0 {
		t.Fatal("no command declares flags; this test would pass vacuously")
	}
}

// TestFlagSetsAreNamedForTheirCommand: "Usage of <name>:" in -h output, and the
// site's reference page, read the set's name.
func TestFlagSetsAreNamedForTheirCommand(t *testing.T) {
	for _, c := range commands {
		if c.flags != nil && c.flags().Name() != c.Name {
			t.Errorf("%s declares a flag set named %q", c.Name, c.flags().Name())
		}
	}
}

// TestMCPHTTPRefusesANonLoopbackBind goes through the real CLI path. It reads
// -allow-remote, so if mcpFlags stops declaring it, values.Bool panics here
// rather than in a running server.
func TestMCPHTTPRefusesANonLoopbackBind(t *testing.T) {
	err := runTool("mcp", []string{"-http", "0.0.0.0:8129"})
	if err == nil {
		t.Fatal("mcp -http accepted a non-loopback address without -allow-remote")
	}
	if !strings.Contains(err.Error(), "THREAT-MODEL") {
		t.Errorf("the refusal does not point at the threat model: %v", err)
	}
}
