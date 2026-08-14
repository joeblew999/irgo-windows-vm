package utmvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultVMName is the VM. There is one, deliberately.
//
// Declared here because the vm stage owns what a VM is called. It was written
// out six times — three flag defaults, the library's own fallback, and every
// app task in mise.toml carrying `${VM:-irgo-win11}` because the app commands
// were the two that had no default and refused to run without -vm.
//
// So the same name meant two different things depending on which command you
// typed: vm-create with no -vm made irgo-win11, and app-create with no -vm was
// an error. -vm still overrides, for a disposable VM to test against.
const DefaultVMName = "irgo-win11"

// The two failures a caller can do something different about.
//
// Every failure used to reach the user as exit 1, so a script could not tell
// "my program failed" from "the VM is not there" from "the agent is busy" —
// and utmctl itself exits 0 on failure, which makes this CLI the only honest
// signal there is. They are sentinels rather than string matches because the
// messages are written for people and get reworded.
var (
	// ErrNoVM means UTM has no such VM. Usually a typo, or a bundle written
	// while UTM was running: it only rescans at launch.
	ErrNoVM = errors.New("no such VM")

	// ErrNoAgent means the VM exists but its guest agent is not answering.
	// Recoverable by waiting — Windows Update takes it away for minutes at a
	// time — which is why it is worth telling apart from a VM that is absent.
	ErrNoAgent = errors.New("guest agent not answering")
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
	vmStageDirName = "bin"

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

	// guestExecTimeout bounds a real guest command. Longer than the probe
	// because callers run actual work through it.
	guestExecTimeout = 10 * time.Minute

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
func (v VM) Exec(cmdline ...string) (string, error) { return v.execFor(guestExecTimeout, cmdline...) }

// execFor is Exec with a deadline. Guest commands hang rather than fail when
// the agent is absent, so every one of them needs a bound.
func (v VM) execFor(d time.Duration, cmdline ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	args := append([]string{"exec", v.Ref, "--cmd"}, cmdline...)
	cmd := exec.CommandContext(ctx, utmctlPath(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	combined := strings.TrimSpace(out.String() + errb.String())
	if ctx.Err() != nil {
		return combined, fmt.Errorf("exec in guest: gave up after %s", d)
	}
	if err != nil {
		return combined, fmt.Errorf("exec in guest: %w: %s", err, combined)
	}
	return combined, nil
}

// AgentReady reports whether the guest agent is answering.
func (v VM) AgentReady() bool {
	// Asked by IP address, which is the only utmctl call that returns something
	// checkable.
	//
	// This was briefly changed to probe by running a command, on the reasoning
	// that callers need to execute rather than to ping — which is true, and the
	// probe still could not work: `utmctl exec` never returns the guest's
	// output and always exits 0. That fact is documented in this package, above
	// batchFile, which exists solely to work around it. The probe therefore
	// looked for an echoed token that can never arrive, AgentReady was
	// permanently false, and vm-create refused a VM that was running fine.
	//
	// The limitation this leaves is real and worth knowing: an IP means the
	// guest is networked, not that it will run something. They can differ while
	// Windows Update has the agent busy. Callers must treat a failed exec as
	// its own answer rather than as proof the VM is unusable — and above all
	// must not read either as permission to type at it.
	_, err := v.IPAddress()
	return err == nil
}

// waitForAgentEvery blocks until the guest agent answers or the timeout
// elapses, polling at the given interval.
//
// This is the honest "is the VM actually usable" check. Status reports
// "started" the instant QEMU launches, long before Windows has booted — so
// polling status tells you nothing about whether you can do anything with it.
//
// There was a no-interval waitForAgent wrapping this with ten seconds. Its only
// caller now photographs each poll and passes its own interval, which left the
// wrapper unused — three layers to express two things.
//
// The interval matters more than it looks, because the two things worth waiting
// for differ by two orders of magnitude: a cold boot takes about a minute, so
// polling every ten seconds costs nothing, while a resume is back in ~400 ms
// and a ten-second poll would report it as four hundred times slower than it is.
func (v VM) waitForAgentEvery(timeout, interval time.Duration) error {
	return v.waitForAgentTicking(timeout, interval, nil)
}

// bootPollEvery is how often a boot is photographed while it is waited on.
//
// Five seconds across a minute-long boot is a dozen frames, which is enough to
// see where it stopped and few enough that the directory still reads as a
// sequence rather than a flip-book.
const bootPollEvery = 5 * time.Second

// waitForAgentTicking is waitForAgentEvery with something to do on each poll.
//
// tick runs before every wait, including the first, so the caller gets a frame
// of the very start rather than only of what came after the first interval.
func (v VM) waitForAgentTicking(timeout, interval time.Duration, tick func()) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.AgentReady() {
			return nil
		}
		if tick != nil {
			tick()
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("%w: did not respond within %s — "+
		"if the VM was created without guest tools it never will", ErrNoAgent, timeout)
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
	return Entry{}, fmt.Errorf("%w: %q (restart UTM if it was just generated)", ErrNoVM, ref)
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

		// Photographed as it goes, not once at each end.
		//
		// A boot took two shots: "booting" the instant the window opened, and
		// "ready" when the agent answered. Everything between them — the
		// firmware menu, the Windows logo, the spinner, the lock screen — went
		// unrecorded, so the one part anybody wants to see when a boot hangs was
		// the part with no pictures of it.
		//
		// Numbered rather than named, because nothing here can tell a lock
		// screen from a logo: the host sees pixels and the guest is not
		// answering yet, which is the whole reason for looking.
		n := 0
		err := vm.waitForAgentTicking(timeout, bootPollEvery, func() {
			n++
			shot(fmt.Sprintf("booting-%d", n))
		})
		if err == nil {
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
	// No duration in this one, because none elapsed. It said "not answering
	// after 10m0s" while failing in under a second — that is the configured
	// timeout, not a measurement, and it reads as though the tool had waited
	// ten minutes and given up when it had asked once and returned.
	return fmt.Errorf("%w: %s is already running.\n"+
		"  Not typing at it: a running VM may be a working desktop whose agent\n"+
		"  is busy, and keystrokes meant for a boot prompt land in whatever has\n"+
		"  focus. Look at the screenshot above, or run vm-screen.\n"+
		"  The agent usually returns a minute or two after the desktop appears", ErrNoAgent, vmRef)
}

// utmContainerDir is UTM's sandbox container, where it keeps everything it
// owns: the bundles it boots and the guest tools it downloads.
func utmContainerDir(home string) string {
	return filepath.Join(home, "Library", "Containers", utmContainer, "Data")
}
