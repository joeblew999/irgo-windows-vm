package main

import (
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func vmCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-create", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	ownerFlag(fs)
	fs.String("os", utmvm.GuestWindows, "the system in a new VM: windows, or linux (Ubuntu Server 24.04 from its cloud image; needs -vm and -install). A VM that exists keeps its own")
	fs.Bool("install", false, "make the VM when there is no golden image to clone: the unattended Windows install (about 45 minutes), or with -os linux a download and a first boot (about a minute)")
	fs.Duration("timeout", 60*time.Minute, "overall limit for the install")
	fs.Bool("golden", true, "clone the golden image of the VM's system when there is one, instead of installing (false: install from the ISO, or with -os linux make it from the cloud image)")
	fs.Bool("overcommit", false, "start the VM even when the running VMs' configured memory leaves this Mac too little; they will swap")
	return fs
}

// runVMCreate makes a VM and, with -install, installs Windows on it, or with
// -os linux makes one from Ubuntu's cloud image. Every stage is idempotent,
// so a second run skips what is done and takes seconds.
func runVMCreate(v values, _ []string) error {
	name, install, timeout := v.String("vm"), v.Bool("install"), v.Duration("timeout")
	say := utmvm.Printer("vm-create")

	bundle, err := utmvm.BundlePath(name)
	if err != nil {
		return err
	}
	osName, err := createOS(v, name)
	if err != nil {
		return err
	}
	say("vm:     %s", name)
	say("os:     %s", osName)
	say("bundle: %s", utmvm.Home(bundle))
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	// Room for it, and whose it is, before anything is made or started.
	// What the tool wrote that is past its bounds goes first, so it never
	// counts against the room (prune.go).
	autoPrune(say)
	finish, err := beginCreateReaping(func() (func(), error) {
		return utmvm.BeginCreate(name, v.caller, osName, !v.Bool("golden"), v.Bool("overcommit"), say)
	}, say)
	if err != nil {
		return err
	}
	defer finish()
	defer reportCapacityChange()

	res, err := utmvm.VMCreate(utmvm.VMCreateOptions{
		VMName:   name,
		Install:  install,
		Timeout:  timeout,
		NoGolden: !v.Bool("golden"),
		OS:       osName,
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

// createOS is the system vm-create is to make or boot: what -os says for a VM
// that does not exist, and what the VM's record says for one that does. An
// -os that disagrees with a VM that exists is refused, as is a Linux VM under
// a name that means Windows to every other command.
func createOS(v values, name string) (string, error) {
	want, err := utmvm.GuestOSNamed(v.String("os"))
	if err != nil {
		return "", fmt.Errorf("%w: -os %w", errUsage, err)
	}
	var osGiven bool
	v.fs.Visit(func(f *flag.Flag) { osGiven = osGiven || f.Name == "os" })
	if _, fErr := findVM(name); fErr == nil {
		have, err := guestOSOf(name)
		if err != nil {
			return "", err
		}
		if osGiven && have != want {
			return "", fmt.Errorf("%w: %s is a %s VM, and -os says %s; a VM keeps the system it was made with. "+
				"Leave -os out, or vm-delete it first", errUsage, name, have, want)
		}
		return have, nil
	}
	if want == utmvm.GuestWindows {
		return want, nil
	}
	// Leaving -vm out lands here too: the default VM is the first of these.
	for _, reserved := range []string{utmvm.DefaultVMName, utmvm.GoldenVMName, utmvm.GoldenLinuxVMName} {
		if strings.EqualFold(name, reserved) {
			return "", fmt.Errorf("%w: -os %s needs -vm <name> with a name of its own: %s is a name every command takes "+
				"to be a Windows VM, and there is no default %s one", errUsage, want, reserved, want)
		}
	}
	return want, nil
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
		// What is lost, which depends on what is in it. A VM whose system
		// cannot be told is described as the expensive one.
		if os, oErr := guestOSOf(name); oErr == nil && os == utmvm.GuestLinux {
			return fmt.Errorf("%s of VM, and the Linux on it.\n"+
				"  Making it again is vm-create -os linux -install, about a minute. Pass -force to do it (%w)",
				utmvm.HumanBytes(r.TotalBytes), errRefused)
		}
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
	defer reportCapacityChange()
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
	reportCapacityChange()
	return nil
}

func vmRepairFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-repair", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	ownerFlag(fs)
	fs.String("user", "dev", "the AutoLogon user whose password must never expire")
	fs.Bool("reboot", false, "restart the VM afterwards so AutoLogon runs again")
	fs.Bool("share", true, "open the guest's SMB share that pushes go through at network speed; -share=false removes it")
	fs.Bool("check", false, "run the VM conformance suite before and after, and say which checks the repair fixed (needs the source checkout; not with -reboot)")
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
	if !v.Bool("check") {
		return utmvm.VMRepair(e.UUID, user, share, reboot, say)
	}
	if reboot {
		return fmt.Errorf("%w: -check runs the suite straight after the repair, and with -reboot the VM is restarting then; "+
			"repair with -reboot, then run irgo-winvm vm-check -vm %s once it is back", errUsage, e.Name)
	}
	root, err := repo()
	if err != nil {
		return err
	}
	if err := glazecheck.NeedGo(); err != nil {
		return err
	}
	return repairChecked(root, e, func() error { return utmvm.VMRepair(e.UUID, user, share, false, say) }, checkVM, say)
}

// repairChecked runs the VM suite, the repair, and the suite again, and says
// what the repair fixed and what it broke. The second run is the one
// recorded in docs/VM-STATUS.md. A repair that failed is still followed by
// the check, so the record says where the VM was left.
func repairChecked(root string, e utmvm.Entry, repair func() error,
	check func(string, utmvm.Entry, func(string, ...any)) (glazecheck.Section, error), say func(string, ...any)) error {
	say("checking %s before the repair", e.Name)
	before, _ := check(root, e, say) // its verdict is only the baseline
	rErr := repair()
	say("checking %s after the repair", e.Name)
	after, cErr := check(root, e, say)
	fixed, broke := glazecheck.Changed(before, after)
	say("fixed by the repair: %s", orNone(fixed))
	say("broken since the repair: %s", orNone(broke))
	say("now: %s", after.Verdict())
	if rErr != nil {
		return rErr
	}
	return cErr
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func vmSSHCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-ssh-create", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	ownerFlag(fs)
	fs.String("key", "~/.ssh/id_ed25519.pub", "the public key to authorize: a .pub file on this Mac. Never a private key")
	fs.String("user", "dev", "the guest account to log in as; on Windows it must be an administrator")
	fs.Duration("timeout", 20*time.Minute, "how long to allow the guest; Windows installs OpenSSH Server the first time, which takes minutes")
	return fs
}

// sshUser is what -user may be: it goes into a command line in the guest and
// into the ssh line printed for the caller to run.
var sshUser = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,19}$`)

// runVMSSHCreate turns on the OpenSSH server in a VM, authorizes one public
// key, waits for port 22 to answer from this Mac, and prints the ssh line. On
// a VM that already has all of it, it says so and changes nothing. Which
// script does it in the guest follows from the system the VM's record names.
func runVMSSHCreate(v values, _ []string) error {
	name, user := v.String("vm"), v.String("user")
	if !sshUser.MatchString(user) {
		return fmt.Errorf("%w: -user %q is not an account name this command accepts (letters, digits, . _ -)", errUsage, user)
	}
	// Before UTM is asked anything: a wrong key should not boot a VM.
	key, err := utmvm.ReadSSHPublicKey(v.String("key"))
	if err != nil {
		return err
	}
	say := utmvm.Printer("vm-ssh-create")
	e, err := utmvm.Find(name)
	if err != nil {
		return err
	}
	if err := ensureAgent(e, say); err != nil {
		return err
	}
	say("vm:     %s", e.Name)
	say("key:    %s %s (%s)", key.Type, key.Comment, v.String("key"))
	host, err := utmvm.VMSSHCreate(e.UUID, user, key, v.Duration("timeout"), say)
	if err != nil {
		return err
	}
	say("turn it off with: irgo-winvm vm-ssh-delete -vm %s", e.Name)
	say("connect with:")
	// The last line, and nothing else on it, so it can be copied or captured.
	_, _ = fmt.Fprintf(utmvm.Out, "ssh %s@%s\n", user, host)
	return nil
}

func vmSSHDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-ssh-delete", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	ownerFlag(fs)
	return fs
}

// runVMSSHDelete turns SSH off in a VM again. A VM UTM says does not exist has
// nothing to turn off, which is success, so the undo can run twice.
func runVMSSHDelete(v values, _ []string) error {
	name := v.String("vm")
	say := utmvm.Printer("vm-ssh-delete")
	say("vm:     %s", name)
	e, found, err := findForUndo(name)
	if err != nil {
		return err
	}
	if !found {
		say("UTM knows no VM %q; nothing to turn off", name)
		return nil
	}
	if err := utmvm.VMSSHDelete(e.UUID, say); err != nil {
		return err
	}
	say("SSH is off in %s", e.Name)
	return nil
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
