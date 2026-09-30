package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
	fs.Bool("windows", false, "run the suite on the VM, through app-create, instead of natively on this machine")
	fs.String("vm", utmvm.DefaultVMName, "VM name, with -windows")
	fs.String("import", "", "record runs made elsewhere instead of running the suite: a directory holding the conformance CI job's downloaded artifacts, whose sections and screenshots replace this checkout's")
	return fs
}

// runGlazeCheck builds examples/conformance into a test binary, runs it on this
// machine or with -windows on the VM, and records every test's result. The
// logic is in internal/glazecheck; this supplies the Windows runner, which is
// app-create's own library call.
//
// Natively it runs on macOS, and on Windows too: that is how the CI job on
// GitHub's windows-11-arm runner gets the same record the VM run does. With
// -import it runs nothing, and records CI's artifacts instead.
func runGlazeCheck(v values, _ []string) error {
	windows, name := v.Bool("windows"), v.String("vm")
	say := utmvm.Printer("glaze-check")

	root, err := repo()
	if err != nil {
		return err
	}
	if dir := v.String("import"); dir != "" {
		// Nothing is built or run, so neither Go nor a desktop is needed:
		// this is how the pages workflow, on Linux, publishes CI's record.
		return glazecheck.Import(root, dir, say)
	}
	if err := glazecheck.NeedGo(); err != nil {
		return err
	}

	o := glazecheck.Options{
		Root:     root,
		Platform: runtime.GOOS + "/" + runtime.GOARCH + " (this machine, natively" + ciRunner() + ")",
		Say:      say,
	}
	switch runtime.GOOS {
	case "darwin":
		o.Target = glazecheck.TargetMac
	case "windows":
		o.Target = glazecheck.TargetWindows
	default:
		if !windows {
			return fmt.Errorf("%w: the suite runs natively on macOS and Windows only; this is %s. -windows runs it on the VM",
				errUsage, runtime.GOOS)
		}
	}
	if windows {
		// Resolved before building, so a missing VM fails fast with exit 3
		// and is never recorded as a glaze verdict.
		e, err := utmvm.Find(name)
		if err != nil {
			return err
		}
		// Held around the run, so the library is called directly rather than
		// through the app-create command, which would try to take the lock
		// again and be refused by this process.
		release, err := utmvm.AcquireMutation()
		if err != nil {
			return err
		}
		defer release()

		// app-create's own defaults, read from its flags, so there is one
		// answer to "which guest account" and "how long".
		defaults := appCreateFlags()
		user := defaults.Lookup("user").DefValue
		timeout := values{defaults}.Duration("timeout")

		o.Target = glazecheck.TargetWindows
		o.Platform = "windows/arm64, VM " + e.Name + " (through app-create -gui)"
		o.BuildEnv = []string{"GOOS=windows", "GOARCH=arm64", "CGO_ENABLED=0"}
		// -gui for the whole suite: most of it opens windows, and the guest
		// agent's session 0 has no window station. The headless tests are
		// checked in session 0 by app:test, which runs the same binary with
		// -test.short.
		o.Run = func(exe string, args []string) (string, error) {
			if err := ensureAgent(e, say); err != nil {
				return "", err
			}
			res, err := utmvm.AppCreate(e.UUID, exe, utmvm.AppOptions{
				Args: args, GUI: true, User: user, Timeout: timeout, Say: say,
			})
			if err != nil {
				return res.Stdout, err
			}
			if res.ExitCode != 0 {
				return res.Stdout, fmt.Errorf("%s exited %d in the guest", filepath.Base(exe), res.ExitCode)
			}
			return res.Stdout, nil
		}
		// The same account the suite runs as.
		o.ResetDesktop = func() error { return utmvm.DesktopReset(e.UUID, user, say) }
		// The screenshots are written in the guest, where the interactive
		// session can write, and pulled back one by one by the names the
		// tests logged. Overwritten by the next run, not swept by app-delete.
		guestShots := utmvm.GuestPublicPath("irgo-conformance-shots")
		o.ShotsDir = guestShots
		o.Fetch = func(rel string) ([]byte, error) {
			return utmvm.Pull(e.UUID, guestShots+`\`+strings.ReplaceAll(rel, "/", `\`))
		}
		// These mean the suite never ran. Anything else, including its own
		// non-zero exit, is a result, read from what it printed.
		o.NotRun = func(err error) bool {
			return errors.Is(err, utmvm.ErrNoAgent) || errors.Is(err, utmvm.ErrNoVM) ||
				errors.Is(err, utmvm.ErrMutationInProgress)
		}
	}
	_, err = glazecheck.Check(o)
	return err
}

// ciRunner names the GitHub Actions image when running in one, so a record
// made on a hosted runner cannot be mistaken for one made on a desk.
func ciRunner() string {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return ""
	}
	return ", GitHub Actions runner " + os.Getenv("ImageOS") + " " + os.Getenv("ImageVersion")
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
