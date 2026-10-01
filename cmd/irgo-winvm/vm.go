package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func vmCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-create", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	ownerFlag(fs)
	fs.Bool("install", false, "run the unattended Windows install (about 45 minutes)")
	fs.Duration("timeout", 60*time.Minute, "overall limit for the install")
	fs.Bool("golden", true, "clone the golden image when there is one, instead of installing (false: install from the ISO)")
	fs.Bool("overcommit", false, "start the VM even when the running VMs' configured memory leaves this Mac too little; they will swap")
	return fs
}

// runVMCreate makes a VM and, with -install, installs Windows on it. Every
// stage is idempotent, so a second run skips what is done and takes seconds.
func runVMCreate(v values, _ []string) error {
	name, install, timeout := v.String("vm"), v.Bool("install"), v.Duration("timeout")
	say := utmvm.Printer("vm-create")

	bundle, err := utmvm.BundlePath(name)
	if err != nil {
		return err
	}
	say("vm:     %s", name)
	say("bundle: %s", utmvm.Home(bundle))
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	// Room for it, and whose it is, before anything is made or started.
	finish, err := utmvm.BeginCreate(name, v.caller, !v.Bool("golden"), v.Bool("overcommit"), say)
	if err != nil {
		return err
	}
	defer finish()

	res, err := utmvm.VMCreate(utmvm.VMCreateOptions{
		VMName:   name,
		Install:  install,
		Timeout:  timeout,
		NoGolden: !v.Bool("golden"),
	}, func(line string) { say("%s", line) })
	if err != nil {
		return err
	}
	if res.Ready {
		say("%s is ready", res.VM)
		return nil
	}
	say("%s is not ready yet — see the steps above for what remains", res.VM)
	return nil
}

func vmDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-delete", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	ownerFlag(fs)
	fs.Bool("force", false, "actually delete; without this it only lists")
	return fs
}

// runVMDelete removes a VM. Without -force it lists what would go and refuses.
// A VM UTM says does not exist is nothing to undo, which is success; UTM not
// answering is not the same thing, and is an error.
func runVMDelete(v values, _ []string) error {
	name, force := v.String("vm"), v.Bool("force")
	say := utmvm.Printer("vm-delete")

	bundle, err := utmvm.BundlePath(name)
	if err != nil {
		return err
	}
	say("STEP 1/2  the VM")
	say("          %s", utmvm.Home(bundle))

	e, found, err := findForUndo(name)
	if err != nil {
		return err
	}
	if !found {
		say("          UTM knows no VM %q; nothing to delete", name)
		if force {
			return utmvm.ForgetVM(name)
		}
		return nil
	}
	r, err := utmvm.InspectRemoval(name)
	if err != nil {
		return err
	}
	say("          %s, %s", e.Status, utmvm.HumanBytes(r.TotalBytes))

	say("STEP 2/2  would delete:")
	say("          %-9s %s", utmvm.HumanBytes(r.TotalBytes), utmvm.Home(r.Path))
	if r.Running {
		say("          it is running and will be stopped first")
	}
	if !force {
		return fmt.Errorf("%s of VM, and the Windows on it.\n"+
			"  Reinstalling takes about 45 minutes. Pass -force to do it (%w)",
			utmvm.HumanBytes(r.TotalBytes), errRefused)
	}

	say("STEP 2/2  deleting")
	out, err := utmvm.Delete(name, true, func(f string, a ...any) { say("          "+f, a...) })
	if err != nil {
		return err
	}
	say("removed %s — %s reclaimed", utmvm.Home(out.Path), utmvm.HumanBytes(out.TotalBytes))
	return utmvm.ForgetVM(e.Name)
}

// findVM is utmvm.Find, a variable so tests can have UTM answer either way
// without UTM.
var findVM = utmvm.Find

// findForUndo is Find for an undo, which must tell "UTM answered: there is no
// such VM" (found is false, and the undo has nothing to do) from "UTM could
// not be asked" (an error). Treating both as nothing to delete made vm-delete
// and app-delete exit 0 when utmctl itself had failed, so a caller believed a
// VM gone that was still there.
func findForUndo(name string) (e utmvm.Entry, found bool, err error) {
	e, err = findVM(name)
	switch {
	case err == nil:
		return e, true, nil
	case errors.Is(err, utmvm.ErrNoVM):
		return utmvm.Entry{}, false, nil
	}
	return utmvm.Entry{}, false, fmt.Errorf("cannot tell whether VM %q exists, so nothing was deleted: %w", name, err)
}

func vmReapFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-reap", flag.ContinueOnError)
	fs.Duration("stale", 24*time.Hour, "the lease: a clone nobody has used for longer than this is removed")
	fs.Bool("force", false, "actually delete; without this it only lists")
	return fs
}

