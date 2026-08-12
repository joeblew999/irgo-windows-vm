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
	"runtime"
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
	fmt.Fprint(os.Stderr, `irgo-winvm — create and inspect Windows 11 ARM64 VMs for UTM

  doctor   Check UTM, guest tools, and disk space
  list     List VMs UTM knows about
  delete   Stop and remove a VM, reporting space reclaimed
  prune    Remove generated payload images and staging leftovers
  targets  Show which desktop builds this machine can actually run
  start    Start a VM and wait until its guest agent answers
  boot     Start a VM and drive it past UTM's UEFI shell (one boot only)
  install  Drive an unattended install to completion, unsupervised
  status   Report a VM's state and IP
  screenshot  Capture the VM's screen (works with no guest agent)
  exec     Run a command inside a VM
  probe    Run the bundled probes in a VM and print the report
  up       create + start + boot in one step, the usual entry point
  create   Generate a UTM bundle from a Windows ARM64 ISO  (Apple Silicon only)
  verify   Check an ISO is really ARM64 and can boot unattended

Run a subcommand with -h for its flags.
`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no subcommand given")
	}
	switch args[0] {
	case "create":
		return runCreate(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "targets":
		return runTargets()
	case "start":
		return runStart(args[1:])
	case "boot":
		return runBoot(args[1:])
	case "up":
		return runUp(args[1:])
	case "install":
		return runInstall(args[1:])
	case "status":
		return runStatus(args[1:])
	case "screenshot":
		return runScreenshot(args[1:])
	case "exec":
		return runExec(args[1:])
	case "probe":
		return runProbe(args[1:])
	case "doctor":
		return runDoctor()
	case "list":
		return runList()
	case "delete":
		return runDelete(args[1:])
	case "prune":
		return runPrune()
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	var (
		iso      = fs.String("iso", "", "path to the Windows 11 ARM64 ISO (required)")
		unattend = fs.String("unattend", "", "prebuilt answer medium; omit to generate one")
		probeDir = fs.String("probes", "", "directory of Windows test binaries to embed")
		noTools  = fs.Bool("no-guest-tools", false, "skip guest tools (utmctl exec will not work)")
		name     = fs.String("name", "Win11ARM", "VM name; also the bundle filename")
		out      = fs.String("out", "", "parent directory (default: UTM's Documents folder)")
		diskGiB  = fs.Int("disk", 64, "system disk size in GiB (sparse — costs nothing until used)")
		memMiB   = fs.Int("memory", 8192, "RAM in MiB")
		cpus     = fs.Int("cpus", 4, "vCPU count")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *iso == "" {
		fs.Usage()
		return fmt.Errorf("-iso is required")
	}
	// Refuse early and explain, rather than failing later on a missing UTM
	// directory. A developer on Windows is on the narrower machine, not doing
	// something wrong.
	if !utmvm.CanCreateVMs() {
		return fmt.Errorf("VM creation needs macOS on Apple Silicon; run `irgo-winvm targets` to see what this machine can test")
	}

	// Fail on a mismatched ISO before writing anything: an x86-64 image boots
	// to a black screen on Apple Silicon with no diagnostic at all.
	info, err := utmvm.InspectISO(*iso)
	if err != nil {
		return fmt.Errorf("inspecting ISO: %w", err)
	}
	if !info.IsARM64 {
		return fmt.Errorf("%s is not an ARM64 ISO (no efi/boot/bootaa64.efi); it cannot boot on Apple Silicon",
			filepath.Base(*iso))
	}

	bundle, err := utmvm.Create(utmvm.Options{
		Name:         *name,
		InstallISO:   *iso,
		UnattendISO:  *unattend,
		ProbeDir:     *probeDir,
		NoGuestTools: *noTools,
		OutDir:       *out,
		DiskGiB:      *diskGiB,
		MemoryMiB:    *memMiB,
		CPUCount:     *cpus,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Created %s\n", bundle)
	fmt.Printf("  disk %d GiB (sparse) · %d MiB RAM · %d vCPU\n", *diskGiB, *memMiB, *cpus)
	fmt.Println("  unattend medium generated (autounattend.xml + startup.nsh)")
	if !*noTools {
		fmt.Println("  guest tools attached — utmctl exec will work once installed")
	}
	fmt.Printf("\nUTM only rescans its folder at launch — quit and reopen UTM, then:\n  utmctl start %q\n", *name)
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	iso := fs.String("iso", "", "path to the ISO to inspect (required)")
	want := fs.String("edition", "", "edition that an answer file selects, e.g. \"Windows 11 Pro\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *iso == "" {
		fs.Usage()
		return fmt.Errorf("-iso is required")
	}

	info, err := utmvm.InspectISO(*iso)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", filepath.Base(*iso))
	if info.IsARM64 {
		fmt.Println("  architecture : ARM64 (efi/boot/bootaa64.efi present)")
	} else {
		fmt.Println("  architecture : NOT ARM64 — will not boot on Apple Silicon")
	}
	if info.HasNoPromptLoader {
		fmt.Println("  autoboot     : cdboot_noprompt.efi present — Setup can start without a keypress")
	} else {
		fmt.Println("  autoboot     : cdboot_noprompt.efi MISSING — Setup will wait at \"Press any key\"")
	}
	fmt.Printf("  size         : %.1f GB\n", float64(info.SizeBytes)/(1<<30))

	if *want != "" {
		// Edition names live inside install.wim behind WIM compression; this
		// tool deliberately does not parse that. Point at the tool that can.
		fmt.Printf("\nEdition check is not implemented (install.wim is WIM-compressed).\n")
		fmt.Printf("Confirm %q with:  wimlib-imagex info <mount>/sources/install.wim\n", *want)
	}
	if !info.IsARM64 {
		return fmt.Errorf("wrong architecture")
	}
	return nil
}

// runTargets prints the honest capability matrix for this host.
func runTargets() error {
	fmt.Printf("Desktop builds runnable on this machine (%s/%s):\n\n", runtimeGOOS(), runtimeGOARCH())
	for _, c := range utmvm.HostCoverage() {
		mark, how := "  no", "—"
		if c.Runnable() {
			mark, how = " yes", c.How
		}
		fmt.Printf("  %s  %-8s %-7s %s\n", mark, c.Target, how, c.Note)
	}
	if !utmvm.CanCreateVMs() {
		fmt.Println("\nThis machine cannot create Windows VMs. Build the Windows probes here")
		fmt.Println("and run them on a Windows box, or use an Apple Silicon Mac, which can do both.")
	}
	return nil
}

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// vmRef pulls the target VM from a flag set, so every VM-facing subcommand
// takes the same -vm.
func vmRef(fs *flag.FlagSet, args []string) (string, error) {
	name := fs.String("vm", "", "VM name or UUID (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *name == "" {
		fs.Usage()
		return "", fmt.Errorf("-vm is required")
	}
	return *name, nil
}

func runStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for the guest agent")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	vm := utmvm.Named(ref)
	if err := vm.Start(); err != nil {
		return err
	}
	fmt.Printf("started %s; waiting for the guest agent...\n", ref)
	if err := vm.WaitForAgent(*wait); err != nil {
		return err
	}
	ips, _ := vm.IPAddress()
	fmt.Printf("ready — %s\n", strings.Join(ips, ", "))
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	vm := utmvm.Named(ref)
	st, err := vm.Status()
	if err != nil {
		return err
	}
	fmt.Printf("%-10s %s\n", "state:", st)
	if ips, err := vm.IPAddress(); err == nil {
		fmt.Printf("%-10s %s\n", "ip:", strings.Join(ips, ", "))
		fmt.Printf("%-10s yes\n", "agent:")
	} else {
		fmt.Printf("%-10s no (guest tools not installed, or still booting)\n", "agent:")
	}
	return nil
}

func runExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	cmdline := fs.String("cmd", "", "command to run in the guest (required)")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	if *cmdline == "" {
		return fmt.Errorf("-cmd is required")
	}
	out, err := utmvm.Named(ref).Exec(strings.Fields(*cmdline)...)
	fmt.Println(out)
	return err
}

// runProbe runs the probe suite that was baked onto the unattend medium.
func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	drive := fs.String("drive", "", "drive letter of the payload medium; probed automatically when empty")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	vm := utmvm.Named(ref)
	if !vm.AgentReady() {
		return fmt.Errorf("guest agent is not answering; run `irgo-winvm status -vm %s`", ref)
	}
	letters := []string{"D", "E", "F", "G", "H"}
	if *drive != "" {
		letters = []string{strings.TrimSuffix(*drive, ":")}
	}
	for _, l := range letters {
		// Binaries sit at the medium's root, not in a subdirectory: go-diskfs
		// mangles Joliet names inside nested directories into UCS-2 garbage,
		// while root entries survive intact.
		out, err := vm.Exec("cmd", "/c", l+`:\run-all.cmd`)
		if err == nil {
			fmt.Println(out)
			return nil
		}
	}
	return fmt.Errorf("could not find run-all.cmd on any of %v", letters)
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
	return nil
}

func runList() error {
	vms, err := utmvm.List()
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Println("no VMs (UTM only rescans its folder at launch)")
		return nil
	}
	fmt.Printf("%-38s %-9s %s\n", "UUID", "STATE", "NAME")
	for _, v := range vms {
		fmt.Printf("%-38s %-9s %s\n", v.UUID, v.Status, v.Name)
	}
	return nil
}

func runDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	force := fs.Bool("force", false, "stop the VM first if it is running")
	dry := fs.Bool("dry-run", false, "report what would be removed and stop")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	if *dry {
		r, err := utmvm.InspectRemoval(ref)
		if err != nil {
			return err
		}
		fmt.Printf("would remove %s (%.1f GB reclaimed, running=%v)\n",
			r.Path, float64(r.TotalBytes)/(1<<30), r.Running)
		return nil
	}
	r, err := utmvm.Delete(ref, *force)
	if err != nil {
		return err
	}
	fmt.Printf("removed %s — %.1f GB reclaimed\n", r.Path, float64(r.TotalBytes)/(1<<30))
	return nil
}

func runPrune() error {
	tmp := os.TempDir()
	freed, removed, err := utmvm.Prune(tmp)
	if err != nil {
		return err
	}
	for _, p := range removed {
		fmt.Println("removed", p)
	}
	fmt.Printf("%.1f MB reclaimed\n", float64(freed)/(1<<20))
	return nil
}

// runBoot exists because UTM's aarch64 firmware never auto-selects a boot
// entry; it drops to the UEFI shell on every boot, including after Windows is
// installed. This types the boot path the way a person would.
func runBoot(args []string) error {
	fs := flag.NewFlagSet("boot", flag.ContinueOnError)
	installed := fs.Bool("installed", false, "boot an installed Windows rather than the installer")
	wait := fs.Duration("wait", 15*time.Minute, "how long to wait for signs of life")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}
	dir, err := utmvm.DefaultVMDir()
	if err != nil {
		return err
	}
	diskPath := filepath.Join(dir, e.Name+".utm", "Data", "disk.img")

	target := utmvm.BootInstaller
	if *installed {
		target = utmvm.BootInstalled
	}
	fmt.Printf("starting %s and driving the UEFI shell...\n", e.Name)
	if err := utmvm.BootAndWait(e.UUID, target, diskPath, *wait); err != nil {
		return err
	}
	fmt.Println("boot took — Setup is running or the guest agent answered")
	return nil
}

