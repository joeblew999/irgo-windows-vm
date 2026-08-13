package utmvm

import (
	_ "embed"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// UTM's aarch64 firmware does not auto-select a boot entry. It enumerates the
// devices, fails to pick one, and drops into the interactive UEFI shell — every
// boot, including after Windows is installed.
//
// startup.nsh is the documented hook for this and is shipped on the unattend
// medium, but it has not been observed working here, so it cannot be relied on.
// What is proven to work is typing the boot path at the shell, so that is what
// this does.
//
// Two details are load-bearing, both learned the hard way:
//
//   - Characters must be sent slowly. At full speed the guest keyboard drops
//     them and `bootaa64.efi` arrives as `boota`, which the shell rejects with
//     "not recognized as an internal or external command".
//   - Exactly ONE key may be sent at the "Press any key to boot from CD or DVD"
//     prompt. Sending a burst to be safe means the extras land in Windows Setup
//     once it starts, activating whatever control has focus. That silently
//     wrecked an install that had already partitioned the disk.
const keystrokeDelay = 90 * time.Millisecond

// The AppleScript lives in assets/ so it can be read and tested as a script,
// not unpicked from a Go string literal full of escaped quotes.
//
//go:embed assets/boot.applescript
var bootScript string

// BootTarget is what to boot from the UEFI shell.
type BootTarget int

const (
	// BootInstaller starts Windows Setup from the install medium. Uses the
	// _noprompt loader so no keypress is needed; the default bootaa64.efi waits
	// about five seconds for one and then gives up.
	BootInstaller BootTarget = iota

	// BootInstalled starts an installed Windows from the ESP.
	BootInstalled
)

// BootAssistWatched is BootAssist with a disk to watch for progress. When
// diskPath is empty no progress check is possible and every candidate is tried,
// which is only safe before an OS exists.
func BootAssistWatched(vmRef string, target BootTarget, diskPath string) error {
	return bootAssist(vmRef, target, "", diskPath)
}

// BootAssistOn drives the shell using a specific filesystem, e.g. "fs2:".
// Empty means fs0:, where the install medium has always been found.
func BootAssistOn(vmRef string, target BootTarget, override string) error {
	return bootAssist(vmRef, target, override, "")
}

func bootAssist(vmRef string, target BootTarget, override, diskPath string) error {
	var paths []string
	switch target {
	case BootInstalled:
		paths = []string{`\efi\microsoft\boot\bootmgfw.efi`}
	default:
		// bootaa64.efi, not cdboot_noprompt.efi.
		//
		// _noprompt looks like the better choice — it skips the "Press any key
		// to boot from CD" wait — but invoked from the shell on this firmware it
		// returns to the prompt immediately without booting and without an
		// error. bootaa64.efi does boot, and the single Enter this script sends
		// afterwards answers its prompt. Proven beats tidy.
		paths = []string{
			`\efi\boot\bootaa64.efi`,
			`\efi\microsoft\boot\cdboot_noprompt.efi`,
		}
	}

	// Booting an INSTALLED Windows is a different problem from booting the
	// installer, and needs a loop where the installer must not have one.
	//
	// The ESP lives on the NVMe disk, whose filesystem number depends on how
	// many CDs are attached and whether Windows has partitioned yet — it is not
	// fs0, which is the install CD. Guessing one number fails silently.
	//
	// Retrying is safe here in a way it is not for the installer: until Windows
	// boots there is only the UEFI shell to type into, and once it boots the
	// loop stops. The installer case is different because Setup's UI appears
	// while typing may still be in flight, which is what destroyed an install.
	if target == BootInstalled && override == "" {
		order := []string{"fs2:", "fs3:", "fs1:", "fs4:", "fs0:"}
		start, _ := diskUsage(diskPath)
		for _, fsn := range order {
			if err := typeBootCommand(vmRef, fsn, paths[0]); err != nil {
				return err
			}
			for i := 0; i < 4; i++ {
				time.Sleep(10 * time.Second)
				if Named(vmRef).AgentReady() {
					return nil
				}
				// The disk is watched as well as the agent. With
				// Options.NoGuestTools the agent NEVER answers, so waiting on it
				// alone meant the loop moved to the next filesystem and typed a
				// boot path into a Windows that had already started — the exact
				// thing the comment above says cannot happen.
				if diskPath != "" {
					if now, ok := diskUsage(diskPath); ok && now > start+(64<<20) {
						return nil
					}
				}
			}
		}
		// Exhausting every candidate is a failure. Returning nil here reported
		// success after five failed boots and 200 seconds of keystrokes.
		return fmt.Errorf("utmvm: none of %v booted %s", order, vmRef)
	}

	// ONE attempt. Not a loop.
	//
	// Looping over candidates is actively destructive and this is not a
	// theoretical concern: it happened. Windows Setup boots into WinPE, which
	// runs entirely in RAM and writes nothing to disk for minutes, so a
	// disk-growth check cannot tell "still booting" from "did not boot". The
	// loop concluded failure, typed the next candidate, and those characters
	// landed in Setup's UI — they were found sitting in the Product key field
	// as "FFBTB-T64FF-2FMCR-FTBTC-DBTNP", fragments of `fs1:` and an EFI path.
	//
	// There is no reliable way to detect success from outside the guest before
	// disk writes begin, so guessing is not allowed. The install medium is the
	// first CD attached and lands on fs0 in every observed case; if that ever
	// changes, the caller retries deliberately with a different Filesystem.
	fsn := "fs0:"
	if override != "" {
		fsn = override
	}
	return typeBootCommand(vmRef, fsn, paths[0])
}

func typeBootCommand(vmRef, fsn, path string) error {
	script := fmt.Sprintf(bootScript,
		keystrokeDelay.Seconds(), vmRef, fsn, path)

	cmd := exec.Command("osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sending keystrokes to %s: %w: %s", vmRef, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// NOTE: there is deliberately no escaping helper here.
//
// An earlier version doubled backslashes before handing the path to Sprintf's
// %q. Go and AppleScript use the same backslash escape syntax, so %q alone is
// already correct — doubling first produced \\efi\\microsoft\\... in the
// guest and every Go-driven boot silently failed at the shell prompt, while
// hand-written osascript worked. %q, once, is the whole answer.

// BootAndWait starts a VM, drives it past the UEFI shell, and waits for signs
// of life — disk writes during an install, or the guest agent once installed.
func BootAndWait(vmRef string, target BootTarget, diskPath string, timeout time.Duration) error {
	vm := Named(vmRef)
	// Nothing is typed at a guest that is already answering. RunInstall gates on
	// this; BootAndWait did not, so `boot` against a healthy logged-in Windows
	// sent fs0:, an EFI path and eight Enters straight into the desktop.
	if vm.AgentReady() {
		return nil
	}
	if st, _ := vm.Status(); !strings.EqualFold(st, "started") {
		// Must be StartWithDisplay: keystrokes go nowhere on a headless VM.
		if err := vm.StartWithDisplay(); err != nil {
			return err
		}
	}
	// Let the firmware finish enumerating and reach the shell prompt.
	time.Sleep(30 * time.Second)

	if err := BootAssistWatched(vmRef, target, diskPath); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	// ok is honoured: discarding it pinned the baseline at 0 for an unreadable
	// path, so the growth check silently never fired and every boot burned the
	// full timeout before reporting "no disk activity".
	start, baseOK := diskUsage(diskPath)
	for time.Now().Before(deadline) {
		if vm.AgentReady() {
			return nil
		}
		if now, ok := diskUsage(diskPath); ok && baseOK && now > start+(64<<20) {
			return nil // Setup is writing; the boot took
		}
		time.Sleep(15 * time.Second)
	}
	return fmt.Errorf("no disk activity or guest agent within %s after boot assist", timeout)
}
