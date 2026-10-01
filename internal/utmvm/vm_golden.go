package utmvm

// The golden image: an installed Windows, sealed once, that every new VM is
// cloned from instead of installed, so a VM of one's own is a clone and a boot
// rather than 45 minutes of Setup.
//
// It is a VM like any other, registered with UTM as GoldenVMName, stopped,
// holding only its system disk. Everything that touches a bundle goes through
// UTM's AppleScript, never the filesystem: macOS App Data protection refuses
// this process ls, cat and touch in UTM's container, and UTM can do all three.
//
// Built locally only: the Windows licence forbids redistribution, and every
// running clone needs its own licence. docs/ARCHITECTURE.md, "The golden
// image: sealing and cloning", has the rest.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GoldenVMName is the golden image's name in UTM. One, fixed: a second golden
// image is a second answer to "what does vm-create clone".
const GoldenVMName = "irgo-golden"

// goldenVerifyName is the throwaway clone vm-golden-create boots to prove the
// image it just made actually boots.
const goldenVerifyName = GoldenVMName + "-verify"

// goldenManifestName is where what is known about the golden image is kept,
// under the application root rather than in the bundle, which this process
// cannot write (see above).
const goldenManifestName = "golden.json"

// cloneHeadroomBytes is the free space a clone is given room to grow into,
// on top of hostDiskReserveBytes (vm_capacity.go).
//
// A clone costs nothing until the guest writes, and then every write is a new
// block: the pagefile coming back, updates, whatever the agent runs. Running
// out mid-write corrupts the clone. 10 GiB is an ESTIMATE, not a measurement —
// phase 0 of the plan measures how much a clone actually grows, and this
// number should be replaced by that.
const cloneHeadroomBytes = 10 << 30

// cloneBootWait is how long a fresh clone gets to answer. The first boot of a
// clone is a normal boot of an installed Windows with a new network card, so
// it should be like any boot; five minutes, not two, until phase 0 has timed
// one.
const cloneBootWait = 5 * time.Minute

//go:embed assets/utm-clone.applescript
var cloneScript string

//go:embed assets/vm-golden-seal.ps1
var sealScript string

// sealSteps is the order the seal script's steps run in, and how long each may
// take. The limits are generous on purpose: decryption and DISM are minutes to
// tens of minutes on a 30 GB install, and a limit that fires on a slow run
// leaves a half-sealed VM for no gain. The step names must be the script's
// ValidateSet (TestSealStepsAreTheScripts).
var sealSteps = []struct {
	step, what string
	limit      time.Duration
}{
	{"facts", "what is there before", 2 * time.Minute},
	{"decrypt", "turning BitLocker off and waiting for the decryption", 130 * time.Minute},
	{"hibernate", "turning hibernation off", 2 * time.Minute},
	{"cleanup", "cleaning up the component store (DISM /ResetBase), which takes minutes", 90 * time.Minute},
	{"trim", "TRIM, so freed blocks can become holes on the host", 30 * time.Minute},
	{"facts", "what is there after", 2 * time.Minute},
}

// GoldenManifest is what is known about the golden image, recorded when it is
// made. doctor reports it.
type GoldenManifest struct {
	Source      string    `json:"source"`       // the VM it was sealed from
	Created     time.Time `json:"created"`      // when sealing finished
	ToolVersion string    `json:"tool_version"` // the irgo-winvm that sealed it
	Windows     string    `json:"windows"`      // CurrentBuild.UBR
	WebView2    string    `json:"webview2"`     // the runtime's version, or "none"
	Allocated   int64     `json:"allocated"`    // disk.img blocks in use, bytes
	Apparent    int64     `json:"apparent"`     // disk.img length, bytes
	SealSeconds int       `json:"seal_seconds"` // how long sealing took
	BootSeconds int       `json:"boot_seconds"` // how long its verification clone took to answer
	VMCheck     string    `json:"vm_check"`     // the VM suite's verdict on that clone, or why it was not run
}

// GoldenManifestPath is where the manifest lives.
func GoldenManifestPath() string { return filepath.Join(appRoot(), goldenManifestName) }

