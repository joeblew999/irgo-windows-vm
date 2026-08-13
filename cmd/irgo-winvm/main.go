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

  setup    EVERYTHING, idempotently: media, VM, Windows. Start here.
  doctor   Check UTM, guest tools, and disk space
  list     List VMs UTM knows about
  delete   Stop and remove a VM, reporting space reclaimed
  prune    Remove generated payload images and staging leftovers
  targets  Show which desktop builds this machine can actually run
  start    Start a VM and wait until its guest agent answers
  suspend  Pause a VM with its RAM intact (resume is ~1s, no keystrokes)
  resume   Bring a suspended VM back
  boot     Start a VM and drive it past UTM's UEFI shell (one boot only)
  install  Drive an unattended install to completion, unsupervised
  run      Push a local binary into the VM, run it, print its output
           (-gui for anything with a window: WebView2, tray, menus)
  status   Report a VM's state and IP
  screenshot  Capture the VM's screen (works with no guest agent)
  exec     Run a command inside a VM
  probe    Run the bundled probes in a VM and print the report
  up       create + start + boot in one step, the usual entry point
  create   Generate a UTM bundle from a Windows ARM64 ISO  (Apple Silicon only)
  verify   Check an ISO is really ARM64 and can boot unattended
  iso      Show every name the ISO has, and -protect it from being overwritten
  fetch-iso   Download Windows media from Microsoft, SHA-1 verified
  build-iso   Turn a downloaded .esd into a bootable ISO

