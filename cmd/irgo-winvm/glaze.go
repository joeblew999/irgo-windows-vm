package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// repo finds the checkout. Not being in one is a usage error: nothing is
// broken, the command was run somewhere it cannot work.
func repo() (string, error) {
	root, err := glazecheck.FindRepo()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errUsage, err)
	}
	return root, nil
}

func glazeCheckFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("glaze-check", flag.ContinueOnError)
	fs.Bool("windows", false, "run the four on the VM, through app-create, instead of natively on this Mac")
	fs.String("vm", utmvm.DefaultVMName, "VM name, with -windows")
	return fs
}

// runGlazeCheck builds the four examples, runs them all on this Mac or with
// -windows on the VM, and records the verdict. The logic is in
// internal/glazecheck; this supplies the Windows runner, which is app-create.
func runGlazeCheck(v values, _ []string) error {
	windows, name := v.Bool("windows"), v.String("vm")
	say := utmvm.Printer("glaze-check")

	root, err := repo()
	if err != nil {
		return err
	}
	if err := glazecheck.NeedGo(); err != nil {
		return err
	}

	o := glazecheck.Options{
		Root:     root,
		Target:   glazecheck.TargetMac,
		Platform: runtime.GOOS + "/" + runtime.GOARCH + " (this machine, natively)",
		Say:      say,
	}
	if windows {
		// Resolved before building, so a missing VM fails fast with exit 3
		// and is never recorded as a glaze verdict.
		e, err := utmvm.Find(name)
		if err != nil {
			return err
		}
		// Held once around all four runs, so app-create is run with exec, not
		// runTool, which would try to take the lock again and be refused.
		release, err := utmvm.AcquireMutation()
		if err != nil {
			return err
		}
		defer release()

		appCreate, _ := find("app-create")
		o.Target = glazecheck.TargetWindows
		o.Platform = "windows/arm64, VM " + e.Name + " (through app-create)"
		o.Run = func(p glazecheck.Program, exe string) error {
			a := []string{"-vm", e.Name}
			if p.GUI {
				a = append(a, "-gui")
			}
			a = append(a, exe)
			return appCreate.exec(append(a, p.Args...))
		}
		// These mean the program never ran. Anything else, including its own
		// non-zero exit, is a result.
		o.NotRun = func(err error) bool {
			return errors.Is(err, utmvm.ErrNoAgent) || errors.Is(err, utmvm.ErrNoVM) ||
				errors.Is(err, utmvm.ErrMutationInProgress)
		}
	}
	_, err = glazecheck.Check(o)
	return err
}

// runGlazeStatus prints the recorded verdict file whole, then whether it still
// holds against the tree now.
func runGlazeStatus(values, []string) error {
	root, err := repo()
	if err != nil {
		return err
	}
	body, err := glazecheck.Read(root)
	if err != nil {
		return err
	}
	say := utmvm.Reporter("glaze-status")
	_, _ = fmt.Fprint(utmvm.Out, body)
	say("")
	say("--- from %s, checked against the tree now:", filepath.Join(root, glazecheck.StatusFile))
	for _, l := range glazecheck.Freshness(root, body) {
		say("%s", l)
	}
	return nil
}