// readGoldenManifest returns the manifest, or an error when there is none or
// it cannot be read.
func readGoldenManifest() (GoldenManifest, error) {
	var m GoldenManifest
	b, err := os.ReadFile(GoldenManifestPath())
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("reading %s: %w", GoldenManifestPath(), err)
	}
	return m, nil
}

func writeGoldenManifest(m GoldenManifest) error {
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
	tmp := GoldenManifestPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, GoldenManifestPath())
}

// GoldenStatus is what doctor reports about the golden image, from the
// filesystem alone: stat works on a known path in UTM's container, and the
// manifest is ours.
type GoldenStatus struct {
	Bundle    string
	Present   bool
	Allocated int64
	Apparent  int64

	ManifestPath string
	Manifest     *GoldenManifest // nil when there is none
}

// Golden reports the golden image.
func Golden() GoldenStatus {
	s := GoldenStatus{ManifestPath: GoldenManifestPath()}
	if b, err := BundlePath(GoldenVMName); err == nil {
		s.Bundle = b
		if fi, sErr := os.Stat(DiskPath(b)); sErr == nil {
			s.Present = true
			s.Apparent = fi.Size()
			s.Allocated, _ = diskUsage(DiskPath(b))
		}
	}
	if m, err := readGoldenManifest(); err == nil {
		s.Manifest = &m
	}
	return s
}

// ErrGoldenRunning is a golden image found running. It must stay stopped:
// UTM refuses to clone a running VM, and a golden image that boots is no
// longer the one its manifest describes.
var ErrGoldenRunning = errors.New("the golden image is running")

