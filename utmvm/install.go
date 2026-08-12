package utmvm

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Phase is where an unattended install has got to, inferred from the host side
// only — no guest agent, no screenshots.
type Phase string

const (
	PhaseBooting    Phase = "booting"      // firmware up, nothing written yet
	PhasePartition  Phase = "partitioning" // Setup has claimed the disk
	PhaseCopying    Phase = "copying"      // expanding the image
	PhaseFinalising Phase = "finalising"   // copy done, OOBE and first logon
	PhaseReady      Phase = "ready"        // guest agent answering
)

// These thresholds are rough by nature: they are read off block usage, which
// tracks the install loosely and varies with edition and updates. They exist to
// give a developer something better than a silent forty-minute wait, not to be
// precise. The only exact signal is PhaseReady, which is the agent responding.
const (
	partitionMiB = 64
	copyingMiB   = 1024
	finalMiB     = 11000
)

// Progress describes an install without needing anything inside the guest.
type Progress struct {
	Phase   Phase
	DiskMiB int64
	AgentUp bool
	// BootEntryWritten reports whether the guest has written EFI NVRAM. Windows
	// does this when Setup registers its own boot entry, so it is a reliable
	// marker that the copy phase finished — and it is observable from the host
	// as a file mtime.
	BootEntryWritten bool
}

func (p Progress) String() string {
	s := fmt.Sprintf("%-12s %5d MB", p.Phase, p.DiskMiB)
	if p.BootEntryWritten {
		s += "  boot-entry"
	}
	if p.AgentUp {
		s += "  agent-up"
	}
	return s
}

// Inspect reports install progress from the host side alone.
func Inspect(vmRef, bundlePath string) Progress {
	var p Progress
	diskPath := bundlePath + "/Data/disk.img"
	if used, ok := diskUsage(diskPath); ok {
		p.DiskMiB = used >> 20
	}
	if st, err := os.Stat(bundlePath + "/Data/efi_vars.fd"); err == nil {
		// Written well after creation means the guest, not UTM, wrote it.
		if bi, err2 := os.Stat(bundlePath + "/config.plist"); err2 == nil {
			p.BootEntryWritten = st.ModTime().After(bi.ModTime().Add(time.Minute))
		}
	}
	p.AgentUp = Named(vmRef).AgentReady()

	switch {
	case p.AgentUp:
		p.Phase = PhaseReady
	case p.DiskMiB >= finalMiB || p.BootEntryWritten:
		p.Phase = PhaseFinalising
	case p.DiskMiB >= copyingMiB:
		p.Phase = PhaseCopying
	case p.DiskMiB >= partitionMiB:
		p.Phase = PhasePartition
	default:
		p.Phase = PhaseBooting
	}
	return p
}

// InstallOptions controls the unattended install driver.
type InstallOptions struct {
	VMRef      string
	BundlePath string
	Timeout    time.Duration
	Log        io.Writer
}

// RunInstall drives an unattended install to completion without supervision.
//
// This exists because a Windows install has TWO boot phases, and driving only
// the first is what made every previous run need a human. Setup boots from the
// installer, copies files, then REBOOTS — landing back in UTM's UEFI shell,
// where it needs a different boot, off the ESP this time. Nothing did that, so
// the install sat at a shell prompt until someone noticed and typed a path.
//
// The state machine here is deliberately conservative about when it types:
//
//   - it only sends a boot command when progress has STALLED, never on a timer,
//     because keystrokes sent into a live Setup land in its UI and have
//     destroyed an install before;
//   - it picks which boot to send from observed state — installer while the
//     disk is untouched, ESP once Windows has written files — rather than
//     trying them in sequence;
//   - it stops entirely once the agent answers.
func RunInstall(opts InstallOptions) error {
	if opts.Timeout == 0 {
		opts.Timeout = 45 * time.Minute
	}
	logf := func(format string, a ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, format+"\n", a...)
		}
	}

	vm := Named(opts.VMRef)
	if st, _ := vm.Status(); st != "started" {
		if err := vm.StartWithDisplay(); err != nil {
			return err
		}
		logf("started with a display (keystrokes need one)")
	}
	// Let the firmware enumerate and settle on the shell prompt.
	time.Sleep(30 * time.Second)

	deadline := time.Now().Add(opts.Timeout)
	var lastMiB int64 = -1
	var stalls int
	var lastPhase Phase
	var assists int

	for time.Now().Before(deadline) {
		p := Inspect(opts.VMRef, opts.BundlePath)

		if p.Phase != lastPhase {
			logf("%s", p)
			lastPhase = p.Phase
		}
		if p.AgentUp {
			logf("guest agent is answering — install complete")
			return nil
		}

		if p.DiskMiB > lastMiB {
			lastMiB, stalls = p.DiskMiB, 0
		} else {
			stalls++
		}

		// Three consecutive quiet samples (~90s). Long enough that a slow copy
		// phase is not mistaken for a stall, short enough not to waste the wait.
		if stalls >= 3 {
			target, what := BootInstaller, "installer"
			if p.DiskMiB >= partitionMiB {
				// Windows has written to the disk, so the ESP exists and the
				// stall is the post-copy reboot sitting in the shell.
				target, what = BootInstalled, "installed Windows off the ESP"
			}
			assists++
			if assists > 6 {
				return fmt.Errorf("stalled at %s after %d boot attempts; "+
					"run `irgo-winvm screenshot -vm %s` to see the guest", p, assists, opts.VMRef)
			}
			logf("stalled at %s — booting %s (attempt %d)", p, what, assists)
			if err := BootAssistOn(opts.VMRef, target, ""); err != nil {
				return err
			}
			stalls = 0
		}
		time.Sleep(30 * time.Second)
	}
	return fmt.Errorf("install did not finish within %s (last: %s)",
		opts.Timeout, Inspect(opts.VMRef, opts.BundlePath))
}
