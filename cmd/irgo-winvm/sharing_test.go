package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// Several callers sharing one Mac: the owner's VM is not anybody else's
// default, and a job runs as the caller that started it.

// TestNonOwnersAreRefusedTheDefaultVM: an MCP client, or anyone who named
// themselves, who leaves -vm out is refused with exit 2 before any lock or
// UTM call; the owner at a terminal is not, and naming the VM is allowed.
//
// app-create with no binary stops at its usage check once it is past
// admission (and its lock, which is free in an empty HOME), so errUsage
// proves it was admitted.
//
// Negative control, run by hand: remove the admit call from runToolFor and
// the refused cases reach errUsage instead.
func TestNonOwnersAreRefusedTheDefaultVM(t *testing.T) {
	// The rule guards the Mac's VMs. Elsewhere app-create and vm-screen are
	// refused before it, as macOS-only (pointing at remote-submit).
	if runtime.GOOS != "darwin" {
		t.Skip("app-create and vm-screen are macOS-only; on " + runtime.GOOS + " they are refused before the sharing rule")
	}
	t.Setenv("HOME", t.TempDir())

	t.Setenv(utmvm.OwnerEnv, "")
	if err := runTool("app-create", nil); !errors.Is(err, errUsage) || errors.Is(err, utmvm.ErrDefaultVMReserved) {
		t.Fatalf("the owner, no -vm: %v, want admitted (errUsage)", err)
	}
	err := runToolFor("claude-code", "app-create", nil)
	if !errors.Is(err, utmvm.ErrDefaultVMReserved) {
		t.Fatalf("an MCP client, no -vm: %v, want ErrDefaultVMReserved", err)
	}
	if exitCode(err) != command.CodeUsage {
		t.Fatalf("the refusal exits %d, want %d", exitCode(err), command.CodeUsage)
	}
	if err := runToolFor("claude-code", "app-create", []string{"-vm", utmvm.DefaultVMName}); !errors.Is(err, errUsage) {
		t.Fatalf("an MCP client naming %s: %v, want admitted", utmvm.DefaultVMName, err)
	}
	if err := runToolFor("claude-code", "vm-screen", nil); !errors.Is(err, utmvm.ErrDefaultVMReserved) {
		t.Fatalf("an MCP client photographing the default VM: %v, want refused", err)
	}

	t.Setenv(utmvm.OwnerEnv, "agent-from-elsewhere")
	if err := runTool("app-create", nil); !errors.Is(err, utmvm.ErrDefaultVMReserved) {
		t.Fatalf("IRGO_WINVM_OWNER set, no -vm: %v, want refused", err)
	}
	if err := runTool("app-create", []string{"-vm", "z1"}); !errors.Is(err, errUsage) {
		t.Fatalf("IRGO_WINVM_OWNER set, its own VM: %v, want admitted", err)
	}
	// glaze-check on the Mac touches no VM, so it is no business of the rule.
	c, _ := find("glaze-check")
	v, _, perr := c.parse(nil)
	if perr != nil {
		t.Fatal(perr)
	}
	v.caller = utmvm.Caller{ID: "agent", Source: utmvm.SourceEnv}
	if err := admit(c, v); err != nil {
		t.Fatalf("glaze-check without -windows was refused: %v", err)
	}
}

// TestJobsRunAsTheirCaller: a job child is a new process with no MCP client,
// so the caller is passed as -owner, unless the call named one already, and
// not to a command that has no -owner.
//
// Negative control, run by hand: return args unchanged from jobArgs and the
// first case fails.
func TestJobsRunAsTheirCaller(t *testing.T) {
	parse := func(name string, args ...string) values {
		t.Helper()
		c, _ := find(name)
		v, _, err := c.parse(args)
		if err != nil {
			t.Fatal(err)
		}
		v.caller = utmvm.Caller{ID: "claude-code/me@mac:other", Source: utmvm.SourceMCP}
		return v
	}
	if got := jobArgs(parse("vm-create", "-vm", "z1", "-install"), []string{"-vm", "z1", "-install"}); strings.Join(got, " ") != "-owner=claude-code/me@mac:other -vm z1 -install" {
		t.Errorf("vm-create job args = %q", got)
	}
	if got := jobArgs(parse("vm-create", "-owner", "ci", "-vm", "z1"), []string{"-owner", "ci", "-vm", "z1"}); strings.Join(got, " ") != "-owner ci -vm z1" {
		t.Errorf("an explicit -owner was overridden: %q", got)
	}
	if got := jobArgs(parse("iso-create", "-fetch"), []string{"-fetch"}); strings.Join(got, " ") != "-fetch" {
		t.Errorf("iso-create, which has no -owner, got %q", got)
	}
}
