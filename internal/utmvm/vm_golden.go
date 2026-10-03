package utmvm

// The golden image: an installed system, sealed once, that every new VM of
// that system is cloned from instead of installed, so a VM of one's own is a
// clone and a boot rather than 45 minutes of Setup (Windows) or a download
// and a first boot that needs the network (Linux). One per system: each
// guestOS names its own (guest.go), and everything here takes the system's
// description and keeps one body.
//
// It is a VM like any other, registered with UTM under that name, stopped,
// holding only its system disk. Everything that touches a bundle goes through
// UTM's AppleScript, never the filesystem: macOS App Data protection refuses
// this process ls, cat and touch in UTM's container, and UTM can do all three.
//
// Built locally only: the Windows licence forbids redistribution, and every
// running clone needs its own licence. docs/concepts/architecture.md, "The golden
// image: sealing and cloning", has the rest.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GoldenVMName and GoldenLinuxVMName are the golden images' names in UTM. One
// for each system, fixed: a second image of one system is a second answer to
// "what does vm-create clone".
const (
	GoldenVMName      = "irgo-golden"
	GoldenLinuxVMName = "irgo-golden-linux"
)

// goldenOf is the description of the system os names, for the golden image
// commands: empty is Windows, as in a record.
func goldenOf(os string) (guestOS, error) {
	g, err := guestNamed(os)
	if err != nil {
		return g, fmt.Errorf("%q is not a system this tool makes VMs of (%s or %s)", os, GuestWindows, GuestLinux)
	}
	return g, nil
}

// GoldenName is the golden image of the system os names.
func GoldenName(os string) (string, error) {
	g, err := goldenOf(os)
	return g.goldenName, err
}

// IsGoldenImage reports whether name is one of the golden images.
func IsGoldenImage(name string) bool {
	for _, g := range guests {
		if strings.EqualFold(name, g.goldenName) {
			return true
		}
	}
	return false
}

// cloneBootWait is how long a fresh clone gets to answer. The first boot of a
// clone is a normal boot of an installed Windows with a new network card, so
// it should be like any boot; five minutes, not two, until phase 0 has timed
// one.
const cloneBootWait = 5 * time.Minute

//go:embed assets/utm-clone.applescript
var cloneScript string

//go:embed assets/vm-golden-seal.ps1
var sealScript string

// GoldenManifest is what is known about the golden image, recorded when it is
// made. doctor reports it.
type GoldenManifest struct {
	Source      string    `json:"source"`       // the VM it was sealed from
	OS          string    `json:"os,omitempty"` // the system; empty is Windows, as in a VM record
	Created     time.Time `json:"created"`      // when sealing finished
	ToolVersion string    `json:"tool_version"` // the irgo-winvm that sealed it
	Windows     string    `json:"windows"`      // CurrentBuild.UBR
	WebView2    string    `json:"webview2"`     // the runtime's version, or "none"
	System      string    `json:"system,omitempty"` // a Linux image's PRETTY_NAME and kernel
	Allocated   int64     `json:"allocated"`    // disk.img blocks in use, bytes
	Apparent    int64     `json:"apparent"`     // disk.img length, bytes
	SealSeconds int       `json:"seal_seconds"` // how long sealing took
	BootSeconds int       `json:"boot_seconds"` // how long its verification clone took to answer
	VMCheck     string    `json:"vm_check"`     // the VM suite's verdict on that clone, or why it was not run
}

// GoldenManifestPath is where the manifest of the system os names lives,
// under the application root rather than in the bundle, which this process
// cannot write (see above).
func GoldenManifestPath(os string) string {
	g, _ := goldenOf(os)
	return g.manifestPath()
}

// manifestPath is where g's golden image manifest lives.
func (g guestOS) manifestPath() string { return filepath.Join(appRoot(), g.goldenManifest) }

// readGoldenManifest returns g's manifest, or an error when there is none or
// it cannot be read.
func readGoldenManifest(g guestOS) (GoldenManifest, error) {
	var m GoldenManifest
	b, err := os.ReadFile(g.manifestPath())
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("reading %s: %w", g.manifestPath(), err)
	}
	return m, nil
}