// runVMReap removes the clones callers made and left: every VM with an owner
// record whose lease has run out and that no command is using. irgo-win11,
// the golden image and any VM without a record are never touched. Without
// -force it lists what would go and refuses, like every destructive command.
func runVMReap(v values, _ []string) error {
	lease, force := v.Duration("stale"), v.Bool("force")
	if lease <= 0 {
		return fmt.Errorf("%w: -stale must be positive, got %s", errUsage, lease)
	}
	say := utmvm.Printer("vm-reap")
	say("records: %s", utmvm.Home(utmvm.RecordsDir()))
	say("lease:   %s", lease)
	decisions, bad, err := utmvm.Reap(lease, force, func(f string, a ...any) { say("          "+f, a...) })
	for _, b := range bad {
		say("  unreadable record, kept: %s", b)
	}
	var acting int
	for _, d := range decisions {
		verb := "keep  "
		switch d.Action {
		case utmvm.ReapDelete:
			verb, acting = "delete", acting+1
		case utmvm.ReapForget:
			verb, acting = "forget", acting+1
		}
		say("  %s %-20s owner %s — %s", verb, d.Record.Name, d.Record.Owner, d.Why)
	}
	if err != nil {
		return err
	}
	if len(decisions) == 0 {
		say("no VM records; nothing to reap")
		return nil
	}
	if acting == 0 {
		say("nothing to reap")
		return nil
	}
	if !force {
		return fmt.Errorf("%d to delete or forget. Pass -force to do it (%w)", acting, errRefused)
	}
	say("reaped %d", acting)
	return nil
}

func vmRepairFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-repair", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	ownerFlag(fs)
	fs.String("user", "dev", "the AutoLogon user whose password must never expire")
	fs.Bool("reboot", false, "restart the VM afterwards so AutoLogon runs again")
	fs.Bool("share", true, "open the guest's SMB share that pushes go through at network speed; -share=false removes it")
	return fs
}

// runVMRepair fixes an expired password and a stale WebView2 registration.
// Both leave the agent answering while every -gui run fails, so the repair runs
// through the agent as SYSTEM, the access that still works. It also opens the
// file share that makes pushes fast, or with -share=false removes it.
func runVMRepair(v values, _ []string) error {
	name, user, share, reboot := v.String("vm"), v.String("user"), v.Bool("share"), v.Bool("reboot")
	say := utmvm.Printer("vm-repair")
	e, err := utmvm.Find(name)
	if err != nil {
		return err
	}
	if err := ensureAgent(e, say); err != nil {
		return err
	}
	say("vm:     %s", e.Name)
	return utmvm.VMRepair(e.UUID, user, share, reboot, say)
}

// ensureAgent recovers a VM whose guest agent is not answering. Windows
// reboots on its own for updates and can land back in the UEFI shell, so a VM
// that answered earlier cannot be assumed to answer now.
func ensureAgent(e utmvm.Entry, say func(string, ...any)) error {
	if utmvm.Named(e.UUID).AgentReady() {
		return nil
	}
	say("VM not answering; recovering")
	bundle, err := utmvm.BundlePath(e.Name)
	if err != nil {
		return err
	}
	return utmvm.EnsureReady(e.UUID, bundle, 10*time.Minute, say)
}

func vmScreenFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-screen", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	ownerFlag(fs)
	fs.String("o", "", "where to write the PNG (default: the shots directory)")
	fs.String("promote", "", "copy the newest shot of each stage into this directory, named for the stage")
	return fs
}

// runVMScreen photographs the guest's display: the only way to see a boot stuck
// at a UEFI prompt, or tell a stalled install from a working one.
//
// With -promote it takes no picture, and instead copies the newest shot of each
// stage to a stable name that docs can reference.
func runVMScreen(v values, _ []string) error {
	name, out, promote := v.String("vm"), v.String("o"), v.String("promote")
	say := utmvm.Printer("vm-screen")

	if promote != "" {
		stages, err := utmvm.Promote(promote)
		if err != nil {
			return err
		}
		say("from:   %s", utmvm.Home(utmvm.ShotDir()))
		say("into:   %s", utmvm.Home(promote))
		for _, s := range stages {
			say("  · %s.png", s)
		}
		say("%d stage(s) published", len(stages))
		return nil
	}

	// Resolved first so a missing VM exits 3 like every other command, rather
	// than failing as "no UTM window titled ...".
	if _, err := utmvm.Find(name); err != nil {
		return err
	}
	say("vm:     %s", name)

	// By default into shots/, timestamped and outside the repository, never
	// over the committed evidence in docs/screens.
	if out == "" {
		p, err := utmvm.Shot(name, "vm-screen")
		if err != nil {
			return err
		}
		say("shot:   %s", utmvm.Home(p))
		say("written")
		return nil
	}
	say("shot:   %s", utmvm.Home(out))
	if err := utmvm.Screenshot(name, out); err != nil {
		return err
	}
	say("written")
	return nil
}
