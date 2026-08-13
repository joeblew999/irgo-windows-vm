// Command irgo-winvm brings up a Windows 11 ARM64 VM on Apple Silicon so an
// irgo desktop build can be tested on the machine that produced it.
//
// It exists because the GUI path is a lot of clicking that cannot be scripted,
// reviewed or run twice the same way. Everything here is plain Go: no hdiutil,
// no plutil, no shell.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/utmvm"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `irgo-winvm — build a Go program on your Mac, run it on real Windows.

Three steps, in this order. Each one is cheap to repeat: if it is already
done, it says so and stops.

  1  iso-create   get the Windows installer (4.2 GB from Microsoft, or
                  built locally from an .esd you already have)
  2  vm-create    make a VM and install Windows on it (about 45 minutes,
                  unattended — you do not click anything)
  3  app-create   push your .exe into that VM, run it, print what it said

Undo, in the same shape:

     iso-delete   remove the installer
     vm-delete    remove the VM
     app-delete   remove your .exe from the VM

When something is wrong:

     vm-screen    save a PNG of the VM's screen — the only way to see a
                  boot that is stuck, since it looks identical from here
     doctor       what is installed, what is missing, what to run next

Your .exe is anything you built with GOOS=windows GOARCH=arm64. The probes
in probe/ and glaze-probes/ are examples of that, and what this repository
uses to find out what breaks in glaze and native on Windows.
`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no subcommand given")
	}
	switch args[0] {
	case "vm-create":
		return runVMCreate(args[1:])
	case "vm-delete":
		return runVMDelete(args[1:])
	case "app-create":
		return runAppCreate(args[1:])
	case "app-delete":
		return runAppDelete(args[1:])
	case "iso-create":
		return runISOCreate(args[1:])
	case "iso-delete":
		return runISODelete(args[1:])
	case "vm-screen":
		return runVMScreen(args[1:])
	case "doctor":
		return runDoctor()
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// runSetup is the one command a new developer runs.
//
// Everything it does was already possible as eight separate calls in an order
// you had to know, with a UTM restart in the middle that nobody discovers
// alone. Every stage is idempotent, so running it twice is safe and the second
// run takes seconds — which matters because the two expensive stages are a
// 4.2 GB download and a 45-minute install.
func runVMCreate(args []string) error {
	fs := flag.NewFlagSet("vm-create", flag.ContinueOnError)
	var (
		name    = fs.String("vm", "irgo-win11", "VM name")
		install = fs.Bool("install", false, "run the unattended Windows install (about 45 minutes)")
		timeout = fs.Duration("timeout", 60*time.Minute, "overall limit for the install")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := stamped()

	// Same shape as iso-create: name every location first, then narrate each
	// step before doing it. This is the command that can take 45 minutes, so
	// silence here is the difference between waiting and wondering.
	b, _ := utmvm.BundlePath(*name)
	say("vm:     %s", *name)
	say("bundle: %s", utmvm.Home(b))
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	res, err := utmvm.VMCreate(utmvm.VMCreateOptions{
		VMName:  *name,
		Install: *install,
		Timeout: *timeout,
	}, func(line string) { fmt.Println(line) })
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

// reportISOTools says whether this machine can build its own Windows media.
//
// Reported by doctor rather than discovered by iso-create, because the answer
// changes what a new developer does first: with both tools they never touch
// CrystalFetch, and without them they should not start a download they cannot
// finish.
//
// Only two, and they are the two CrystalFetch bundles inside its own app —
// which is why it appears to need nothing.
func reportISOTools() {
	wim := utmvm.ISOWimTool()
	master, candidates := utmvm.ISOFindMasterer()

	fmt.Printf("\nbuilding your own Windows ISO needs two tools:\n\n")

	state := "MISSING — " + wim.Install()
	if wim.Found() {
		state = utmvm.Home(wim.Path)
	}
	fmt.Printf("  %-16s %s\n", wim.Name, state)

	if master.Found() {
		fmt.Printf("  %-16s %s\n", master.Name, utmvm.Home(master.Path))
	} else {
		fmt.Printf("  %-16s MISSING — %s\n", "an ISO masterer", candidates[0].Install())
	}

	if wim.Found() && master.Found() {
		fmt.Printf("\n  both present: irgo-winvm iso-create can build media.\n")
	} else {
		fmt.Printf("\n  until then, get the ISO with CrystalFetch —\n")
		fmt.Printf("  `irgo-winvm vm -list` says which build and its SHA-1.\n")
	}
}

func runDoctor() error {
	in, err := utmvm.EnsureUTM()
	if err != nil {
		return err
	}
	fmt.Printf("UTM        %s at %s\n", in.Version, in.Path)
	if !in.Compatible {
		fmt.Printf("           WARNING: schema verified against %s; a major difference\n", utmvm.VerifiedVersion)
		fmt.Printf("           is the usual cause of \"cannot import this VM\"\n")
	}
	if gt, err := utmvm.EnsureGuestTools(); err == nil {
		fmt.Printf("guest tools present (%s)\n", gt)
	} else {
		fmt.Printf("guest tools MISSING — utmctl exec will not work\n  %v\n", err)
	}
	home, _ := os.UserHomeDir()
	if sp, err := utmvm.CheckSpace(home, ""); err == nil {
		state := "ok"
		if !sp.OK {
			state = "LOW"
		}
		fmt.Printf("disk       %s (%s)\n", sp, state)
	}

	reportExternals()
	return nil
}

// reportExternals inventories everything the project needs that is not in git.
//
// It is here rather than in a document because a document cannot tell you
// whether the file is actually on this machine, and that is the only question
// worth asking. A clone of this repository is roughly 1 MB; running any of it
// needs ~33 GB that git has never seen, and every one of them fails a long way
// from its cause when absent — a missing guest-tools ISO presents as a VM with
// no network rather than as a missing ISO.
func reportExternals() {
	root, err := os.Getwd()
	if err != nil {
		root = ""
	}
	ext := utmvm.Externals(root)

	// "not in git" rather than "outside the repository": .cache and .bin sit
	// inside the working tree and are still absent from a fresh clone, which
	// is the property that matters.
	fmt.Printf("\nwhat this needs that git does not have:\n\n")
	fmt.Printf("  %-22s %-9s %-9s %s\n", "WHAT", "SIZE", "SHARED", "WHERE")
	for _, e := range ext {
		size, shared := "MISSING", ""
		if e.Present {
			size = utmvm.HumanBytes(e.Bytes)
			if e.Shared > 0 {
				shared = utmvm.HumanBytes(e.Shared)
			}
		}
		fmt.Printf("  %-22s %-9s %-9s %s\n", e.Name, size, shared, utmvm.Home(e.Path))
	}
	fmt.Printf("\n  %-22s %s\n", "total on disk", utmvm.HumanBytes(utmvm.TotalBytes(ext)))
	fmt.Printf("  SHARED is hardlinked blocks an earlier row already counted, not extra space.\n")

	reportISOTools()

	missing := utmvm.Missing(ext)
	if len(missing) == 0 {
		fmt.Printf("\nnothing missing.\n")
		return
	}
	fmt.Printf("\n%d missing:\n", len(missing))
	for _, e := range missing {
		fmt.Printf("\n  %s — %s\n", e.Name, e.Why)
		fmt.Printf("    get it: %s\n", e.Fix)
	}
}

func runVMDelete(args []string) error {
	fs := flag.NewFlagSet("vm-delete", flag.ContinueOnError)
	name := fs.String("vm", "irgo-win11", "VM name")
	force := fs.Bool("force", false, "actually delete; without this it only lists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := stamped()

	b, _ := utmvm.BundlePath(*name)
	say("STEP 1/2  the VM")
	say("          %s", utmvm.Home(b))

	e, fErr := utmvm.Find(*name)
	if fErr != nil {
		say("          UTM knows no VM %q; nothing to delete", *name)
		return nil
	}
	r, iErr := utmvm.InspectRemoval(*name)
	if iErr != nil {
		return iErr
	}
	say("          %s, %s", e.Status, utmvm.HumanBytes(r.TotalBytes))

	say("STEP 2/2  would delete:")
	say("          %-9s %s", utmvm.HumanBytes(r.TotalBytes), utmvm.Home(r.Path))
	if r.Running {
		say("          it is running and will be stopped first")
	}
	if !*force {
		return fmt.Errorf("%s of VM, and the Windows on it.\n"+
			"  Reinstalling takes about 45 minutes. Pass -force to do it",
			utmvm.HumanBytes(r.TotalBytes))
	}

	say("STEP 2/2  deleting")
	out, err := utmvm.Delete(*name, true, func(f string, a ...any) { say("          "+f, a...) })
	if err != nil {
		return err
	}
	say("removed %s — %s reclaimed", utmvm.Home(out.Path), utmvm.HumanBytes(out.TotalBytes))
	return nil
}

// runRun is the inner loop: build on the Mac, run on Windows, read output back.
func runAppCreate(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to allow the guest command")
	name := fs.String("vm", "", "VM name or UUID (required)")
	gui := fs.Bool("gui", false, "run on the guest's desktop (required for anything with a window)")
	user := fs.String("user", "dev", "guest account for -gui")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: irgo-winvm run -vm <name> <local.exe> [args...]")
	}
	e, err := utmvm.Find(*name)
	if err != nil {
		return err
	}
	vm := utmvm.Named(e.UUID)

	// Recover the VM if it is not answering. A Windows guest reboots on its own
	// — Windows Update does it — and lands back in the UEFI shell, so a run
	// cannot assume the VM is still reachable just because it was earlier.
	if !vm.AgentReady() {
		fmt.Fprintln(os.Stderr, "VM not answering; recovering...")
		if err := utmvm.EnsureReady(e.UUID, bundleOf(e), 10*time.Minute); err != nil {
			return err
		}
	}

	local := fs.Arg(0)
	res, err := utmvm.AppCreate(e.UUID, local, utmvm.AppOptions{
		Args:    fs.Args()[1:],
		GUI:     *gui,
		User:    *user,
		Timeout: *timeout,
	})
	if res.Stdout != "" {
		fmt.Println(res.Stdout)
	}
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s exited %d in the guest", filepath.Base(local), res.ExitCode)
	}
	return nil
}

// bundleOf is the CLI's one route to a VM's bundle. The layout belongs to
// utmvm; every command used to rebuild the path itself.
func bundleOf(e utmvm.Entry) string {
	p, err := utmvm.BundlePath(e.Name)
	if err != nil {
		return ""
	}
	return p
}

// runRunDelete removes what `run` put on the guest: the binaries it pushed and
// any scratch files a run that did not finish left behind.
func runAppDelete(args []string) error {
	fs := flag.NewFlagSet("app-delete", flag.ExitOnError)
	name := fs.String("vm", "", "VM name or UUID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("run-delete needs -vm")
	}
	fmt.Printf("guest: %s and %s\n", `C:\Windows\Temp`, `C:\Users\Public`)
	e, err := utmvm.Find(*name)
	if err != nil {
		// No VM means nothing was ever put on it. An undo that fails when
		// there is nothing to undo cannot be run twice.
		fmt.Printf("\nUTM knows no VM %q; nothing to delete\n", *name)
		return nil
	}
	if err := utmvm.AppDelete(e.UUID, func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }, fs.Args()...); err != nil {
		return err
	}
	fmt.Printf("cleaned %s\n", e.Name)
	return nil
}

// runISO gets the Windows media, which is the slowest and most rate-limited
// step in `vm`. Separate so it can be done once and kept.
func runISOCreate(args []string) error {
	fs := flag.NewFlagSet("iso-create", flag.ExitOnError)
	fetch := fs.Bool("fetch", false, "download from Microsoft ("+utmvm.ISODownloadSize()+") if nothing local works")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Elapsed time on every line. A multi-gigabyte download and an ISO build take
	// minutes, and without this there is no way to tell a slow step from a
	// stuck one — which is how a 77-second cached-media check went unnoticed.
	say := stamped()
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	say("STEP 0/4  the two programs an ISO build needs")
	// The two external programs building an ISO needs. Installed here, and
	// removed by iso-delete — what `iso` puts on the machine, `iso-delete`
	// takes off. Every path printed, because "installed" without a location
	// cannot be checked and cannot be undone by hand.
	for _, t := range utmvm.ISOTools() {
		say("tool:   %-16s %s", t.Name, utmvm.Home(t.Where()))
		if t.Found() {
			continue
		}
		if err := t.Ensure(); err != nil {
			return err
		}
		say("  ✓ %-16s installed at %s", t.Name, utmvm.Home(t.Path))
	}

	// No UTM, no guest tools, no VM. Getting Windows media is a download or an
	// ESD expansion; a hypervisor is not involved, and this used to run the
	// whole setup chain — so fetching an ISO required UTM to be installed first.
	iso, detail, skipped, err := utmvm.ISOGet(utmvm.ISOGetOptions{Fetch: *fetch}, say)
	if err != nil {
		return err
	}
	if skipped {
		say("media: %s (already there — %s)", utmvm.Home(iso), detail)
		return nil
	}
	say("media: %s (%s)", utmvm.Home(iso), detail)
	return nil
}

// runISODelete removes the media, which is protected on purpose, so it says so
// rather than failing with EPERM.
func runISODelete(args []string) error {
	fs := flag.NewFlagSet("iso-delete", flag.ExitOnError)
	force := fs.Bool("force", false, "actually delete; without this it only lists")
	all := fs.Bool("all", false, "also delete the .esd, the one thing that cannot be rebuilt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := stamped()

	// Everything that would go, listed as what it is — things about to be
	// deleted, not an inventory. The earlier version printed the same lines
	// under "media:" and "tool:" and then refused, which read as a status
	// report followed by an unrelated complaint.
	say("STEP 1/3  media in %s", utmvm.Home(utmvm.ISODir()))
	var files []string
	var bytes int64
	wanted := utmvm.ISODerived()
	if *all {
		wanted = utmvm.ISOFiles()
	}
	for _, f := range wanted {
		fi, err := os.Stat(f)
		if err != nil {
			continue // absent files are not news; the directory is named above
		}
		files = append(files, f)
		// Sidecars go with the file they describe rather than as entries of
		// their own: 32 bytes of cached scan result each, and listing them
		// separately made a two-file directory look like four.
		if strings.HasSuffix(f, ".scan") {
			continue
		}
		say("          %-9s %s", utmvm.HumanBytes(fi.Size()), filepath.Base(f))
		bytes += fi.Size()
	}
	if len(files) == 0 {
		say("          none")
	}
	// The .esd is reported even when it is not being deleted: "what is on this
	// machine" is the question, and a silent 4.2 GB is the answer nobody
	// expects.
	if !*all {
		if fi, err := os.Stat(utmvm.ISOSourcePath()); err == nil {
			say("          %-9s %s  (kept — pass -all to delete it)",
				utmvm.HumanBytes(fi.Size()), filepath.Base(utmvm.ISOSourcePath()))
		}
	}
	say("STEP 2/3  looking for the tools iso installed")
	tools := utmvm.ISOTools()
	var installed []int
	for i := range tools {
		if tools[i].Found() {
			say("          found %-16s %s", tools[i].Name, utmvm.Home(tools[i].Path))
			installed = append(installed, i)
			continue
		}
		say("          not installed: %s", tools[i].Name)
	}

	if len(files) == 0 && len(installed) == 0 {
		say("nothing to delete")
		say("  media would be at %s", utmvm.Home(utmvm.ISODir()))
		for i := range tools {
			say("  %s would be at %s", tools[i].Name, utmvm.Home(tools[i].Where()))
		}
		return nil
	}

	say("STEP 3/3  nothing deleted yet — this is what would go:")
	for _, f := range files {
		if strings.HasSuffix(f, ".scan") {
			continue
		}
		fi, _ := os.Stat(f)
		say("          %-9s %s", utmvm.HumanBytes(fi.Size()), filepath.Base(f))
	}
	for _, i := range installed {
		say("          %-9s %s (uninstalls %s)", "tool", utmvm.Home(tools[i].Path), tools[i].Formula)
	}

	if !*force {
		var what []string
		if len(files) > 0 {
			// Sidecars are not counted: they are 32 bytes of cache and saying
			// "2 files" for one ISO plus its scan result is just wrong.
			n := 0
			for _, f := range files {
				if !strings.HasSuffix(f, ".scan") {
					n++
				}
			}
			what = append(what, fmt.Sprintf("%d file(s), %s", n, utmvm.HumanBytes(bytes)))
		}
		if len(installed) > 0 {
			what = append(what, fmt.Sprintf("%d tool(s)", len(installed)))
		}
		msg := strings.Join(what, " and ")
		if len(files) > 0 {
			if *all {
				msg += "\n  Includes the .esd: " + utmvm.ISODownloadSize() + " to re-fetch from a source that rate-limits."
			} else {
				// 40s, measured — see RESULTS.md. It said "about three minutes"
				// from before the figure was taken.
				msg += "\n  The .esd is kept, so iso-create rebuilds this in about 40s with\n" +
					"  no network. Add -all to delete that too."
			}
		}
		return fmt.Errorf("%s\n  Pass -force to do it", msg)
	}

	say("STEP 3/3  deleting")
	for _, f := range files {
		say("          clearing the immutable flag on %s", utmvm.Home(f))
		_ = utmvm.ISOUnprotect(f) // uchg blocks unlink
		if err := os.Remove(f); err != nil {
			return err
		}
		say("  · deleted %s", utmvm.Home(f))
	}
	// The tools go whether or not the media was there: an undo has to run to
	// completion from any starting point.
	for i := range tools {
		if tools[i].Found() {
			say("          uninstalling %s (brew uninstall %s)", tools[i].Name, tools[i].Formula)
		}
		where, err := tools[i].Remove()
		switch {
		case err != nil:
			say("          · %s left in place: %v", tools[i].Name, err)
		case where != "":
			say("          · uninstalled %s from %s", tools[i].Name, utmvm.Home(where))
		}
	}
	return nil
}

// stamped returns a printer that prefixes every line with seconds elapsed.
func stamped() func(string, ...any) {
	start := time.Now()
	return func(f string, a ...any) {
		fmt.Printf("[%6.1fs] %s\n", time.Since(start).Seconds(), fmt.Sprintf(f, a...))
	}
}

// runVMScreen photographs the guest's display.
//
// The only thing that answers "what is it actually doing" when a VM is stuck:
// a failed boot leaves a UEFI prompt nobody sees, and a stalled install looks
// exactly like a working one from the host.
func runVMScreen(args []string) error {
	fs := flag.NewFlagSet("vm-screen", flag.ContinueOnError)
	name := fs.String("vm", "irgo-win11", "VM name")
	out := fs.String("o", "", "where to write the PNG (default: alongside the media)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(utmvm.VMScreensDir(), *name+".png")
	}
	say := stamped()
	say("vm:     %s", *name)
	say("shot:   %s", utmvm.Home(*out))
	if err := utmvm.Screenshot(*name, *out); err != nil {
		return err
	}
	say("written")
	return nil
}
