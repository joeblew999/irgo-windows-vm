//go:build darwin

package main

import (
	"errors"
	"flag"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// The mutation-lock wrapper tests run only on macOS, because the lock itself is
// a flock and does not exist off macOS — there is nothing to serialise where
// UTM cannot run.

// TestRunToolRefusesMutationWhileLockHeld is the wrapper the whole feature
// hangs on: a mutating command is refused before its own work starts, and help
// is not a mutation.
func TestRunToolRefusesMutationWhileLockHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := utmvm.Acquire(utmvm.VMLock(utmvm.DefaultVMName))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := runTool("vm-create", nil); !errors.Is(err, utmvm.ErrMutationInProgress) {
		t.Fatalf("runTool(vm-create) = %v, want ErrMutationInProgress", err)
	}

	// -h must be answered before the lock, or asking for help while another
	// mutation runs would be refused as "busy".
	if err := runTool("vm-create", []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("runTool(vm-create -h) = %v, want ErrHelp", err)
	}
}

// TestRunToolLetsAnotherVMThrough is what several agents on one Mac need:
// while one VM is busy, a command on a different VM gets past the lock.
//
// app-create with no binary is used because it stops at its usage check,
// right after the lock and before anything touches UTM: errUsage proves it got
// through, ErrMutationInProgress proves it did not.
//
// Negative control, run by hand: make locksFor return utmvm.MachineLock for
// LockVM and the -vm b call is refused.
func TestRunToolLetsAnotherVMThrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := utmvm.Acquire(utmvm.VMLock("a"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := runTool("app-create", []string{"-vm", "b"}); !errors.Is(err, errUsage) {
		t.Fatalf("runTool(app-create -vm b) with VM a busy = %v, want it past the lock (errUsage)", err)
	}
	if err := runTool("app-create", []string{"-vm=A"}); !errors.Is(err, utmvm.ErrMutationInProgress) {
		t.Fatalf("runTool(app-create -vm=A) with VM a busy = %v, want ErrMutationInProgress", err)
	}
}

// TestLocksForReadsTheVMFlag: every spelling of -vm, and its default, lands on
// one lock, and the declared scope decides which kinds are taken.
func TestLocksForReadsTheVMFlag(t *testing.T) {
	appCreate, _ := command.Find("app-create")
	vmCreate, _ := command.Find("vm-create")
	isoDelete, _ := command.Find("iso-delete")
	appDelete, _ := command.Find("app-delete")

	one := func(c command.Command, args ...string) utmvm.Lock {
		t.Helper()
		l := locksFor(c, args)
		if len(l) != 1 {
			t.Fatalf("locksFor(%s %v) = %v, want one lock", c.Name, args, l)
		}
		return l[0]
	}
	if a, b := one(appCreate, "-vm", "a1", "x.exe"), one(vmCreate, "-vm=A1"); a != b {
		t.Errorf("-vm a1 and -vm=A1 took %q and %q", a, b)
	}
	if got := one(vmCreate); got != utmvm.VMLock(utmvm.DefaultVMName) {
		t.Errorf("no -vm took %q, want the default VM's lock", got)
	}
	if got := one(isoDelete, "-force"); got != utmvm.MachineLock {
		t.Errorf("iso-delete took %q, want the machine lock", got)
	}
	if got := locksFor(appDelete, []string{"-vm", "a1"}); len(got) != 2 {
		t.Errorf("app-delete took %v; it clears the stage and cleans a VM, so both", got)
	}
}

// TestRunToolReadOnlyCommandsSkipTheLock: reporting is not a mutation, so it
// must keep working while another mutation holds the lock.
func TestRunToolReadOnlyCommandsSkipTheLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := utmvm.Acquire(utmvm.MachineLock, utmvm.VMLock(utmvm.DefaultVMName))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := runTool("doctor", nil); err != nil {
		t.Fatalf("runTool(doctor) = %v, want nil while the lock is held", err)
	}
}
