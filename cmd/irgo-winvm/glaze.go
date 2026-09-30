package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// repo finds the checkout, and classifies "not in one" as the command being
// called wrongly: nothing is broken, it was run somewhere it cannot work.
func repo() (string, error) {
	root, err := glazecheck.FindRepo()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errUsage, err)
	}
	return root, nil
}

// runGlazeCheck is `mise run glaze:mac` and `mise run glaze:windows`.
//
// It was two shell scripts in mise.toml that printed YES or NO and threw it
// away. The logic — build, run all four even when one fails, record the
// verdict, keep the full log — lives in internal/glazecheck; this wires the
// Windows runner, which is app-create itself.
func runGlazeCheck(args []string) error {
	fs := glazeCheckFlags()
	if err := fs.Parse(args); err != nil {
		return err
	}
	v := values{fs}
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
		// Resolved before anything is built, so a missing VM is exit 3 in a
		// second rather than after a build, and is never recorded as a glaze
		// verdict: it is not one.
		e, fErr := utmvm.Find(name)
		if fErr != nil {
			return fErr
		}
		// Once, around all four. app-create is called directly below rather
		// than through runTool, which would take the lock again and be refused
		// by the holder — this process.
		release, lErr := utmvm.AcquireMutation()
		if lErr != nil {
			return lErr
		}
		defer release()

		o.Target = glazecheck.TargetWindows
		o.Platform = "windows/arm64, VM " + e.Name + " (through app-create)"
		o.Run = func(p glazecheck.Program, exe string) error {
			a := []string{"-vm", e.Name}
			if p.GUI {
				a = append(a, "-gui")
			}
			a = append(a, exe)
			return runAppCreate(append(a, p.Args...))
		}
		// These mean the program never ran. Anything else — including the
		// program's own non-zero exit — is a result.
		o.NotRun = func(err error) bool {
			return errors.Is(err, utmvm.ErrNoAgent) || errors.Is(err, utmvm.ErrNoVM) ||
				errors.Is(err, utmvm.ErrMutationInProgress)
		}
	}
	_, err = glazecheck.Check(o)
	return err
}

// runGlazeStatus prints the recorded verdict, then whether it still holds.
//
// The whole file, not a summary of it: it is short, it is what is committed,
// and an agent asking over MCP should get the same text the owner reads.
func runGlazeStatus([]string) error {
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
