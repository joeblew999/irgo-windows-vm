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

// BootAssist drives the UEFI shell to boot the VM.
//
// Filesystem numbering shifts with how many drives are attached and whether
// Windows has created its partitions yet, so each candidate is tried in turn
// rather than assuming a fixed mapping.
//
// Crucially it STOPS at the first attempt that takes. Trying every combination
// unconditionally is a bug, not thoroughness: once a boot succeeds the guest
// owns the keyboard, and further keystrokes land in Windows Setup and activate
// whatever control has focus. That silently destroyed an install that had
// already partitioned the disk — so progress is checked between attempts and
// the loop exits the moment there is any.
func BootAssist(vmRef string, target BootTarget) error {
	return BootAssistWatched(vmRef, target, "")
}

// BootAssistWatched is BootAssist with a disk to watch for progress. When
// diskPath is empty no progress check is possible and every candidate is tried,
// which is only safe before an OS exists.
func BootAssistWatched(vmRef string, target BootTarget, diskPath string) error {
	return BootAssistOn(vmRef, target, "")
}

// BootAssistOn drives the shell using a specific filesystem, e.g. "fs2:".
// Empty means fs0:, where the install medium has always been found.
func BootAssistOn(vmRef string, target BootTarget, override string) error {
	var paths []string
	switch target {
	case BootInstalled:
		paths = []string{`\efi\microsoft\boot\bootmgfw.efi`}
	default:
		// _noprompt first: it skips the keypress wait entirely. bootaa64.efi is
		// the fallback for media that lacks it.
		paths = []string{
			`\efi\microsoft\boot\cdboot_noprompt.efi`,
			`\efi\boot\bootaa64.efi`,
		}
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
		keystrokeDelay.Seconds(), vmRef, fsn, escapeAppleScript(path))

	cmd := exec.Command("osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sending keystrokes to %s: %w: %s", vmRef, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// escapeAppleScript doubles backslashes, which matter here because every EFI
// path is full of them.
func escapeAppleScript(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}

// OpenDisplay ensures the VM's display window exists.
//
// This is required before any keystrokes are sent. utmctl start powers the VM
// on without opening a window, and UTM routes input through the display — so
// keystrokes sent to a windowless VM go nowhere, silently. Opening the bundle
// makes UTM show it.
func OpenDisplay(bundlePath string) error {
	if bundlePath == "" {
		return nil
	}
	if out, err := exec.Command("open", bundlePath).CombinedOutput(); err != nil {
		return fmt.Errorf("opening VM display: %w: %s", err, strings.TrimSpace(string(out)))
	}
	time.Sleep(5 * time.Second)
	return nil
}

// BootAndWait starts a VM, drives it past the UEFI shell, and waits for signs
// of life — disk writes during an install, or the guest agent once installed.
func BootAndWait(vmRef string, target BootTarget, diskPath string, timeout time.Duration) error {
	vm := Named(vmRef)
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
	start, _ := diskUsage(diskPath)
	for time.Now().Before(deadline) {
		if vm.AgentReady() {
			return nil
		}
		if now, ok := diskUsage(diskPath); ok && now > start+(64<<20) {
			return nil // Setup is writing; the boot took
		}
		time.Sleep(15 * time.Second)
	}
	return fmt.Errorf("no disk activity or guest agent within %s after boot assist", timeout)
}