func writeGoldenManifest(g guestOS, m GoldenManifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(appRoot(), 0o755); err != nil {
		return err
	}
	// Written whole to a temporary name and renamed, so a reader never sees
	// half a manifest. The write's error is checked: a full disk shows up
	// there, and a manifest that looks written and is not would report a
	// golden image nobody made.
	tmp := g.manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.manifestPath())
}

// GoldenStatus is what doctor reports about the golden image, from the
// filesystem alone: stat works on a known path in UTM's container, and the
// manifest is ours.
type GoldenStatus struct {
	Name      string // its name in UTM
	Bundle    string
	Present   bool
	Allocated int64
	Apparent  int64

	ManifestPath string
	Manifest     *GoldenManifest // nil when there is none
}

// Golden reports the golden image of the system os names; empty is Windows.
func Golden(os string) GoldenStatus {
	g, _ := goldenOf(os)
	return golden(g)
}

func golden(g guestOS) GoldenStatus {
	s := GoldenStatus{Name: g.goldenName, ManifestPath: g.manifestPath()}
	if b, err := BundlePath(g.goldenName); err == nil {
		s.Bundle = b
		if fi, sErr := os.Stat(DiskPath(b)); sErr == nil {
			s.Present = true
			s.Apparent = fi.Size()
			s.Allocated, _ = diskUsage(DiskPath(b))
		}
	}
	if m, err := readGoldenManifest(g); err == nil {
		s.Manifest = &m
	}
	return s
}

// ErrGoldenRunning is a golden image found running. It must stay stopped:
// UTM refuses to clone a running VM, and a golden image that boots is no
// longer the one its manifest describes.
var ErrGoldenRunning = errors.New("the golden image is running")

// goldenEntry finds g's golden image. ok is false when there is none; err is
// set when UTM could not be asked, which is not the same as "none".
func goldenEntry(g guestOS) (Entry, bool, error) {
	e, err := Find(g.goldenName)
	if err == nil {
		return e, true, nil
	}
	if errors.Is(err, ErrNoVM) {
		return Entry{}, false, nil
	}
	return Entry{}, false, err
}

// GoldenCreateOptions configures GoldenCreate.
type GoldenCreateOptions struct {
	Source      string // the disposable VM to seal; it must exist and be installed
	ToolVersion string // recorded in the manifest

	// Verify, when set, checks the verification clone once it answers — the
	// VM conformance suite, which lives outside this package — and returns
	// its verdict. An error refuses the image (verifyGolden). Nil records
	// that it was not checked.
	Verify func(vm string) (verdict string, err error)
}

