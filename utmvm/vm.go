package utmvm

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The bundle layout, declared once.
//
// These four names were spelled out at ten call sites across the package and
// the CLI, assembled by hand each time, so
// UTM's on-disk layout was knowledge every caller had to carry, and one of them
// used string concatenation rather than filepath.Join.
const (
	// vmStageDirName is where binaries staged onto a VM's payload medium are
	// looked for by default.
	vmStageDirName   = "bin"
	vmScreensDirName = "docs/screens"

	// Where UTM itself lives, and what it keeps inside its container. UTM
	// decides all of this; we only have to spell it correctly, and spelling it
	// in five places is how one of them ends up wrong.
	utmAppPath   = "/Applications/UTM.app"
	utmContainer = "com.utmapp.UTM"
	utmBinDir    = "Contents/MacOS"
	utmDocuments = "Documents"
	utmToolsDir  = "GuestSupportTools"
	utmToolsISO  = "utm-guest-tools-latest.iso"

	// utmctlTimeout bounds every utmctl call. Guest-side commands wait forever
	// when the agent is absent, which is the normal state of a VM that has not
	// finished installing.
	utmctlTimeout = 20 * time.Second

	// agentProbeCmd is the cheapest thing that proves the guest can execute.
	// Its output is checked, because utmctl exec exits 0 either way.
	agentProbeToken = "irgo-agent-ok"
	agentProbeCmd   = "cmd.exe /c echo " + agentProbeToken

	// agentProbeTimeout is short: this is asked in a loop, and a guest that
	// cannot answer in a few seconds is not answering.
	agentProbeTimeout = 10 * time.Second

	// What goes inside a bundle we generate.
	bundleExt   = ".utm"
	bundleData  = "Data"
	diskImage   = "disk.img"
	installISO  = "install.iso"
	guestISO    = "guest-tools.iso"
	unattendISO = "unattend.iso"
)

// DefaultVMDir is where UTM looks for bundles. Note UTM only rescans this
// directory at launch, so a bundle written while UTM is running will not appear
// until it is quit and reopened.
func DefaultVMDir() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("utmvm: UTM is macOS-only (host is %s)", runtime.GOOS)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(utmContainerDir(home), utmDocuments), nil
}

// utmctl is UTM's CLI, shipped inside the app bundle and not on PATH.
func utmctlPath() string { return filepath.Join(AppPath, utmBinDir, "utmctl") }

// VM identifies a virtual machine by name or UUID. utmctl accepts either, so
// callers can use whichever they have.
type VM struct{ Ref string }

// Named returns a handle to a VM. Nothing is validated until a call is made.
func Named(ref string) VM { return VM{Ref: ref} }

func (v VM) run(args ...string) (string, error) { return v.runFor(utmctlTimeout, args...) }

// runFor is `utmctl <args> <vm>` with a deadline.
//
// The deadline is not optional. `utmctl ip-address` against a guest with no
// agent does not fail — it waits, forever, and so does everything built on it:
// AgentReady, and every command that asks whether a VM is usable. A VM with no
// Windows installed hung the CLI for ten minutes with no output.
func (v VM) runFor(d time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, utmctlPath(), append(args, v.Ref)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	combined := strings.TrimSpace(out.String() + errb.String())
	if ctx.Err() != nil {
		return combined, fmt.Errorf("utmctl %s on %s: gave up after %s", strings.Join(args, " "), v.Ref, d)
	}
	if err != nil {
		return combined, fmt.Errorf("utmctl %s: %w: %s", strings.Join(args, " "), err, combined)
	}
	return combined, nil
}

// statusStarted is what utmctl reports for a powered-on VM.
//
// Compared through IsRunning, never spelled out at a call site: there used to
// be five such comparisons and two of them were case-sensitive while three used
// EqualFold, so the same VM could be "running" to one caller and not to another.
const statusStarted = "started"

// IsRunning reports whether the VM is powered on. One comparison, case-folded.
func (v VM) IsRunning() bool {
	st, err := v.Status()
	return err == nil && strings.EqualFold(st, statusStarted)
}

// Status returns "started", "stopped", "paused" or similar.
func (v VM) Status() (string, error) { return v.run("status") }

// Start powers on the VM. Starting an already-running VM is not an error.
//
// Note this does NOT open a display window, which matters more than it sounds:
// UTM routes keyboard input through the display, so a VM started this way
// cannot be sent keystrokes at all — they are accepted and silently discarded.
// Use StartWithDisplay when the UEFI shell will need driving, which on this
// firmware is every boot.
func (v VM) Start() error { _, err := v.run("start"); return err }

