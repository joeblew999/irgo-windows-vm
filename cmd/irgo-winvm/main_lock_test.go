//go:build darwin

package main

import (
	"errors"
	"flag"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// macOS only: the mutation lock is a flock that exists only where UTM runs.

// TestRunToolRefusesMutationWhileLockHeld: a mutating command is refused
// before its own work starts, and help is not a mutation.
func TestRunToolRefusesMutationWhileLockHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := utmvm.AcquireMutation()
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

// TestRunToolReadOnlyCommandsSkipTheLock: reporting is not a mutation, so it
// must keep working while another mutation holds the lock.
func TestRunToolReadOnlyCommandsSkipTheLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := utmvm.AcquireMutation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := runTool("doctor", nil); err != nil {
		t.Fatalf("runTool(doctor) = %v, want nil while the lock is held", err)
	}
}