// GoldenCreate seals Source and registers the result as the golden image of
// the system Source holds, which its record says.
//
// The guard against sealing the shared VM is the caller's (it needs -force,
// which is the CLI's), and so is the lock. What it does, each step announced
// with the disk's allocation after it, so the measurements the plan asks for
// fall out of every run:
//
//  1. boot Source and wait for its agent;
//  2. in the guest, the system's seal script, one step at a time: on Windows
//     as SYSTEM, decrypt, hibernation off, component cleanup, TRIM
//     (assets/vm-golden-seal.ps1); on Linux as root, what makes the machine
//     itself removed, cloud-init off, fstrim (assets/vm-golden-seal.sh);
//  3. shut the guest down from inside and wait for UTM to say stopped;
//  4. clone it through UTM as the golden image, keeping only the system disk;
//  5. prove it boots: clone the golden image, boot the clone, wait for its
//     agent (and on Linux give it its name and check it), delete the clone;
//  6. write the manifest.
//
// Source is left sealed and stopped, and is still an ordinary VM: vm-delete
// removes it, and removing it frees nothing while the golden image shares its
// blocks.
func GoldenCreate(opts GoldenCreateOptions, say func(string, ...any)) (GoldenManifest, error) {
	var m GoldenManifest
	began := time.Now()

	src, err := Find(opts.Source)
	if err != nil {
		return m, err
	}
	g, err := guestOf(src.UUID)
	if err != nil {
		return m, err
	}
	if _, ok, err := goldenEntry(g); err != nil {
		return m, err
	} else if ok {
		// Idempotent, like every make here: done is reported, not redone.
		// Rebuilding means deleting first, on purpose — it is an hour of work
		// to get back.
		say("STEP 1/6  the golden image")
		say("          %s is already registered; vm-golden-delete -os %s -force to make a new one", g.goldenName, g.name)
		if existing, rErr := readGoldenManifest(g); rErr == nil {
			return existing, nil
		}
		return m, nil
	}

	bundle, err := BundlePath(src.Name)
	if err != nil {
		return m, err
	}
	disk := DiskPath(bundle)
	m.Source = src.Name
	m.ToolVersion = opts.ToolVersion
	if g.name != GuestWindows {
		m.OS = g.name
	}

	if err := CheckAutomation(); err != nil {
		return m, err
	}

	say("STEP 1/6  booting %s", src.Name)
	say("          %s", Home(bundle))
	if err := EnsureReady(src.UUID, bundle, cloneBootWait, say); err != nil {
		return m, err
	}
	allocated := func() string {
		n, ok := diskUsage(disk)
		if !ok {
			return "unknown (cannot stat " + Home(disk) + ")"
		}
		return fmt.Sprintf("%s allocated (%d bytes)", HumanBytes(n), n)
	}
	say("          disk.img: %s", allocated())

	say("STEP 2/6  sealing %s, in the guest, as %s", g.label, g.sshAs)
	if g.name == GuestWindows {
		// vm-repair first, so every clone starts with what it sets: the
		// password that never expires, Windows Update kept quiet, WebView2
		// registered, and the SMB share Push uses — a source installed before
		// the answer file opened that share would otherwise give clones the
		// 0.4 MB/s path.
		say("          vm-repair: password, Windows Update, WebView2, the SMB share, the desktop")
		if err := VMRepair(src.UUID, shareUser, true, false, func(f string, a ...any) { say("            "+f, a...) }); err != nil {
			return m, fmt.Errorf("repairing %s before sealing: %w", src.Name, err)
		}
	}
	guest := g.publicPath(g.sealFile)
	if err := pushScript(src.UUID, guest, g.sealScript); err != nil {
		return m, fmt.Errorf("pushing the seal script: %w", err)
	}
	facts := map[string]string{}
	for _, st := range g.sealSteps {
		say("          %s", st.what)
		t0 := time.Now()
		res, xErr := appExec(src.UUID, g.sealRun(guest, st.step), st.limit, say)
		if xErr != nil {
			return m, fmt.Errorf("seal step %s: %w", st.step, xErr)
		}
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			say("            %s", line)
			if k, v, ok := strings.Cut(line, ": "); ok {
				facts[k] = v
			}
		}
		if res.ExitCode != 0 {
			return m, fmt.Errorf("seal step %s exited %d in the guest", st.step, res.ExitCode)
		}
		say("            [%s] disk.img: %s", time.Since(t0).Round(time.Second), allocated())
	}
	m.Windows, m.WebView2, m.System = facts["windows"], facts["webview2"], facts["system"]
	m.SealSeconds = int(time.Since(began).Seconds())

	say("STEP 3/6  shutting %s down from inside", src.Name)
	if err := shutdownGuest(src.UUID, g, say); err != nil {
		return m, err
	}
	say("          stopped; disk.img: %s", allocated())

	say("STEP 4/6  cloning it as %s, through UTM, keeping only the system disk", g.goldenName)
	t0 := time.Now()
	reprotect := releaseLegacyMedia(bundle, say)
	cErr := cloneVM(src.Name, g.goldenName, randomMAC(), 0, g.diskIface)
	reprotect()
	if cErr != nil {
		return m, cErr
	}
	say("          cloned in %s", time.Since(t0).Round(time.Millisecond))

	gs := golden(g)
	if !gs.Present {
		return m, fmt.Errorf("UTM reported the clone made, and there is no disk at %s", Home(DiskPath(gs.Bundle)))
	}
	m.Allocated, m.Apparent = gs.Allocated, gs.Apparent
	say("          %s: %s allocated of %s", Home(gs.Bundle), HumanBytes(gs.Allocated), HumanBytes(gs.Apparent))

	verify := g.goldenVerify()
	say("STEP 5/6  proving it boots: a clone of it, %s, until its agent answers", verify)
	// A verification clone left by an earlier run that failed here is ours
	// and would make the clone refuse its name.
	if _, fErr := Find(verify); fErr == nil {
		if _, dErr := Delete(verify, true, func(f string, a ...any) { say("          "+f, a...) }); dErr != nil {
			return m, fmt.Errorf("removing the %s an earlier run left: %w", verify, dErr)
		}
	}
	boot, err := cloneAndBoot(g, verify, say)
	if err != nil {
		return m, fmt.Errorf("the golden image did not boot a clone: %w\n"+
			"  %s is left for you to look at (vm-screen -vm %s); vm-delete removes it, and\n"+
			"  vm-golden-delete -os %s removes the golden image", err, verify, verify, g.name)
	}
	m.BootSeconds = int(boot.Seconds())
	say("          %s answered %s after it was started", verify, boot.Round(time.Second))
	if err := verifyGolden(opts.Verify, verify, &m, say); err != nil {
		// Unregistered, so vm-create cannot clone an image that failed. Cheap
		// to undo: the source is still sealed, and registering it again is a
		// clone of seconds.
		if dErr := goldenDelete(g, func(f string, a ...any) { say("          "+f, a...) }); dErr != nil {
			return m, fmt.Errorf("%w; and removing the golden image failed too: %v", err, dErr)
		}
		return m, fmt.Errorf("%w\n  the golden image is removed, so nothing clones it; %s is left to look at\n"+
			"  (vm-screen -vm %s, vm-status), and %s is still sealed: fix it, then vm-golden-create -vm %s again",
			err, verify, verify, src.Name, src.Name)
	}
	if _, err := Delete(verify, true, func(f string, a ...any) { say("          "+f, a...) }); err != nil {
		return m, fmt.Errorf("deleting %s: %w", verify, err)
	}

	say("STEP 6/6  recording it")
	m.Created = time.Now().UTC()
	if err := writeGoldenManifest(g, m); err != nil {
		return m, err
	}
	say("          %s", Home(g.manifestPath()))
	if g.name == GuestWindows {
		say("          sealed in %s, Windows %s, WebView2 %s", time.Duration(m.SealSeconds)*time.Second, m.Windows, m.WebView2)
	} else {
		say("          sealed in %s, %s", time.Duration(m.SealSeconds)*time.Second, m.System)
	}
	return m, nil
}