// StartWithDisplay powers on the VM through UTM itself so a display window
// opens.
//
// Discovered the hard way: identical VMs booted with utmctl start ignored every
// keystroke, while the same VM started from the app accepted them. utmctl
// starts the machine headless, and without a display there is nowhere for input
// to go. Since UTM's aarch64 firmware always drops to the interactive UEFI
// shell, a Windows VM is unusable without this.
func (v VM) StartWithDisplay() error {
	script := fmt.Sprintf(`tell application "UTM"
  activate
  start virtual machine named %q
end tell`, v.Ref)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		// Fall back to the UUID form; AppleScript matches on name only.
		if e, ferr := Find(v.Ref); ferr == nil && !strings.EqualFold(e.Name, v.Ref) {
			return VM{Ref: e.Name}.StartWithDisplay()
		}
		return fmt.Errorf("starting %s with a display: %w: %s", v.Ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop requests a shutdown.
func (v VM) Stop() error { _, err := v.run("stop"); return err }

// Resume brings a suspended VM back. It is the same utmctl verb as a cold
// start, which is why there is no separate "resume" in UTM: a VM with saved
// state resumes, one without boots.
func (v VM) Resume() error { return v.Start() }

// IsPaused reports whether the VM is suspended rather than stopped or running.
//
// Worth distinguishing because the three need different treatment and only one
// of them is expensive: a paused VM is seconds from ready, a stopped one needs
// its firmware driven, and a running one needs nothing.
func (v VM) IsPaused() bool {
	st, err := v.Status()
	return err == nil && strings.TrimSpace(st) == "paused"
}

// IPAddress returns the guest's non-loopback addresses.
//
// This only works once the QEMU guest agent is installed, which on Windows
// means the UTM guest tools. A VM generated without them will always report
// the agent as missing — that is the tools being absent, not the guest being
// broken.
func (v VM) IPAddress() ([]string, error) {
	out, err := v.run("ip-address")
	if err != nil {
		return nil, err
	}
	// utmctl exits 0 and prints its complaint as ordinary output when the agent
	// is missing, so the exit status cannot be trusted here. Every line must be
	// validated as an address or a human-readable error is mistaken for one —
	// which made `status` cheerfully report a working agent on a VM that had
	// none.
	var ips []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		ip := net.ParseIP(line)
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		ips = append(ips, line)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no guest IP: %s", firstLine(out))
	}
	return ips, nil
}

// Exec runs a command in the guest and returns its output. Requires the guest
// agent.
func (v VM) Exec(cmdline ...string) (string, error) {
	args := append([]string{"exec", v.Ref, "--cmd"}, cmdline...)
	cmd := exec.Command(utmctlPath(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	combined := strings.TrimSpace(out.String() + errb.String())
	if err != nil {
		return combined, fmt.Errorf("exec in guest: %w: %s", err, combined)
	}
	return combined, nil
}

// AgentReady reports whether the guest agent is answering.
func (v VM) AgentReady() bool {
	// Tests what callers actually need: the ability to RUN something.
	//
	// This used to ask for an IP address, and the two come up at different
	// times. Measured minutes apart on one VM: ip-address answered while exec
	// failed, and earlier exec worked while ip-address failed. So vm-create
	// said "ready" for a guest app-create could not use, and waited on one it
	// could — the same wrong answer in both directions.
	//
	// A network address is not the point. Nothing here needs the guest's IP;
	// everything here needs to push a file and run it.
	out, err := v.runFor(agentProbeTimeout, "exec", "--cmd", agentProbeCmd)
	if err != nil {
		return false
	}
	// utmctl exec exits 0 whatever happens in the guest, so the output is the
	// only evidence. A working agent echoes the token back; a broken one
	// returns its own complaint.
	return strings.Contains(out, agentProbeToken)
}

// waitForAgent blocks until the guest agent answers or the timeout elapses.
//
// This is the honest "is the VM actually usable" check. Status reports
// "started" the instant QEMU launches, long before Windows has booted — so
// polling status tells you nothing about whether you can do anything with it.
func (v VM) waitForAgent(timeout time.Duration) error {
	return v.waitForAgentEvery(timeout, 10*time.Second)
}

// waitForAgentEvery is waitForAgent with the poll interval exposed.
//
// The interval matters more than it looks, because the two things worth waiting
// for differ by two orders of magnitude: a cold boot takes about a minute, so
// polling every ten seconds costs nothing, while a resume is back in ~400 ms
// and a ten-second poll would report it as four hundred times slower than it is.
func (v VM) waitForAgentEvery(timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.AgentReady() {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("guest agent did not respond within %s — "+
		"if the VM was created without guest tools it never will", timeout)
}

// Entry is one row of utmctl list.
type Entry struct {
	UUID, Status, Name string
}

// List enumerates VMs UTM knows about.
//
// UTM only rescans its bundle directory at launch, so a VM generated while UTM
// is running will not appear here until UTM is restarted.
func List() ([]Entry, error) {
	out, err := exec.Command(utmctlPath(), "list").Output()
	if err != nil {
		return nil, fmt.Errorf("utmctl list: %w", err)
	}
	var entries []Entry
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // header
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		entries = append(entries, Entry{UUID: f[0], Status: f[1], Name: strings.Join(f[2:], " ")})
	}
	return entries, nil
}

// Find resolves a name or UUID to a known VM.
func Find(ref string) (Entry, error) {
	list, err := List()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range list {
		if strings.EqualFold(e.Name, ref) || strings.EqualFold(e.UUID, ref) {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no VM named %q (restart UTM if it was just generated)", ref)
}

// firstLine keeps error messages to one line; utmctl can be verbose.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// RestartUTM quits and relaunches UTM.
//
// Necessary because UTM enumerates its bundle directory only at launch: a VM
// written to disk while UTM is running simply does not exist as far as utmctl
// or the UI are concerned, with no error to suggest why.
func RestartUTM() error {
	_ = exec.Command("osascript", "-e", `tell application "UTM" to quit`).Run()
	for i := 0; i < 15; i++ {
		if err := exec.Command("pgrep", "-f", filepath.Join(AppPath, utmBinDir, "UTM")).Run(); err != nil {
			break // gone
		}
		time.Sleep(time.Second)
	}
	if err := exec.Command("open", "-a", "UTM").Run(); err != nil {
		return fmt.Errorf("relaunching UTM: %w", err)
	}
	// Give it time to enumerate before anything asks for the new VM.
	time.Sleep(8 * time.Second)
	return nil
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

// NOTE: there is deliberately no escaping helper here.
//
// An earlier version doubled backslashes before handing the path to Sprintf's
// %q. Go and AppleScript use the same backslash escape syntax, so %q alone is
// already correct — doubling first produced \\efi\\microsoft\\... in the
// guest and every Go-driven boot silently failed at the shell prompt, while
// hand-written osascript worked. %q, once, is the whole answer.

// VMStageDir is where binaries built for the guest are kept.
//
// Nothing stages them onto the install medium any more: app-create pushes a
// binary to a running VM, and having two ways to get one there meant two
// answers to "why is my binary not in the guest".
func VMStageDir() string { return filepath.Join(appRoot(), vmStageDirName) }

// BundlePath is where UTM keeps the bundle for a VM of this display name.
func BundlePath(name string) (string, error) {
	dir, err := DefaultVMDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+bundleExt), nil
}

// DiskPath is the system disk inside a bundle. Its growth is how an install is
// watched from the host, so it is asked for often.
func DiskPath(bundle string) string { return filepath.Join(bundle, bundleData, diskImage) }

// CheckAutomation reports whether this process may drive UTM through AppleScript.
//
// Booting needs it: UTM's aarch64 firmware drops to a UEFI shell and something
// has to type the boot path, which goes through `osascript` into UTM's display
// window. macOS gates that behind an Automation consent dialog that no script
// can grant, and the failure arrives as a timeout with no mention of
// permissions — after the install has already run for forty minutes.
//
// So it is asked FIRST, with a call that changes nothing.
func CheckAutomation() error {
	out, err := exec.Command("osascript", "-e", `tell application "UTM" to count virtual machines`).CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("this process cannot control UTM: %s\n"+
		"  macOS asks once, in a dialog. Grant it under System Settings ->\n"+
		"  Privacy & Security -> Automation, then run this again.\n"+
		"  Without it the boot cannot be driven and an install stops at a UEFI prompt",
		strings.TrimSpace(string(out)))
}

// isoSearchDirs are the two places an ISO's other names can be: the one
// directory media lives in, and UTM's bundles, which hardlink it.
//
// Two, and both fixed. It used to include ~/Downloads as well, which was a
// third place a file could be and therefore a third answer to "where is the
// media" — the permutation this layout exists to remove.
func isoSearchDirs() []string {
	dirs := []string{ISODir()}
	if vmDir, err := DefaultVMDir(); err == nil {
		dirs = append(dirs, vmDir)
	}
	return dirs
}

// ---- UTM itself: finding it, installing it, and its guest tools ----

// AppPath is where UTM is installed.
const AppPath = utmAppPath

// ---- the unattend medium a VM installs from ----

// EnsureReady brings a VM to a state where commands can be run in it.
//
// A Windows guest reboots on its own — Windows Update did so mid-session here,
// dropping the agent with "Port is not connected" — and UTM's firmware does not
// reliably auto-boot afterwards. So "is the VM ready" cannot be assumed just
// because it was ready a minute ago, and every entry point that talks to the
// guest has to be able to recover rather than fail.
func EnsureReady(vmRef, bundlePath string, timeout time.Duration, log func(string, ...any)) error {
	say := func(f string, a ...any) {
		if log != nil {
			log(f, a...)
		}
	}
	// Photographed at each turn, like the install is. Booting is where a VM
	// hangs, and this path had no pictures at all: it waits up to ten minutes
	// for an agent that may never answer, and the only evidence of what the
	// screen said was gone by the time anyone asked.
	shot := func(stage string) {
		if p, err := Shot(vmRef, stage); err == nil {
			say("   %s", Home(p))
		}
	}

	vm := Named(vmRef)
	if vm.AgentReady() {
		return nil
	}
	if !vm.IsRunning() {
		// Resuming a suspended VM restores RAM and never reaches the firmware,
		// so this is both the fast path and the one needing no keystrokes.
		if err := vm.StartWithDisplay(); err != nil {
			return err
		}
		say("waiting up to %s for Windows to answer", timeout)
		shot("booting")
		if err := vm.waitForAgent(timeout); err == nil {
			shot("ready")
			return nil
		}
		shot("no-agent")

		// Stop here rather than driving the boot.
		//
		// A guest that started and then went quiet is usually Windows Update,
		// which reboots itself and takes the agent with it. Typing at that is
		// the accident this project has a chapter about, and it wastes ten
		// minutes before failing. The screenshot above says which it is, and
		// re-running is cheap.
		return fmt.Errorf("%s started but has not answered in %s.\n"+
			"  It is probably Windows Update; look at the screenshot above.\n"+
			"  Re-run vm-create when it settles, or use vm-screen to watch", vmRef, timeout)
	}
	// Already running and not answering: DO NOT TYPE.
	//
	// This used to hand off to RunInstall, which drives the UEFI shell. But a
	// running VM that will not answer is ambiguous — it may be at a shell after
	// a reboot, or it may be a perfectly good Windows desktop whose agent is
	// busy, and nothing here can tell the two apart.
	//
	// It guessed wrong, on this machine, and the evidence is a screenshot of
	// Edge with three tabs open searching Bing for
	// "fs2:\efi\microsoft\boot\bootmgfw.efi" — the boot path, typed into the
	// address bar of a logged-in desktop while Windows Update ran. The same
	// keystrokes into Setup once destroyed an install.
	//
	// Typing is only safe when this code started the VM and knows it is booting
	// from the install medium, which is RunInstall's job, not this one.
	shot("running-no-agent")
	return fmt.Errorf("%s is running but not answering after %s.\n"+
		"  Not typing at it: a running VM may be a working desktop whose agent\n"+
		"  is busy, and keystrokes meant for a boot prompt land in whatever has\n"+
		"  focus. Look at the screenshot above, or run vm-screen", vmRef, timeout)
}

// utmContainerDir is UTM's sandbox container, where it keeps everything it
// owns: the bundles it boots and the guest tools it downloads.
func utmContainerDir(home string) string {
	return filepath.Join(home, "Library", "Containers", utmContainer, "Data")
}

// VMScreensDir is where screenshots go: a folder in the repository, not under
// the user's data directory.
//
// Deliberately visible and throwaway. A screenshot of a stuck boot is the one
// piece of evidence that cannot be reconstructed later, and evidence hidden in
// ~/Library is evidence nobody looks at. Some of these become documentation;
// the rest are deleted.
func VMScreensDir() string {
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; {
			if _, sErr := os.Stat(filepath.Join(d, "go.mod")); sErr == nil {
				return filepath.Join(d, vmScreensDirName)
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return filepath.Join(appRoot(), vmScreensDirName)
}
