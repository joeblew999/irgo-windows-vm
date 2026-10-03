package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func vmGoldenCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-golden-create", flag.ContinueOnError)
	// No default, unlike every other -vm: the default is the shared VM.
	fs.String("vm", "", "the installed, disposable VM to seal (required); its system decides which golden image it becomes")
	fs.Bool("force", false, "allow sealing "+utmvm.DefaultVMName+", the shared VM")
	fs.Bool("check", true, "run the VM conformance suite on the verification clone, and refuse an image that fails it (needs the source checkout)")
	return fs
}

// verifyGoldenClone runs the VM suite on the golden image's verification
// clone and records it in docs/VM-STATUS.md. Outside a checkout there is no
// suite to build: that is the verdict, recorded in golden.json, and not a
// refusal — vm-golden-create works on any machine. The suite has no Linux
// checks yet; a Linux clone has passed vm-create's own check by then.
func verifyGoldenClone(vm string, say func(string, ...any)) (string, error) {
	if os, err := guestOSOf(vm); err != nil {
		return "", err
	} else if os != utmvm.GuestWindows {
		return "not run: the VM conformance suite has no " + os + " checks yet; the clone passed vm-create's own check", nil
	}
	root, err := glazecheck.FindRepo()
	if err != nil {
		return "not run: not in a checkout of irgo-windows-vm, which the suite is built from", nil
	}
	if err := glazecheck.NeedGo(); err != nil {
		return "not run: " + err.Error(), nil
	}
	e, err := utmvm.Find(vm)
	if err != nil {
		return "", err
	}
	sec, err := checkVM(root, e, say)
	return sec.Verdict(), err
}

// runVMGoldenCreate seals a disposable VM into the golden image. The VM is
// resolved before the guard, so whether it is the shared one is answered yes,
// no or could-not-tell, and could-not-tell refuses.
func runVMGoldenCreate(v values, _ []string) error {
	name, force := v.String("vm"), v.Bool("force")
	if name == "" {
		return fmt.Errorf("%w: irgo-winvm vm-golden-create -vm <installed disposable VM>\n"+
			"  make one with: irgo-winvm vm-create -vm <name> -install -golden=false", errUsage)
	}
	say := utmvm.Printer("vm-golden-create")

	// ErrNoVM is exit 3; any other error is UTM not answering, which refuses.
	e, err := utmvm.Find(name)
	if err != nil {
		return err
	}
	switch {
	case utmvm.IsGoldenImage(e.Name):
		return fmt.Errorf("%w: %s is a golden image itself; seal the VM it should be made from", errUsage, e.Name)
	case strings.EqualFold(e.Name, utmvm.DefaultVMName) && !force:
		return fmt.Errorf("%s is the shared VM. Sealing it turns BitLocker and hibernation off\n"+
			"  and removes the component store's backups, and stops it while that happens.\n"+
			"  Seal a disposable VM instead, or pass -force (%w)", e.Name, errRefused)
	}

	src, err := utmvm.BundlePath(e.Name)
	if err != nil {
		return err
	}
	osName, err := guestOSOf(e.Name)
	if err != nil {
		return err
	}
	image, err := utmvm.GoldenName(osName)
	if err != nil {
		return err
	}
	golden, err := utmvm.BundlePath(image)
	if err != nil {
		return err
	}
	say("from:     %s (%s, %s)", e.Name, osName, utmvm.Home(src))
	say("golden:   %s (%s)", image, utmvm.Home(golden))
	say("manifest: %s", utmvm.Home(utmvm.GoldenManifestPath(osName)))
	opts := utmvm.GoldenCreateOptions{Source: e.Name, ToolVersion: version}
	if v.Bool("check") {
		opts.Verify = func(vm string) (string, error) { return verifyGoldenClone(vm, say) }
	}
	if _, err := utmvm.GoldenCreate(opts, say); err != nil {
		return err
	}
	if osName == utmvm.GuestWindows {
		say("vm-create -vm <name> now clones %s instead of installing", image)
	} else {
		say("vm-create -os %s -vm <name> now clones %s", osName, image)
	}
	return nil
}

func vmGoldenDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-golden-delete", flag.ContinueOnError)
	fs.String("os", utmvm.GuestWindows, "which golden image: windows ("+utmvm.GoldenVMName+") or linux ("+utmvm.GoldenLinuxVMName+")")
	fs.Bool("force", false, "actually delete; without this it only lists")
	return fs
}

// runVMGoldenDelete removes a golden image and its manifest. Without -force
// it only lists; with nothing there it succeeds.
func runVMGoldenDelete(v values, _ []string) error {
	force := v.Bool("force")
	say := utmvm.Printer("vm-golden-delete")
	osName, err := utmvm.GuestOSNamed(v.String("os"))
	if err != nil {
		return fmt.Errorf("%w: -os %w", errUsage, err)
	}

	g := utmvm.Golden(osName)
	say("golden:   %s (%s)", g.Name, utmvm.Home(g.Bundle))
	say("manifest: %s", utmvm.Home(g.ManifestPath))
	if !g.Present && g.Manifest == nil {
		// UTM is asked too: a registered image whose disk cannot be stat'ed
		// is still something to delete.
		if _, err := utmvm.Find(g.Name); err != nil {
			say("no golden image; nothing to delete")
			return nil
		}
	}
	if !force {
		then := "a full install"
		if osName != utmvm.GuestWindows {
			then = "a VM made from Ubuntu's cloud image (vm-create -install)"
		}
		return fmt.Errorf("the golden image %s, %s on disk. VMs already cloned from it are not affected,\n"+
			"  but a new one takes %s until vm-golden-create makes another. Pass -force to do it (%w)",
			g.Name, utmvm.HumanBytes(g.Allocated), then, errRefused)
	}
	return utmvm.GoldenDelete(osName, func(f string, a ...any) { say("  "+f, a...) })
}

// goldenRows reports each golden image and its manifest. They are optional,
// so an absence is "none", never MISSING.
func goldenRows() []doctorRow {
	var rows []doctorRow
	for _, osName := range []string{utmvm.GuestWindows, utmvm.GuestLinux} {
		g := utmvm.Golden(osName)
		image := "none"
		if g.Present {
			image = utmvm.HumanBytes(g.Allocated) + " of " + utmvm.HumanBytes(g.Apparent)
		}
		sealed := "none"
		if m := g.Manifest; m != nil {
			system := "Windows " + m.Windows
			if m.System != "" {
				system = m.System
			}
			sealed = fmt.Sprintf("%s, %s old", system, age(time.Since(m.Created)))
			if !g.Present {
				sealed += fmt.Sprintf("; the image is gone (vm-golden-delete -os %s clears this)", osName)
			}
		}
		what := "golden image"
		if osName != utmvm.GuestWindows {
			what += " (" + osName + ")"
		}
		rows = append(rows,
			doctorRow{What: what, State: image, Path: g.Bundle, Present: g.Present},
			doctorRow{What: strings.Replace(what, "image", "sealed", 1), State: sealed, Path: g.ManifestPath, Present: g.Manifest != nil},
		)
	}
	return rows
}

// age is a duration read at a glance: whole days once it is a day.
func age(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return d.Round(time.Minute).String()
}