// ErrGoldenUnverified is a golden image whose verification clone failed the
// VM conformance suite.
var ErrGoldenUnverified = errors.New("the golden image failed the VM conformance suite")

// verifyGolden runs verify on the verification clone and records its verdict
// in m. No verify is recorded as not run, and lets the image through: the
// caller chose that (vm-golden-create -check=false, or no checkout to build
// the suite from, which verify itself reports as a verdict). A verdict with
// an error refuses it.
func verifyGolden(verify func(string) (string, error), vm string, m *GoldenManifest, say func(string, ...any)) error {
	if verify == nil {
		m.VMCheck = "not run (vm-golden-create -check=false)"
		say("          VM conformance suite: not run")
		return nil
	}
	say("          running the VM conformance suite on %s", vm)
	verdict, err := verify(vm)
	m.VMCheck = verdict
	if err != nil {
		if verdict == "" {
			verdict = err.Error()
		}
		return fmt.Errorf("%w: %s", ErrGoldenUnverified, verdict)
	}
	say("          VM conformance suite: %s", verdict)
	return nil
}

// GoldenDelete removes the golden image of the system os names, and its
// manifest. Nothing there is success: an undo has to be runnable twice. The
// -force guard is the caller's.
func GoldenDelete(os string, say func(string, ...any)) error {
	g, err := goldenOf(os)
	if err != nil {
		return err
	}
	return goldenDelete(g, say)
}

