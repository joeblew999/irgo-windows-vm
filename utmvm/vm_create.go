package utmvm

// One command, from a fresh clone to a Windows VM that answers.
//
// Everything this project does was already possible as eight or nine separate
// calls, in an order you had to know, with waits between them that are not
// obvious and a UTM restart in the middle that is not discoverable at all. That
// is a tool for the person who wrote it.
//
// Setup is the same work as one idempotent step. Every stage asks whether it is
// already done and skips if so, which means:
//
//   - running it twice is safe, and the second run is seconds rather than an hour;
//   - an interrupted run is resumed by running it again, not restarted;
//   - it is the same command whether you have nothing, an ISO, or a half-built VM.
//
// Idempotency here is not a nicety. The expensive stages are a 4.2 GB download
// and a 45-minute unattended install, and a setup command that redoes either
// because something later failed is one nobody will run twice.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SetupOptions configures Setup.
type SetupOptions struct {
	VMName   string
	ISO      string // media to use; empty means "find or build one"
	ProbeDir string // Windows probe binaries to embed
	Timeout  time.Duration

	// Fetch permits downloading 4.2 GB from Microsoft when no ISO is present.
	// Off by default: a command that starts a multi-gigabyte download because
	// you ran it in the wrong directory is a bad command.
	Fetch bool

	// Install drives the unattended Windows installation, which takes about 45
	// minutes. Off by default for the same reason.
	Install bool
}

// SetupStage is one step, and what happened to it.
type SetupStage struct {
	Name    string
	Skipped bool // already satisfied
	Detail  string
	Err     error
}

// SetupResult is the whole run.
type SetupResult struct {
	Stages []SetupStage
	ISO    string
	VM     string
	Ready  bool // the guest agent answered
}