Run a subcommand with -h for its flags.
`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no subcommand given")
	}
	switch args[0] {
	case "setup":
		return runSetup(args[1:])
	case "create":
		return runCreate(args[1:])
	case "iso":
		return runISO(args[1:])
	case "fetch-iso":
		return runFetchISO(args[1:])
	case "build-iso":
		return runBuildISO(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "targets":
		return runTargets()
	case "start":
		return runStart(args[1:])
	case "suspend":
		return runSuspend(args[1:])
	case "resume":
		return runResume(args[1:])
	case "boot":
		return runBoot(args[1:])
	case "install":
		return runInstall(args[1:])
	case "run":
		return runRun(args[1:])
	case "status":
		return runStatus(args[1:])
	case "screenshot":
		return runScreenshot(args[1:])
	case "exec":
		return runExec(args[1:])
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
		noGPU    = fs.Bool("no-gpu", false, "drop GPU acceleration (one of two devices blocking disk snapshots)")
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
		NoGPUAccel:   *noGPU,
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
	fmt.Printf("  size         : %s\n", utmvm.HumanBytes(info.SizeBytes))

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
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}
	vm := utmvm.Named(e.UUID)
	st, err := vm.Status()
	if err != nil {
		return err
	}
	fmt.Printf("%-10s %s\n", "state:", st)

	// Phase comes from host-side signals only — block usage and whether the
	// guest has written EFI NVRAM — so it works during an install, when there
	// is no agent to ask and a screenshot is the only alternative.
	if b := bundleOf(e); b != "" {
		p := utmvm.Inspect(e.UUID, b)
		fmt.Printf("%-10s %s\n", "phase:", p.Phase)
		fmt.Printf("%-10s %d MB\n", "disk:", p.DiskMiB)
		if p.BootEntryWritten {
			fmt.Printf("%-10s yes (Setup registered its own boot entry)\n", "boot-entry:")
		}
	}

	if ips, ipErr := vm.IPAddress(); ipErr == nil {
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
	// Everything after the flags is the command, so quoted arguments and paths
	// with spaces survive. strings.Fields used to mangle them.
	argv := fs.Args()
	if len(argv) == 0 {
		if *cmdline == "" {
			return fmt.Errorf("give a command after the flags, e.g. exec -vm X -- cmd.exe /c dir")
		}
		argv = strings.Fields(*cmdline)
	}
	res, err := utmvm.RunInGuest(ref, argv, 5*time.Minute)
	if res.Stdout != "" {
		fmt.Println(res.Stdout)
	}
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("guest command exited %d", res.ExitCode)
	}
	return nil
}

// runFetchISO gets Windows install media from Microsoft's Media Creation Tool
// catalog, which — unlike the software-download page's API — is not gated.
//
// What this does today is list the catalog and download a verified ESD. What it
// pairs with build-iso, which turns the .esd into bootable media.
func runFetchISO(args []string) error {
	fs := flag.NewFlagSet("fetch-iso", flag.ContinueOnError)
	var (
		catalog = fs.String("catalog", "", "an already-extracted products.xml (see -h notes on LZX)")
		arch    = fs.String("arch", "ARM64", "architecture: ARM64 or x64")
		lang    = fs.String("lang", "en-us", "language code, e.g. en-us, en-gb")
		edition = fs.String("edition", "CLIENTCONSUMER", "edition substring to match in the filename")
		list    = fs.Bool("list", false, "list matching images and stop")
		out     = fs.String("o", "", "download the matching ESD to this path (default: under IRGO_CACHE_DIR)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	all, src, err := loadCatalog(*catalog)
	if err != nil {
		return err
	}
	fmt.Printf("catalog: %d images (%s)\n", len(all), src)

	matches := utmvm.FilterCatalog(all, *arch, *lang, *edition)
	if len(matches) == 0 {
		return fmt.Errorf("nothing matched arch=%s lang=%s edition=%s", *arch, *lang, *edition)
	}
	for _, e := range matches {
		fmt.Printf("\n  %s\n", e.FileName)
		fmt.Printf("    build    %s\n", e.Build())
		fmt.Printf("    size     %s\n", utmvm.HumanBytes(e.Size))
		fmt.Printf("    sha1     %s\n", e.Sha1)
	}

	if *list || *out == "" {
		fmt.Printf("\nThese are .esd archives, not bootable ISOs. The whole sequence is:\n\n")
		fmt.Printf("  irgo-winvm fetch-iso -o win11.esd     # this, with a destination\n")
		fmt.Printf("  irgo-winvm build-iso  -esd win11.esd  # -> a bootable ISO\n")
		return nil
	}
	if len(matches) > 1 {
		return fmt.Errorf("%d images matched; narrow it with -lang or -edition before downloading", len(matches))
	}

	e := matches[0]
	fmt.Printf("\ndownloading to %s\n", *out)
	err = utmvm.Download(e.FilePath, *out, e.Sha1, func(done, total int64) {
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total) * 100
		}
		fmt.Printf("\r  %s / %s (%.1f%%)   ", utmvm.HumanBytes(done), utmvm.HumanBytes(total), pct)
	})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("verified sha1 %s\n", e.Sha1)
	fmt.Printf("\nnow build a bootable ISO from it:\n  irgo-winvm build-iso -esd %s\n", *out)
	return nil
}

// loadCatalog prefers an explicit file, then the network, then a catalog some
// other tool already extracted — in that order, because the first two are
// reproducible and the third is whatever happens to be on this machine.
func loadCatalog(explicit string) ([]utmvm.CatalogEntry, string, error) {
	if explicit != "" {
		b, err := os.ReadFile(explicit) //nolint:gosec // the user named this file
		if err != nil {
			return nil, "", err
		}
		all, err := utmvm.ParseCatalogXML(b)
		return all, explicit, err
	}

	all, netErr := utmvm.FetchCatalog(2 * time.Minute)
	if netErr == nil {
		return all, utmvm.CatalogURL, nil
	}

	for _, p := range utmvm.CachedCatalogPaths() {
		b, err := os.ReadFile(p) //nolint:gosec // a known cache location
		if err != nil {
			continue
		}
		parsed, pErr := utmvm.ParseCatalogXML(b)
		if pErr != nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "note: %v\n      using a cached catalog instead: %s\n\n", netErr, utmvm.Home(p))
		return parsed, p, nil
	}
	return nil, "", netErr
}

// runSuspend pauses a VM with its RAM intact.
//
// Measured: suspend, then resume, and the guest agent answers again in **one
// second** — against about two minutes for a cold boot, which additionally has
// to be DRIVEN through the UEFI shell with eight keypresses and therefore needs
// an unlocked Mac and a visible display window.
//
// The catch, and it is the whole of PLAN item 3: the state is in MEMORY. Quit
// UTM or reboot the Mac and it is gone. `-save` asks for the durable version
// and is refused on this hardware; see the note it prints.
func runSuspend(args []string) error {
	fs := flag.NewFlagSet("suspend", flag.ContinueOnError)
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}
	vm := utmvm.Named(e.UUID)

	if sErr := vm.Suspend(); sErr != nil {
		return sErr
	}
	fmt.Printf("%s suspended (in memory — resume with: irgo-winvm resume -vm %s)\n", e.Name, e.Name)
	fmt.Printf("  Do not quit UTM: the state is RAM, not a file.\n")
	return nil
}

// runResume brings a suspended VM back, or boots a stopped one.
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the guest agent")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}
	vm := utmvm.Named(e.UUID)

	// A stopped VM has no state to restore, so `start` would boot it — into the
	// UEFI shell, where it would sit forever, because the firmware never
	// auto-selects a boot entry. Saying so beats appearing to hang.
	if !vm.IsPaused() {
		return fmt.Errorf("%s is %q, not suspended — there is nothing to resume.\n"+
			"  A stopped VM needs its firmware driven: irgo-winvm boot -vm %s -installed",
			e.Name, e.Status, e.Name)
	}

	start := time.Now()
	if rErr := vm.Resume(); rErr != nil {
		return rErr
	}
	// 100ms: a resume is back in about 400ms, and this is the number the
	// command reports, so the poll interval is the measurement's resolution.
	if wErr := vm.WaitForAgentEvery(*wait, 100*time.Millisecond); wErr != nil {
		fmt.Printf("%s resumed, but the agent has not answered within %s\n", e.Name, *wait)
		return nil
	}
	fmt.Printf("%s resumed in %s — no firmware, no keystrokes\n",
		e.Name, time.Since(start).Round(time.Millisecond*100))
	return nil
}

// runSetup is the one command a new developer runs.
//
// Everything it does was already possible as eight separate calls in an order
// you had to know, with a UTM restart in the middle that nobody discovers
// alone. Every stage is idempotent, so running it twice is safe and the second
// run takes seconds — which matters because the two expensive stages are a
// 4.2 GB download and a 45-minute install.
func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	paths := utmvm.DefaultPaths()
	var (
		name    = fs.String("vm", "irgo-win11", "VM name")
		iso     = fs.String("iso", "", "media to use (default: find one, or build with -fetch)")
		probes  = fs.String("probes", "", "probe binaries to embed (default: the .bin directory)")
		fetch   = fs.Bool("fetch", false, "download 4.2 GB from Microsoft if no media is present")
		install = fs.Bool("install", false, "run the unattended Windows install (about 45 minutes)")
		timeout = fs.Duration("timeout", 60*time.Minute, "overall limit for the install")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *probes == "" {
		if _, err := os.Stat(paths.Bin); err == nil {
			*probes = paths.Bin
		}
	}

	fmt.Printf("setting up %s\n\n", *name)
	res, err := utmvm.Setup(utmvm.SetupOptions{
		VMName:   *name,
		ISO:      *iso,
		ProbeDir: *probes,
		Fetch:    *fetch,
		Install:  *install,
		Timeout:  *timeout,
	}, paths, func(line string) { fmt.Println(line) })
	if err != nil {
		return err
	}

	fmt.Println()
	if res.Ready {
		fmt.Printf("%s is ready.\n\n", res.VM)
		fmt.Printf("  irgo-winvm probe -vm %s          run the native capability probes\n", res.VM)
		fmt.Printf("  irgo-winvm run -gui -vm %s <exe> run a windowed binary\n", res.VM)
		return nil
	}
	fmt.Printf("%s is not ready yet — see the stages above for what remains.\n", res.VM)
	return nil
}

// runBuildISO turns a downloaded ESD, or a directory of media, into a bootable
// ISO — the half of replacing CrystalFetch that is not downloading.
//
// Measured working on 12 Aug 2026: a self-built ISO booted UTM straight into
// Windows Setup, which then installed from it unattended. See RESULTS.md.
func runBuildISO(args []string) error {
	fs := flag.NewFlagSet("build-iso", flag.ContinueOnError)
	paths := utmvm.DefaultPaths()
	var (
		esd   = fs.String("esd", "", "the .esd downloaded by fetch-iso")
		from  = fs.String("from", "", "a directory of media already laid out (skips the .esd step)")
		out   = fs.String("o", "", "the ISO to write (default: <cache>/win11-arm64-built.iso)")
		label = fs.String("label", "", "volume label (default: taken from the media, else WINDOWS_ARM64)")
		keep  = fs.Bool("keep", false, "keep the expanded media directory instead of removing it")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*esd == "") == (*from == "") {
		fs.Usage()
		return fmt.Errorf("give exactly one of -esd or -from")
	}
	if *out == "" {
		*out = filepath.Join(paths.Cache, "win11-arm64-built.iso")
	}

	// Refuse before doing an hour of work, not after.
	if err := paths.CheckWritable(*out); err != nil {
		return err
	}

	dir := *from
	if *esd != "" {
		work, err := paths.EnsureWork(12 << 30) // media tree plus the ISO
		if err != nil {
			return err
		}
		dir = filepath.Join(work, "media")
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Printf("expanding %s\n", filepath.Base(*esd))
		if err := utmvm.ExpandESD(*esd, dir, func(step string) {
			fmt.Printf("  %s\n", step)
		}); err != nil {
			return err
		}
		if !*keep {
			// Deliberately after the ISO is written, not here.
			defer func() {
				fmt.Printf("removing %s (-keep to retain it)\n", dir)
				_ = os.RemoveAll(dir)
			}()
		}
	}

	if *label == "" {
		*label = "WINDOWS_ARM64"
	}
	return utmvm.BuildISO(utmvm.RemasterOptions{
		Source:   dir,
		Output:   *out,
		Label:    *label,
		NoPrompt: true,
	}, paths)
}

// reportISOTools says whether this machine can build its own Windows media.
//
// Reported by doctor rather than discovered by build-iso, because the answer
// changes what a new developer does first: with both tools they never touch
// CrystalFetch, and without them they should not start a download they cannot
// finish.
//
// Only two, and they are the two CrystalFetch bundles inside its own app —
// which is why it appears to need nothing.
func reportISOTools() {
	wim := utmvm.WimTool()
	master, candidates := utmvm.FindMasterer()

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
		fmt.Printf("\n  both present: irgo-winvm fetch-iso, then build-iso.\n")
	} else {
		fmt.Printf("\n  until then, get the ISO with CrystalFetch —\n")
		fmt.Printf("  `irgo-winvm fetch-iso -list` says which build and its SHA-1.\n")
	}
}

// runISO reports and protects the working ISO.
//
// It exists because the ISO is hardlinked into UTM's bundle, so it is not the
// spare copy it looks like: writing to the cached path truncates the media the
// VM boots from, and the only way back is a 5 GB gated download. -protect
// makes that impossible rather than merely unlikely, which matters most while
// `fetch-iso` — a command whose entire job is writing an ISO — does not exist
// yet and cannot be reviewed.
func runISO(args []string) error {
	fs := flag.NewFlagSet("iso", flag.ContinueOnError)
	path := fs.String("iso", utmvm.DefaultPaths().ISO(), "the ISO to report on (IRGO_CACHE_DIR moves the default)")
	protect := fs.Bool("protect", false, "make it immutable: no write, truncate, rename or delete")
	unprotect := fs.Bool("unprotect", false, "clear the immutable flag (needed before `delete` can remove a VM using it)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *protect && *unprotect {
		return fmt.Errorf("-protect and -unprotect are opposites; pick one")
	}
	switch {
	case *protect:
		if err := utmvm.ProtectISO(*path); err != nil {
			return err
		}
	case *unprotect:
		if err := utmvm.UnprotectISO(*path); err != nil {
			return err
		}
	}

	st, err := utmvm.ISOLinks(*path, utmvm.ISOSearchDirs())
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", st.Path)
	fmt.Printf("  size       %s\n", utmvm.HumanBytes(st.Bytes))
	if st.Protected {
		fmt.Printf("  protected  YES — immutable; clear it with -unprotect before replacing or deleting\n")
	} else {
		fmt.Printf("  protected  no — anything writing this path truncates every name below\n")
	}
	fmt.Printf("  names      %d according to the filesystem; %d found:\n", st.Links, len(st.Found))
	for _, p := range st.Found {
		fmt.Printf("               %s\n", utmvm.Home(p))
	}
	if st.Links > len(st.Found) {
		fmt.Printf("             (%d more exist outside ~/Downloads and UTM's bundles —\n", st.Links-len(st.Found))
		fmt.Printf("              a Time Machine local snapshot, usually. Not a problem.)\n")
	}
	fmt.Printf("\n  These are ONE file with several names, not copies. 5 GB once.\n")

	// Every other Windows image on the machine, and whether anything uses it.
	//
	// These are 5 GB each and are named after a Windows build rather than
	// anything a person would recognise, so a second one downloaded months ago
	// is invisible until the disk fills. "Which of these is the one that works"
	// is not answerable from the filenames; "does it share blocks with a VM
	// bundle" is, and that is what this reports.
	const minWindowsISO = 1 << 30
	found := utmvm.ScanISOs([]string{"."}, minWindowsISO)
	if len(found) > 1 {
		fmt.Printf("\nother Windows images on this machine:\n\n")
		fmt.Printf("  %-9s %-7s %s\n", "SIZE", "USED", "WHERE")
		var idle int64
		for _, f := range found {
			used := "no"
			if f.InUse {
				used = "yes"
			} else {
				idle += f.Bytes
			}
			fmt.Printf("  %-9s %-7s %s\n", utmvm.HumanBytes(f.Bytes), used, utmvm.Home(f.Path))
		}
		if idle > 0 {
			fmt.Printf("\n  %s is in images no VM refers to. Check them before deleting —\n", utmvm.HumanBytes(idle))
			fmt.Printf("  a different language or build is a deliberate spare, not litter.\n")
		}
	}
	return nil
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
		fmt.Printf("would remove %s (%s reclaimed, running=%v)\n",
			r.Path, utmvm.HumanBytes(r.TotalBytes), r.Running)
		return nil
	}
	r, err := utmvm.Delete(ref, *force)
	if err != nil {
		return err
	}
	fmt.Printf("removed %s — %s reclaimed\n", r.Path, utmvm.HumanBytes(r.TotalBytes))
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
	diskPath := utmvm.DiskPath(bundleOf(e))

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

// runScreenshot captures the guest's display. This is the only way to see a VM
// that has no guest agent yet — during install, in the UEFI shell, or any time
// exec is unavailable.
// runScreenshot captures the VM's screen, defaulting into the documentation
// directory rather than the working directory.
//
// The default matters more than it looks. This is the only way to show that any
// of this works — a guest agent cannot photograph a machine that has not
// finished installing, and a claim like "it boots straight into Setup" is worth
// nothing without the picture. Landing shots in docs/screens by default means
// the evidence is already where it belongs when somebody decides to keep it,
// instead of scattered through the working directory under timestamps nobody
// can match to anything afterwards.
func runScreenshot(args []string) error {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	out := fs.String("o", "", "exact output path; overrides -name")
	name := fs.String("name", "", "file name under the screens directory (default: <vm>-<timestamp>)")
	ref, err := vmRef(fs, args)
	if err != nil {
		return err
	}
	e, err := utmvm.Find(ref)
	if err != nil {
		return err
	}

	paths := utmvm.DefaultPaths()
	target := *out
	if target == "" {
		n := *name
		if n == "" {
			// A timestamp, not a fixed name: an unnamed shot is a scratch shot,
			// and overwriting the last one is how evidence goes missing.
			n = fmt.Sprintf("%s-%d", e.Name, time.Now().Unix())
		}
		target, err = paths.Screenshot(n)
		if err != nil {
			return err
		}
	} else if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
		return mkErr
	}

	if err := utmvm.Screenshot(e.Name, target); err != nil {
		return err
	}
	fmt.Println(target)
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
	return utmvm.RunInstall(utmvm.InstallOptions{
		VMRef:      e.UUID,
		BundlePath: bundleOf(e),
		Timeout:    *wait,
		Log:        os.Stdout,
	})
}

// runRun is the inner loop: build on the Mac, run on Windows, read output back.
func runRun(args []string) error {
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
	res, err := utmvm.Run(e.UUID, local, utmvm.RunOptions{
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