func goldenDelete(g guestOS, say func(string, ...any)) error {
	e, ok, err := goldenEntry(g)
	if err != nil {
		return err
	}
	if ok {
		if _, err := Delete(e.UUID, true, say); err != nil {
			return err
		}
	} else {
		say("UTM knows no VM %q", g.goldenName)
	}
	// The manifest goes whether or not the VM was there: a manifest describing
	// a golden image that is gone is what doctor would then report.
	if err := os.Remove(g.manifestPath()); err == nil {
		say("removed %s", Home(g.manifestPath()))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CloneFromGolden makes name a clone of the golden image of the system os
// names, and boots it.
//
// It reports whether there was a golden image to clone: false with a nil
// error means there is none, and the caller falls back to installing, saying
// so. The machine lock is taken for the clone itself — seconds — so it cannot
// race vm-golden-delete; the boot runs under the caller's VM lock only.
func CloneFromGolden(name, os string, say func(string, ...any)) (bool, error) {
	g, err := goldenOf(os)
	if err != nil {
		return false, err
	}
	img, ok, err := goldenEntry(g)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if strings.EqualFold(img.Status, statusStarted) || strings.EqualFold(img.Status, "paused") {
		return true, fmt.Errorf("%w (%s): it must stay stopped. Stop it in UTM; "+
			"if it was changed, vm-golden-delete -os %s -force and make it again", ErrGoldenRunning, img.Status, g.name)
	}
	if err := CheckAutomation(); err != nil {
		return true, err
	}
	// Free memory and disk are vm-create's to check, before it gets here
	// (BeginCreate, vm_capacity.go), in one place for clones and installs.
	release, err := Acquire(MachineLock)
	if err != nil {
		return true, err
	}
	say("… cloning %s as %s", g.goldenName, name)
	t0 := time.Now()
	cErr := cloneVM(g.goldenName, name, randomMAC(), g.cloneMemoryMiB, g.diskIface)
	release()
	if cErr != nil {
		return true, cErr
	}
	say("  cloned in %s", time.Since(t0).Round(time.Millisecond))
	if _, err := bootClone(g, name, say); err != nil {
		return true, err
	}
	return true, nil
}

// cloneAndBoot clones g's golden image as name and boots it, for a caller
// that already holds the machine lock.
func cloneAndBoot(g guestOS, name string, say func(string, ...any)) (time.Duration, error) {
	if err := cloneVM(g.goldenName, name, randomMAC(), g.cloneMemoryMiB, g.diskIface); err != nil {
		return 0, err
	}
	return bootClone(g, name, say)
}

// bootClone starts a fresh clone and waits for its agent, returning how long
// that took. A Linux clone is then given its own name and checked
// (linuxCloned): its image has neither, since cloud-init is off in it.
func bootClone(g guestOS, name string, say func(string, ...any)) (time.Duration, error) {
	e, err := Find(name)
	if err != nil {
		return 0, fmt.Errorf("UTM reported the clone made, and does not list it: %w", err)
	}
	bundle, err := BundlePath(e.Name)
	if err != nil {
		return 0, err
	}
	say("… booting %s", e.Name)
	t0 := time.Now()
	if err := EnsureReady(e.UUID, bundle, cloneBootWait, say); err != nil {
		return 0, err
	}
	took := time.Since(t0)
	say("  %s answered in %s", e.Name, took.Round(time.Second))
	if g.name == GuestLinux {
		if err := linuxCloned(e.UUID, e.Name, say); err != nil {
			return took, err
		}
	}
	return took, nil
}

// cloneVM clones src as dst through UTM with a fresh MAC and memMiB of memory
// (0 keeps src's), keeping only the system disk, the one fixed drive on the
// disk interface iface, and checks that the MAC and the memory took. See
// assets/utm-clone.applescript.
func cloneVM(src, dst, mac string, memMiB int, iface DriveInterface) error {
	if _, err := Find(dst); err == nil {
		return fmt.Errorf("a VM named %q already exists; vm-delete it first or choose another name", dst)
	} else if !errors.Is(err, ErrNoVM) {
		return err
	}
	out, err := utmScript(cloneScriptFor(src, dst, mac, memMiB, iface), 5*time.Minute)
	if err != nil {
		return fmt.Errorf("cloning %s as %s: %w", src, dst, err)
	}
	gotMAC, gotMem, _ := strings.Cut(strings.TrimSpace(out), "\t")
	if !strings.EqualFold(gotMAC, mac) {
		return fmt.Errorf("cloned %s as %s, and its MAC is %q, not the %s asked for; "+
			"two clones sharing one MAC fight over one DHCP lease", src, dst, gotMAC, mac)
	}
	if n, err := strconv.Atoi(gotMem); err != nil || n <= 0 || (memMiB != 0 && n != memMiB) {
		return fmt.Errorf("cloned %s as %s, and UTM says its memory is %q MiB, not the %d asked for; "+
			"vm-delete -vm %s -force and try again", src, dst, gotMem, memMiB, dst)
	}
	e, err := Find(dst)
	if err != nil {
		return fmt.Errorf("UTM reported %s cloned, and does not list it: %w", dst, err)
	}
	if !strings.EqualFold(e.Status, "stopped") {
		return fmt.Errorf("the new clone %s is %s, not stopped", dst, e.Status)
	}
	return nil
}

// cloneScriptFor renders the clone script. iface is an AppleScript enumerator
// of UTM's (NVMe, VirtIO), written bare: it is a constant of this package,
// never input.
func cloneScriptFor(src, dst, mac string, memMiB int, iface DriveInterface) string {
	return fmt.Sprintf(cloneScript, src, iface, iface, memMiB, dst, mac, dst)
}

// shutdownGuest asks the guest to shut down and waits for UTM to report the
// VM stopped. From inside, not `utmctl stop`, so the guest writes everything
// out and the disk is consistent when it is copied.
func shutdownGuest(vmRef string, g guestOS, say func(string, ...any)) error {
	// After a pause, not at once: the batch that runs this still has to write
	// its exit code, and the host still has to pull it, before the guest goes
	// away.
	if _, err := appExec(vmRef, g.shutdown, time.Minute, say); err != nil {
		return fmt.Errorf("asking %s to shut down: %w", g.label, err)
	}
	vm := Named(vmRef)
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		st, err := vm.Status()
		if err == nil && strings.EqualFold(strings.TrimSpace(st), "stopped") {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	st, _ := vm.Status()
	return fmt.Errorf("%s did not stop within 5m after Windows was asked to shut down (UTM says %q)", vmRef, st)
}

// unattendMarker is written by the answer file's last first-logon command, so
// its presence means every one before it ran.
const unattendMarker = `C:\unattend-complete.txt`

// finishInstall ends an install whose agent has just answered: it waits for the
// answer file's last first-logon command, then shuts Windows down from inside,
// takes the install medium out through UTM, and boots again until the agent
// answers. Out of a running VM, never out of a busy one: the first-logon
// commands (guest tools, the SMB share) were cut off once by a hard stop.
func finishInstall(vmRef string, logf func(string, ...any)) error {
	say := func(f string, a ...any) { logf(f, a...) }
	logf("waiting for the answer file's first-logon commands to finish (%s)", unattendMarker)
	deadline := time.Now().Add(20 * time.Minute)
	for {
		res, err := appExec(vmRef, []string{"cmd", "/c", "if exist " + unattendMarker + " (exit 0) else (exit 1)"}, time.Minute, say)
		if err == nil && res.ExitCode == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the guest agent answers, but %s did not appear within 20m: a first-logon command did not finish", unattendMarker)
		}
		time.Sleep(15 * time.Second)
	}
	logf("first-logon commands done; shutting down from inside to take the install medium out")
	if err := shutdownGuest(vmRef, windowsGuest, say); err != nil {
		return err
	}
	done, err := ejectInstallMedia(vmRef)
	if err != nil {
		return err
	}
	if done {
		logf("install medium out")
	}
	vm := Named(vmRef)
	if err := vm.StartWithDisplay(logf); err != nil {
		return err
	}
	return vm.waitForAgentEvery(10*time.Minute, 5*time.Second)
}

// releaseLegacyMedia clears the immutable flag on a bundle's install.iso, if
// it has one, and returns what puts it back.
//
// UTM's clone is copyfile with CLONE, which copies BSD flags (measured: a
// uchg file cloned with those flags is uchg), and dropping the drive then has
// UTM delete that file — which fails with EPERM, and the clone with it. VMs
// made before the media was cloned rather than hardlinked may carry the
// protected ISO's inode. The path is known, so it is stat'ed rather than
// listed: listing UTM's container is refused.
func releaseLegacyMedia(bundle string, say func(string, ...any)) func() {
	iso := filepath.Join(bundle, bundleData, installISO)
	flags, ok := fileFlags(iso)
	if !ok || flags&uchgFlag == 0 {
		return func() {}
	}
	say("          %s is immutable (the protected media's inode); releasing it for the clone", Home(iso))
	survivors := releaseImmutable([]string{iso}, isoSearchDirs())
	return func() {
		// The flag is per inode, so protecting the media's own name puts it
		// back on the bundle's link too.
		for _, p := range survivors {
			_ = isoProtect(p)
		}
	}
}
