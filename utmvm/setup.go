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
	say := func(f string, a ...any) {
		if log != nil {
			log(fmt.Sprintf(f, a...))
		}
	}
	stage := func(name string, skipped bool, detail string, err error) error {
		res.Stages = append(res.Stages, SetupStage{name, skipped, detail, err})
		switch {
		case err != nil:
			say("  ✗ %s: %v", name, err)
		case skipped:
			say("  · %s — already done (%s)", name, detail)
		default:
			say("  ✓ %s — %s", name, detail)
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
	in, err := EnsureUTM()
	if err != nil {
		return res, stage("UTM", false, "", err)
	}
	if err := stage("UTM", true, in.Version+" at "+in.Path, nil); err != nil {
		return res, err
	}

	// 2. The guest tools. Skipped VMs boot fine and are then unreachable —
	// no network, no exec, no IP — so this is checked before anything is built.
	if gt, gErr := EnsureGuestTools(); gErr != nil {
		return res, stage("guest tools", false, "", fmt.Errorf(
			"%w\n     open UTM once and let it download them", gErr))
	} else if err := stage("guest tools", true, filepath.Base(gt), nil); err != nil {
		return res, err
	}

	// 3. Media. Three ways to already have it, in order of preference, then
	// building one.
	iso, isoDetail, isoSkipped, err := ensureMedia(opts, paths, say)
	if err != nil {
		return res, stage("Windows media", false, "", err)
	}
	if err := stage("Windows media", isoSkipped, isoDetail, nil); err != nil {
		return res, err
	}
	res.ISO = iso

	// 4. Protect it. Free, and the difference between a slip and a 4.2 GB
	// re-download of something rate-limited.
	if st, sErr := ISOLinks(iso, nil); sErr == nil && st.Protected {
		_ = stage("protect the media", true, "immutable", nil)
	} else if pErr := ProtectISO(iso); pErr != nil {
		// Not fatal: an unprotected ISO still works, it is just easier to lose.
		_ = stage("protect the media", false, "could not: "+pErr.Error(), nil)
	} else {
		_ = stage("protect the media", false, "now immutable", nil)
	}

	// 5. The VM bundle.
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
		say("  … restarting UTM so it sees the new VM")
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
		say("  … resuming from suspend")
		if rErr := vm.Resume(); rErr != nil {
			return res, stage("resume", false, "", rErr)
		}
		if waitForAgent(vm, 2*time.Minute) {
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
	say("  … installing Windows unattended; this takes about 45 minutes")
	e, fErr := Find(opts.VMName)
	if fErr != nil {
		return res, stage("install Windows", false, "", fErr)
	}
	dir, dErr := DefaultVMDir()
	if dErr != nil {
		return res, stage("install Windows", false, "", dErr)
	}
	if iErr := EnsureReady(e.UUID, filepath.Join(dir, e.Name+".utm"), opts.Timeout); iErr != nil {
		return res, stage("install Windows", false, "", iErr)
	}
	res.Ready = true
	_ = stage("install Windows", false, "installed and answering", nil)
	return res, nil
}

// waitForAgent polls until the guest agent answers, or gives up.
//
// Polling rather than a fixed sleep because the two cases differ by an order of
// magnitude: a resumed VM answers in seconds, a cold-booted one takes minutes.
func waitForAgent(vm VM, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if vm.AgentReady() {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// ensureMedia finds usable Windows media, or makes some.
//
// The order is what a developer would want if they thought about it: use what
// is here, then what they built earlier, then build from an ESD they already
// downloaded, and only then spend 4.2 GB of somebody's bandwidth.
func ensureMedia(opts SetupOptions, paths Paths, say func(string, ...any)) (iso, detail string, skipped bool, err error) {
	// Named explicitly.
	if opts.ISO != "" {
		if _, sErr := os.Stat(opts.ISO); sErr != nil {
			return "", "", false, fmt.Errorf("no such ISO: %s", opts.ISO)
		}
		return opts.ISO, filepath.Base(opts.ISO), true, nil
	}

	// Already in the cache, under either name.
	for _, candidate := range []string{
		paths.ISO(),
		filepath.Join(paths.Cache, "win11-arm64-built.iso"),
	} {
		if _, sErr := os.Stat(candidate); sErr != nil {
			continue
		}
		info, iErr := InspectISO(candidate)
		if iErr != nil || !info.IsARM64 {
			say("  … %s is not ARM64 media; ignoring it", filepath.Base(candidate))
			continue
		}
		return candidate, filepath.Base(candidate), true, nil
	}

	// An ESD already downloaded — build from it rather than downloading again.
	esd := filepath.Join(paths.Cache, "win11-arm64.esd")
	if _, sErr := os.Stat(esd); sErr == nil {
		built := filepath.Join(paths.Cache, "win11-arm64-built.iso")
		say("  … building an ISO from the .esd already in the cache")
		if bErr := buildFromESD(esd, built, paths, say); bErr != nil {
			return "", "", false, bErr
		}
		return built, filepath.Base(built), false, nil
	}

	if !opts.Fetch {
		return "", "", false, fmt.Errorf(
			"no Windows media found.\n"+
				"     Put an ARM64 ISO at %s, or re-run with -fetch to download\n"+
				"     4.2 GB from Microsoft and build one (needs wimlib and xorriso).",
			Home(paths.ISO()))
	}

	// Download, then build.
	all, cErr := FetchCatalog(2 * time.Minute)
	if cErr != nil {
		return "", "", false, cErr
	}
	match := FilterCatalog(all, "ARM64", "en-us", "CLIENTCONSUMER")
	if len(match) != 1 {
		return "", "", false, fmt.Errorf("catalog matched %d ARM64 en-us images, expected exactly 1", len(match))
	}
	e := match[0]
	say("  … downloading %s (%s)", e.Build(), HumanBytes(e.Size))
	if err := os.MkdirAll(paths.Cache, 0o755); err != nil {
		return "", "", false, err
	}
	if dErr := Download(e.FilePath, esd, e.Sha1, func(done, total int64) {
		if total > 0 {
			say("      %s / %s", HumanBytes(done), HumanBytes(total))
		}
	}); dErr != nil {
		return "", "", false, dErr
	}

	built := filepath.Join(paths.Cache, "win11-arm64-built.iso")
	if bErr := buildFromESD(esd, built, paths, say); bErr != nil {
		return "", "", false, bErr
	}
	return built, filepath.Base(built), false, nil
}

func buildFromESD(esd, out string, paths Paths, say func(string, ...any)) error {
	work, err := paths.EnsureWork(12 << 30)
	if err != nil {
		return err
	}
	media := filepath.Join(work, "media")
	if err := os.RemoveAll(media); err != nil {
		return err
	}
	defer os.RemoveAll(media)

	if err := ExpandESD(esd, media, func(step string) { say("      %s", step) }); err != nil {
		return err
	}
	return BuildISO(RemasterOptions{
		Source:   media,
		Output:   out,
		Label:    "WINDOWS_ARM64",
		NoPrompt: true,
	}, paths)
}