// runUp is the single command that takes an ISO to a running Windows VM.
//
// The separate steps exist because each one can fail in its own way and is
// worth retrying alone, but nobody wants to type four commands to get started.
func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	var (
		iso     = fs.String("iso", "", "path to the Windows 11 ARM64 ISO (required)")
		name    = fs.String("name", "irgo-win11", "VM name")
		probes  = fs.String("probes", "", "directory of Windows test binaries to embed")
		wait    = fs.Duration("wait", 45*time.Minute, "how long to allow for the install")
		replace = fs.Bool("replace", false, "delete an existing VM of the same name first")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *iso == "" {
		fs.Usage()
		return fmt.Errorf("-iso is required")
	}
	if !utmvm.CanCreateVMs() {
		return fmt.Errorf("this needs macOS on Apple Silicon; see `irgo-winvm targets`")
	}

	if *replace {
		if _, err := utmvm.Delete(*name, true); err == nil {
			fmt.Printf("removed the existing %s\n", *name)
		}
	}

	info, err := utmvm.InspectISO(*iso)
	if err != nil {
		return err
	}
	if !info.IsARM64 {
		return fmt.Errorf("%s is not an ARM64 ISO; it cannot boot on Apple Silicon", filepath.Base(*iso))
	}

	bundle, err := utmvm.Create(utmvm.Options{Name: *name, InstallISO: *iso, ProbeDir: *probes})
	if err != nil {
		return err
	}
	fmt.Printf("created %s\n", bundle)

	// UTM only rescans its bundle directory at launch, so a VM generated while
	// UTM is running is invisible to it until restarted.
	fmt.Println("restarting UTM so it picks up the new VM...")
	if err := utmvm.RestartUTM(); err != nil {
		return err
	}

	// Resolve to the UUID before booting. BootAndWait ultimately renders
	// assets/boot.applescript, whose specifier is `virtual machine id %q` —
	// AppleScript's id form does not accept a name, so passing *name here made
	// every `up` run fail at the boot step while `boot` (which resolves first)
	// worked.
	e, err := utmvm.Find(*name)
	if err != nil {
		return err
	}

	// Drive the whole install, not just the first boot. Setup reboots partway
	// and lands back in the UEFI shell needing a different boot; handling only
	// the first is what made every previous run need a human.
	fmt.Println("installing (both boot phases, unsupervised)...")
	if err := utmvm.RunInstall(utmvm.InstallOptions{
		VMRef:      e.UUID,
		BundlePath: bundle,
		Timeout:    *wait,
		Log:        os.Stdout,
	}); err != nil {
		return err
	}
	fmt.Printf("\n%s is ready. Try:  irgo-winvm probe -vm %s\n", *name, *name)
	return nil
}

// runScreenshot captures the guest's display. This is the only way to see a VM
// that has no guest agent yet — during install, in the UEFI shell, or any time
// exec is unavailable.
func runScreenshot(args []string) error {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	out := fs.String("o", "", "output PNG path (default: ./<vm>-<timestamp>.png)")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = fmt.Sprintf("%s-%d.png", e.Name, time.Now().Unix())
	}
	if err := utmvm.Screenshot(e.Name, path); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// runInstall drives an install with no supervision, through both boot phases.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	wait := fs.Duration("wait", 45*time.Minute, "overall timeout")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}
	dir, err := utmvm.DefaultVMDir()
	if err != nil {
		return err
	}
	return utmvm.RunInstall(utmvm.InstallOptions{
		VMRef:      e.UUID,
		BundlePath: filepath.Join(dir, e.Name+".utm"),
		Timeout:    *wait,
		Log:        os.Stdout,
	})
}