// goldenEntry finds the golden image. ok is false when there is none; err is
// set when UTM could not be asked, which is not the same as "none".
func goldenEntry() (Entry, bool, error) {
	e, err := Find(GoldenVMName)
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

// GoldenCreate seals Source and registers the result as GoldenVMName.
//
// The guard against sealing the shared VM is the caller's (it needs -force,
// which is the CLI's), and so is the lock. What it does, each step announced
// with the disk's allocation after it, so the measurements the plan asks for
// fall out of every run:
//
//  1. boot Source and wait for its agent;
//  2. in the guest, as SYSTEM: decrypt, hibernation off, component cleanup,
//     TRIM (assets/vm-golden-seal.ps1);
//  3. shut the guest down from inside and wait for UTM to say stopped;
//  4. clone it through UTM as GoldenVMName, keeping only the system disk;
//  5. prove it boots: clone the golden image, boot the clone, wait for its
//     agent, delete the clone;
//  6. write the manifest.
//
// Source is left sealed and stopped, and is still an ordinary VM: vm-delete
// removes it, and removing it frees nothing while the golden image shares its
// blocks.
func GoldenCreate(opts GoldenCreateOptions, say func(string, ...any)) (GoldenManifest, error) {
	var m GoldenManifest
	began := time.Now()

	if _, ok, err := goldenEntry(); err != nil {
		return m, err
	} else if ok {
		// Idempotent, like every make here: done is reported, not redone.
		// Rebuilding means deleting first, on purpose — it is an hour of work
		// to get back.
		say("STEP 1/6  the golden image")
		say("          %s is already registered; vm-golden-delete -force to make a new one", GoldenVMName)
		if existing, rErr := readGoldenManifest(); rErr == nil {
			return existing, nil
		}
		return m, nil
	}

	src, err := Find(opts.Source)
	if err != nil {
		return m, err
	}
	bundle, err := BundlePath(src.Name)
	if err != nil {
		return m, err
	}
	disk := DiskPath(bundle)
	m.Source = src.Name
	m.ToolVersion = opts.ToolVersion

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

	say("STEP 2/6  sealing Windows, in the guest, as SYSTEM")
	// vm-repair first, so every clone starts with what it sets: the password
	// that never expires, Windows Update kept quiet, WebView2 registered, and
	// the SMB share Push uses — a source installed before the answer file
	// opened that share would otherwise give clones the 0.4 MB/s path.
	say("          vm-repair: password, Windows Update, WebView2, the SMB share, the desktop")
	if err := VMRepair(src.UUID, shareUser, true, false, func(f string, a ...any) { say("            "+f, a...) }); err != nil {
		return m, fmt.Errorf("repairing %s before sealing: %w", src.Name, err)
	}
	guest := guestPublic + `\irgo-vm-golden-seal.ps1`
	if err := pushScript(src.UUID, guest, sealScript); err != nil {
		return m, fmt.Errorf("pushing the seal script: %w", err)
	}
	facts := map[string]string{}
	for _, st := range sealSteps {
		say("          %s", st.what)
		t0 := time.Now()
		res, xErr := appExec(src.UUID, []string{
			"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", guest, "-Step", st.step,
		}, st.limit, say)
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
	m.Windows, m.WebView2 = facts["windows"], facts["webview2"]
	m.SealSeconds = int(time.Since(began).Seconds())

	say("STEP 3/6  shutting %s down from inside", src.Name)
	if err := shutdownGuest(src.UUID, say); err != nil {
		return m, err
	}
	say("          stopped; disk.img: %s", allocated())

	say("STEP 4/6  cloning it as %s, through UTM, keeping only the system disk", GoldenVMName)
	t0 := time.Now()
	reprotect := releaseLegacyMedia(bundle, say)
	cErr := cloneVM(src.Name, GoldenVMName, randomMAC())
	reprotect()
	if cErr != nil {
		return m, cErr
	}
	say("          cloned in %s", time.Since(t0).Round(time.Millisecond))

	g := Golden()
	if !g.Present {
		return m, fmt.Errorf("UTM reported the clone made, and there is no disk at %s", Home(DiskPath(g.Bundle)))
	}
	m.Allocated, m.Apparent = g.Allocated, g.Apparent
	say("          %s: %s allocated of %s", Home(g.Bundle), HumanBytes(g.Allocated), HumanBytes(g.Apparent))

	say("STEP 5/6  proving it boots: a clone of it, %s, until its agent answers", goldenVerifyName)
	// A verification clone left by an earlier run that failed here is ours
	// and would make the clone refuse its name.
	if _, fErr := Find(goldenVerifyName); fErr == nil {
		if _, dErr := Delete(goldenVerifyName, true, func(f string, a ...any) { say("          "+f, a...) }); dErr != nil {
			return m, fmt.Errorf("removing the %s an earlier run left: %w", goldenVerifyName, dErr)
		}
	}
	boot, err := cloneAndBoot(goldenVerifyName, say)
	if err != nil {
		return m, fmt.Errorf("the golden image did not boot a clone: %w\n"+
			"  %s is left for you to look at (vm-screen -vm %s); vm-delete removes it, and\n"+
			"  vm-golden-delete removes the golden image", err, goldenVerifyName, goldenVerifyName)
	}
	m.BootSeconds = int(boot.Seconds())
	say("          %s answered %s after it was started", goldenVerifyName, boot.Round(time.Second))
	if err := verifyGolden(opts.Verify, goldenVerifyName, &m, say); err != nil {
		// Unregistered, so vm-create cannot clone an image that failed. Cheap
		// to undo: the source is still sealed, and registering it again is a
		// clone of seconds.
		if dErr := GoldenDelete(func(f string, a ...any) { say("          "+f, a...) }); dErr != nil {
			return m, fmt.Errorf("%w; and removing the golden image failed too: %v", err, dErr)
		}
		return m, fmt.Errorf("%w\n  the golden image is removed, so nothing clones it; %s is left to look at\n"+
			"  (vm-screen -vm %s, vm-status), and %s is still sealed: fix it, then vm-golden-create -vm %s again",
			err, goldenVerifyName, goldenVerifyName, src.Name, src.Name)
	}
	if _, err := Delete(goldenVerifyName, true, func(f string, a ...any) { say("          "+f, a...) }); err != nil {
		return m, fmt.Errorf("deleting %s: %w", goldenVerifyName, err)
	}

	say("STEP 6/6  recording it")
	m.Created = time.Now().UTC()
	if err := writeGoldenManifest(m); err != nil {
		return m, err
	}
	say("          %s", Home(GoldenManifestPath()))
	say("          sealed in %s, Windows %s, WebView2 %s", time.Duration(m.SealSeconds)*time.Second, m.Windows, m.WebView2)
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

// GoldenDelete removes the golden image and its manifest. Nothing there is
// success: an undo has to be runnable twice. The -force guard is the caller's.
func GoldenDelete(say func(string, ...any)) error {
	e, ok, err := goldenEntry()
	if err != nil {
		return err
	}
	if ok {
		if _, err := Delete(e.UUID, true, say); err != nil {
			return err
		}
	} else {
		say("UTM knows no VM %q", GoldenVMName)
	}
	// The manifest goes whether or not the VM was there: a manifest describing
	// a golden image that is gone is what doctor would then report.
	if err := os.Remove(GoldenManifestPath()); err == nil {
		say("removed %s", Home(GoldenManifestPath()))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CloneFromGolden makes name a clone of the golden image and boots it.
//
// It reports whether there was a golden image to clone: false with a nil
// error means there is none, and the caller falls back to installing, saying
// so. The machine lock is taken for the clone itself — seconds — so it cannot
// race vm-golden-delete; the boot runs under the caller's VM lock only.
func CloneFromGolden(name string, say func(string, ...any)) (bool, error) {
	g, ok, err := goldenEntry()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if strings.EqualFold(g.Status, statusStarted) || strings.EqualFold(g.Status, "paused") {
		return true, fmt.Errorf("%w (%s): it must stay stopped. Stop it in UTM; "+
			"if it was changed, vm-golden-delete -force and make it again", ErrGoldenRunning, g.Status)
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
	say("… cloning %s as %s", GoldenVMName, name)
	t0 := time.Now()
	cErr := cloneVM(GoldenVMName, name, randomMAC())
	release()
	if cErr != nil {
		return true, cErr
	}
	say("  cloned in %s", time.Since(t0).Round(time.Millisecond))
	if _, err := bootClone(name, say); err != nil {
		return true, err
	}
	return true, nil
}

// cloneAndBoot clones the golden image as name and boots it, for a caller
// that already holds the machine lock.
func cloneAndBoot(name string, say func(string, ...any)) (time.Duration, error) {
	if err := cloneVM(GoldenVMName, name, randomMAC()); err != nil {
		return 0, err
	}
	return bootClone(name, say)
}

// bootClone starts a fresh clone and waits for its agent, returning how long
// that took.
func bootClone(name string, say func(string, ...any)) (time.Duration, error) {
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
	return took, nil
}

// cloneVM clones src as dst through UTM with a fresh MAC, keeping only the
// system disk, and checks that the MAC took. See assets/utm-clone.applescript.
func cloneVM(src, dst, mac string) error {
	if _, err := Find(dst); err == nil {
		return fmt.Errorf("a VM named %q already exists; vm-delete it first or choose another name", dst)
	} else if !errors.Is(err, ErrNoVM) {
		return err
	}
	out, err := utmScript(fmt.Sprintf(cloneScript, src, dst, mac, dst), 5*time.Minute)
	if err != nil {
		return fmt.Errorf("cloning %s as %s: %w", src, dst, err)
	}
	if got := strings.TrimSpace(out); !strings.EqualFold(got, mac) {
		return fmt.Errorf("cloned %s as %s, and its MAC is %q, not the %s asked for; "+
			"two clones sharing one MAC fight over one DHCP lease", src, dst, got, mac)
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

// shutdownGuest asks Windows to shut down and waits for UTM to report the VM
// stopped. From inside, not `utmctl stop`, so Windows writes everything out
// and the disk is consistent when it is copied.
func shutdownGuest(vmRef string, say func(string, ...any)) error {
	// /t 5, not /t 0: the batch that runs this still has to write its exit
	// code, and the host still has to pull it, before Windows goes away.
	if _, err := appExec(vmRef, []string{"shutdown", "/s", "/t", "5"}, time.Minute, say); err != nil {
		return fmt.Errorf("asking Windows to shut down: %w", err)
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
	if err := shutdownGuest(vmRef, say); err != nil {
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
	if err := vm.StartWithDisplay(); err != nil {
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