// Setup takes a machine from wherever it is to a Windows VM that answers.
//
// It returns the stages it went through, including the ones it skipped, because
// "nothing to do" is the most valuable thing an idempotent command can tell you
// and is indistinguishable from "did nothing" unless it says so.
func Setup(opts SetupOptions, paths Paths, log func(string)) (SetupResult, error) {
	var res SetupResult
	began := time.Now()
	say := func(f string, a ...any) {
		if log != nil {
			log(fmt.Sprintf("[%6.1fs] ", time.Since(began).Seconds()) + fmt.Sprintf(f, a...))
		}
	}
	last := time.Now()
	stage := func(name string, skipped bool, detail string, err error) error {
		took := time.Since(last)
		last = time.Now()
		if took > 200*time.Millisecond {
			detail = fmt.Sprintf("%s  [%s]", detail, took.Round(time.Millisecond))
		}
		res.Stages = append(res.Stages, SetupStage{name, skipped, detail, err})
		switch {
		case err != nil:
			say("✗ %s: %v", name, err)
		case skipped:
			say("· %s — already done (%s)", name, detail)
		default:
			say("✓ %s — %s", name, detail)
		}
		return err
	}

	if opts.VMName == "" {
		opts.VMName = "irgo-win11"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Minute
	}
	res.VM = opts.VMName

	// 1. The hypervisor. Nothing else can be checked without it.
	say("… checking UTM")
	in, err := EnsureUTM()
	if err != nil {
		return res, stage("UTM", false, "", err)
	}
	if err := stage("UTM", true, in.Version+" at "+in.Path, nil); err != nil {
		return res, err
	}

	// 2. The guest tools. Skipped VMs boot fine and are then unreachable —
	// no network, no exec, no IP — so this is checked before anything is built.
	hadTools := true
	if _, tErr := GuestToolsISO(); tErr != nil {
		hadTools = false
		say("… downloading UTM guest tools (~120 MB)")
	}
	gt, gErr := EnsureGuestTools()
	if gErr != nil {
		return res, stage("guest tools", false, "", gErr)
	}
	if err := stage("guest tools", hadTools, filepath.Base(gt), nil); err != nil {
		return res, err
	}

	// 3. ISOGet. Three ways to already have it, in order of preference, then
	// building one.
	say("… checking Windows media (scans the ISO the first time; cached after)")
	iso, isoDetail, isoSkipped, err := ISOGet(ISOGetOptions{ISO: opts.ISO, Fetch: opts.Fetch}, say)
	if err != nil {
		return res, stage("Windows media", false, "", err)
	}
	if err := stage("Windows media", isoSkipped, isoDetail, nil); err != nil {
		return res, err
	}
	res.ISO = iso

	// 4. Protect it. Free, and the difference between a slip and a 4.2 GB
	// re-download of something rate-limited.
	if st, sErr := isoLinks(iso, nil); sErr == nil && st.Protected {
		_ = stage("protect the media", true, "immutable", nil)
	} else if pErr := isoProtect(iso); pErr != nil {
		// Not fatal: an unprotected ISO still works, it is just easier to lose.
		_ = stage("protect the media", false, "could not: "+pErr.Error(), nil)
	} else {
		_ = stage("protect the media", false, "now immutable", nil)
	}

	// 5. Permission to drive UTM, asked before anything expensive.
	//
	// This is the one prerequisite that cannot be installed: it is a consent
	// dialog. Asked here rather than at boot time so a missing grant costs a
	// second instead of forty minutes of install followed by a timeout that
	// says nothing about permissions.
	if aErr := CheckAutomation(); aErr != nil {
		return res, stage("control UTM", false, "", aErr)
	}
	_ = stage("control UTM", true, "permitted", nil)

	// 6. The VM bundle.
	existing, findErr := Find(opts.VMName)
	if findErr == nil {
		_ = stage("VM bundle", true, opts.VMName+" ("+existing.Status+")", nil)
	} else {
		if _, cErr := Create(Options{
			Name:       opts.VMName,
			InstallISO: iso,
			ProbeDir:   opts.ProbeDir,
		}); cErr != nil {
			return res, stage("VM bundle", false, "", cErr)
		}
		_ = stage("VM bundle", false, "created "+opts.VMName, nil)

		// UTM enumerates its bundle directory only at launch, so a VM written
		// while it is running does not exist as far as utmctl is concerned —
		// with no error saying so. This is the step nobody discovers alone.
		say("… restarting UTM so it sees the new VM")
		if rErr := RestartUTM(); rErr != nil {
			return res, stage("restart UTM", false, "", rErr)
		}
		_ = stage("restart UTM", false, "rescanned", nil)
	}

	// 6. Is it already installed and answering?
	vm := Named(opts.VMName)
	if vm.AgentReady() {
		res.Ready = true
		_ = stage("Windows", true, "installed, agent answering", nil)
		return res, nil
	}

	// Suspended is the cheap case and must be checked before anything decides
	// this VM needs booting. Resuming restores RAM and never reaches firmware:
	// seconds, no keystrokes, and it works with the Mac's screen locked —
	// where a cold boot cannot, because UTM routes the keystrokes that drive
	// the UEFI shell through a display window.
	if vm.IsPaused() {
		say("… resuming from suspend")
		if rErr := vm.Resume(); rErr != nil {
			return res, stage("resume", false, "", rErr)
		}
		if vm.waitForAgentEvery(2*time.Minute, time.Second) == nil {
			res.Ready = true
			_ = stage("resume", false, "restored from suspend, agent answering", nil)
			return res, nil
		}
		_ = stage("resume", false, "resumed, but the agent has not answered yet", nil)
	}

	if !opts.Install {
		_ = stage("Windows", false, "not installed yet — re-run with -install (about 45 minutes)", nil)
		return res, nil
	}

	// 7. The long one.
	//
	// RunInstall, not EnsureReady. They are not interchangeable and using the
	// wrong one is a silent failure: EnsureReady RECOVERS a VM that has Windows
	// on it already, while an install has TWO boot phases — Setup copies files,
	// reboots, and lands back in the UEFI shell needing a different boot, off
	// the ESP this time. EnsureReady does not know about the second phase, so a
	// fresh VM would sit at a shell prompt until the timeout.
	say("… installing Windows unattended; this takes about 45 minutes")
	e, fErr := Find(opts.VMName)
	if fErr != nil {
		return res, stage("install Windows", false, "", fErr)
	}
	bundle, dErr := BundlePath(e.Name)
	if dErr != nil {
		return res, stage("install Windows", false, "", dErr)
	}

	// An installed VM that is merely off needs recovering, not reinstalling —
	// and reinstalling would destroy it. The boot entry Setup registers is what
	// distinguishes the two.
	if Inspect(e.UUID, bundle).BootEntryWritten {
		say("  … Windows is installed; booting it")
		if rErr := EnsureReady(e.UUID, bundle, opts.Timeout); rErr != nil {
			return res, stage("boot Windows", false, "", rErr)
		}
		res.Ready = true
		_ = stage("boot Windows", false, "booted, agent answering", nil)
		return res, nil
	}

	if iErr := RunInstall(InstallOptions{
		VMRef:      e.UUID,
		BundlePath: bundle,
		Timeout:    opts.Timeout,
		Log:        os.Stdout,
	}); iErr != nil {
		return res, stage("install Windows", false, "", iErr)
	}
	res.Ready = true
	_ = stage("install Windows", false, "installed and answering", nil)
	return res, nil
}
