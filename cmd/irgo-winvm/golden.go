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
	fs.String("vm", "", "the installed, disposable VM to seal (required)")
	fs.Bool("force", false, "allow sealing "+utmvm.DefaultVMName+", the shared VM")
	fs.Bool("check", true, "run the VM conformance suite on the verification clone, and refuse an image that fails it (needs the source checkout)")
	return fs
}

// verifyGoldenClone runs the VM suite on the golden image's verification
// clone and records it in docs/VM-STATUS.md. Outside a checkout there is no
// suite to build: that is the verdict, recorded in golden.json, and not a
// refusal — vm-golden-create works on any machine.
func verifyGoldenClone(vm string, say func(string, ...any)) (string, error) {
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
	case strings.EqualFold(e.Name, utmvm.GoldenVMName):
		return fmt.Errorf("%w: %s is the golden image itself; seal the VM it should be made from", errUsage, e.Name)
	case strings.EqualFold(e.Name, utmvm.DefaultVMName) && !force:
		return fmt.Errorf("%s is the shared VM. Sealing it turns BitLocker and hibernation off\n"+
			"  and removes the component store's backups, and stops it while that happens.\n"+
			"  Seal a disposable VM instead, or pass -force (%w)", e.Name, errRefused)
	}

	src, err := utmvm.BundlePath(e.Name)
	if err != nil {
		return err
	}
	golden, err := utmvm.BundlePath(utmvm.GoldenVMName)
	if err != nil {
		return err
	}
	say("from:     %s (%s)", e.Name, utmvm.Home(src))
	say("golden:   %s (%s)", utmvm.GoldenVMName, utmvm.Home(golden))
	say("manifest: %s", utmvm.Home(utmvm.GoldenManifestPath()))
	opts := utmvm.GoldenCreateOptions{Source: e.Name, ToolVersion: version}
	if v.Bool("check") {
		opts.Verify = func(vm string) (string, error) { return verifyGoldenClone(vm, say) }
	}
	if _, err := utmvm.GoldenCreate(opts, say); err != nil {
		return err
	}
	say("vm-create -vm <name> now clones %s instead of installing", utmvm.GoldenVMName)
	return nil
}

func vmGoldenDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-golden-delete", flag.ContinueOnError)
	fs.Bool("force", false, "actually delete; without this it only lists")
	return fs
}

// runVMGoldenDelete removes the golden image and its manifest. Without -force
// it only lists; with nothing there it succeeds.
func runVMGoldenDelete(v values, _ []string) error {
	force := v.Bool("force")
	say := utmvm.Printer("vm-golden-delete")

	g := utmvm.Golden()
	say("golden:   %s", utmvm.Home(g.Bundle))
	say("manifest: %s", utmvm.Home(g.ManifestPath))
	if !g.Present && g.Manifest == nil {
		// UTM is asked too: a registered image whose disk cannot be stat'ed
		// is still something to delete.
		if _, err := utmvm.Find(utmvm.GoldenVMName); err != nil {
			say("no golden image; nothing to delete")
			return nil
		}
	}
	if !force {
		return fmt.Errorf("the golden image, %s on disk. VMs already cloned from it are not affected,\n"+
			"  but a new one takes a full install until vm-golden-create makes another. Pass -force to do it (%w)",
			utmvm.HumanBytes(g.Allocated), errRefused)
	}
	return utmvm.GoldenDelete(func(f string, a ...any) { say("  "+f, a...) })
}

// goldenRows reports the golden image and its manifest. It is optional, so its
// absence is "none", never MISSING.
func goldenRows() []doctorRow {
	g := utmvm.Golden()
	image := "none"
	if g.Present {
		image = utmvm.HumanBytes(g.Allocated) + " of " + utmvm.HumanBytes(g.Apparent)
	}
	sealed := "none"
	if m := g.Manifest; m != nil {
		sealed = fmt.Sprintf("Windows %s, %s old", m.Windows, age(time.Since(m.Created)))
		if !g.Present {
			sealed += "; the image is gone (vm-golden-delete clears this)"
		}
	}
	return []doctorRow{
		{What: "golden image", State: image, Path: g.Bundle, Present: g.Present},
		{What: "golden sealed", State: sealed, Path: g.ManifestPath, Present: g.Manifest != nil},
	}
}

// age is a duration read at a glance: whole days once it is a day.
func age(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return d.Round(time.Minute).String()
}
