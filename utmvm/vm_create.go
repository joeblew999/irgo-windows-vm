package utmvm

// One command, from a fresh clone to a Windows VM that answers.
//
// Everything this project does was already possible as eight or nine separate
// calls, in an order you had to know, with waits between them that are not
// obvious and a UTM restart in the middle that is not discoverable at all. That
// is a tool for the person who wrote it.
//
// VMCreate is the same work as one idempotent step. Every stage asks whether it is
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
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// VMCreateOptions configures VMCreate.
type VMCreateOptions struct {
	VMName  string
	ISO     string // media to use; empty means "find or build one"
	Timeout time.Duration

	// Install drives the unattended Windows installation, which takes about 45
	// minutes. Off by default for the same reason.
	Install bool
}

// VMCreateStage is one step, and what happened to it.
type VMCreateStage struct {
	Name    string
	Skipped bool // already satisfied
	Detail  string
	Err     error
}

// VMCreateResult is the whole run.
type VMCreateResult struct {
	Stages []VMCreateStage
	ISO    string
	VM     string
	Ready  bool // the guest agent answered
}

// VMCreate takes a machine from wherever it is to a Windows VM that answers.
//
// It returns the stages it went through, including the ones it skipped, because
// "nothing to do" is the most valuable thing an idempotent command can tell you
// and is indistinguishable from "did nothing" unless it says so.
func VMCreate(opts VMCreateOptions, log func(string)) (VMCreateResult, error) {
	var res VMCreateResult
	// No timestamp here: the caller's printer owns that, and adding one too
	// produced "[   0.2s] [   0.2s]".
	say := func(f string, a ...any) {
		if log != nil {
			log(fmt.Sprintf(f, a...))
		}
	}
	last := time.Now()
	step := 0
	const steps = 7
	begin := func(name string) {
		step++
		say("STEP %d/%d  %s", step, steps, name)
	}
	stage := func(name string, skipped bool, detail string, err error) error {
		took := time.Since(last)
		last = time.Now()
		if took > 200*time.Millisecond {
			detail = fmt.Sprintf("%s  [%s]", detail, took.Round(time.Millisecond))
		}
		res.Stages = append(res.Stages, VMCreateStage{name, skipped, detail, err})
		switch {
		case err != nil:
			say("          ✗ %s", err)
		case skipped:
			say("          already done (%s)", detail)
		default:
			say("          %s", detail)
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
	begin("UTM")
	in, err := EnsureUTM()
	if err != nil {
		return res, stage("UTM", false, "", err)
	}
	if err := stage("UTM", true, in.Version+" at "+in.Path, nil); err != nil {
		return res, err
	}

	// 2. The guest tools. Skipped VMs boot fine and are then unreachable —
	// no network, no exec, no IP — so this is checked before anything is built.
	begin("the guest tools")
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
	begin("the Windows media")
	iso := isoBuiltPath()
	if _, sErr := os.Stat(iso); sErr != nil {
		iso = isoPath()
	}
	if _, sErr := os.Stat(iso); sErr != nil {
		return res, stage("the Windows media", false, "", fmt.Errorf(
			"no media at %s\n  AppCreate `irgo-winvm iso-create` first", Home(ISODir())))
	}
	_ = stage("the Windows media", true, filepath.Base(iso), nil)
	res.ISO = iso

	// 4. Protect it. Free, and the difference between a slip and a 4.2 GB
	// re-download of something rate-limited.
	begin("protecting the media")
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
	begin("permission to drive UTM")
	if aErr := CheckAutomation(); aErr != nil {
		return res, stage("control UTM", false, "", aErr)
	}
	_ = stage("control UTM", true, "permitted", nil)

	// 6. The VM bundle.
	begin("the VM bundle")
	existing, findErr := Find(opts.VMName)
	if findErr == nil {
		_ = stage("VM bundle", true, opts.VMName+" ("+existing.Status+")", nil)
	} else {
		if _, cErr := Create(Options{
			Name:       opts.VMName,
			InstallISO: iso,
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

	// Installed but off is the other cheap case, and it was missing: this said
	// "not installed yet — re-run with -install", which is false and invites a
	// 45-minute reinstall over working Windows. Booting is a minute or two.
	if e, fErr := Find(opts.VMName); fErr == nil {
		if bundle, bErr := BundlePath(e.Name); bErr == nil {
			if p := Inspect(e.UUID, bundle); p.BootEntryWritten {
				begin("booting the Windows already on it")
				if rErr := EnsureReady(e.UUID, bundle, 10*time.Minute); rErr != nil {
					return res, stage("boot Windows", false, "", rErr)
				}
				res.Ready = true
				_ = stage("boot Windows", false, "booted, agent answering", nil)
				return res, nil
			}
		}
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
	begin("installing Windows unattended — about 45 minutes")
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
	// distinguishes the two, and it is a GUESS: a partially installed disk
	// satisfies it too. So this boots and waits for the agent rather than
	// announcing an install that may not have finished, and the timeout is the
	// answer when it did not.
	if Inspect(e.UUID, bundle).BootEntryWritten {
		say("          it looks installed — booting to find out")
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

// ---- making a bundle, and driving the install ----

// Options controls bundle creation.
type Options struct {
	Name         string // VM name; also the bundle filename
	InstallISO   string // path to the Windows ARM64 ISO
	UnattendISO  string // prebuilt answer-file medium; leave empty to generate one
	NoGuestTools bool   // skip the guest tools CD (utmctl exec will not work)
	OutDir       string // parent directory; defaults to DefaultVMDir()
	DiskGiB      int    // system disk size, sparse
	MemoryMiB    int
	CPUCount     int

	// NoGPUAccel drops host GPU acceleration. See the display comment in
	// config.go: it removes one of the two devices that block a durable
	// suspend, and does not on its own make one possible.
	NoGPUAccel bool
}

func (o *Options) setDefaults() {
	if o.DiskGiB == 0 {
		o.DiskGiB = 64
	}
	if o.MemoryMiB == 0 {
		o.MemoryMiB = 8192
	}
	if o.CPUCount == 0 {
		o.CPUCount = 4
	}
	if o.Name == "" {
		o.Name = "Win11ARM"
	}
}

// Create writes a bootable UTM bundle and returns its path.
//
// The install ISO is hardlinked rather than copied. Both the source and UTM's
// container live on the same APFS volume in every normal setup, so a 5 GB image
// costs nothing to reference. Creation falls back to a copy across volumes.
func Create(opts Options) (string, error) {
	opts.setDefaults()

	if _, err := os.Stat(opts.InstallISO); err != nil {
		return "", fmt.Errorf("install ISO: %w", err)
	}
	if opts.OutDir == "" {
		d, err := DefaultVMDir()
		if err != nil {
			return "", err
		}
		opts.OutDir = d
	}

	// Check space before writing anything. The sparse disk and hardlinked ISO
	// make a new bundle look nearly free, so the real cost only shows up
	// mid-install — where running out corrupts the VM and wastes 20 minutes.
	if sp, err := CheckSpace(opts.OutDir, opts.InstallISO); err == nil && !sp.OK {
		return "", fmt.Errorf("not enough disk space: %s.\n"+
			"Windows Setup would fail partway and leave an unusable VM", sp)
	}

	bundle := filepath.Join(opts.OutDir, opts.Name+bundleExt)
	if _, err := os.Stat(bundle); err == nil {
		return "", fmt.Errorf("%s already exists; remove it or choose another name", bundle)
	}
	data := filepath.Join(bundle, bundleData)
	if err := os.MkdirAll(data, 0o755); err != nil {
		return "", err
	}
	// Anything below this point that fails leaves a half-built bundle, which
	// UTM would try to load and reject confusingly. Clean up instead.
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(bundle) // cleanup of a failed create; the create error is the news
		}
	}()

	if err := createSparse(filepath.Join(data, diskImage), int64(opts.DiskGiB)<<30); err != nil {
		return "", fmt.Errorf("system disk: %w", err)
	}
	if err := linkOrCopy(opts.InstallISO, filepath.Join(data, installISO)); err != nil {
		return "", fmt.Errorf("install ISO: %w", err)
	}

	uuid, err := newUUID()
	if err != nil {
		return "", err
	}
	mac, err := randomMAC()
	if err != nil {
		return "", err
	}
	cfg := Config{
		Name:       opts.Name,
		UUID:       uuid,
		MemoryMiB:  opts.MemoryMiB,
		CPUCount:   opts.CPUCount,
		MACAddress: mac,
		NoGPUAccel: opts.NoGPUAccel,
	}

	// Checked: an empty Identifier renders a plist UTM rejects with the generic
	// "cannot import this VM" that names no field (see config.go).
	id1, err := newUUID()
	if err != nil {
		return "", err
	}
	id2, err := newUUID()
	if err != nil {
		return "", err
	}
	cfg.Drives = append(cfg.Drives,
		Drive{ID: id1, ImageName: diskImage, Type: DriveDisk, Interface: IfaceNVMe},
		Drive{ID: id2, ImageName: installISO, Type: DriveCD, Interface: IfaceUSB, ReadOnly: true},
	)

	// The unattend medium is generated unless the caller supplied one. This is
	// what carries autounattend.xml, startup.nsh and the probe binaries.
	unattendImg := filepath.Join(data, unattendISO)
	if opts.UnattendISO != "" {
		if err := linkOrCopy(opts.UnattendISO, unattendImg); err != nil {
			return "", fmt.Errorf("unattend medium: %w", err)
		}
	} else {
		if err := BuildPayload(unattendImg, PayloadOptions{}); err != nil {
			return "", fmt.Errorf("building unattend medium: %w", err)
		}
	}
	id3, _ := newUUID()
	// A CD, not a removable disk. Attached as a FAT disk, Setup ignored
	// autounattend.xml and ran interactively with no diagnostic; as a CD it is
	// read and applied. Do not "improve" this back to a disk.
	cfg.Drives = append(cfg.Drives,
		Drive{ID: id3, ImageName: unattendISO, Type: DriveCD, Interface: IfaceUSB, ReadOnly: true})

	// Guest tools give the QEMU guest agent, and with it utmctl exec and
	// ip-address. A VM without them boots but cannot be driven from the host,
	// which defeats the purpose of generating one from code — so this is on by
	// default and its absence is reported, not silently skipped.
	if !opts.NoGuestTools {
		gt, err := GuestToolsISO()
		if err != nil {
			return "", err
		}
		if err := linkOrCopy(gt, filepath.Join(data, guestISO)); err != nil {
			return "", fmt.Errorf("guest tools: %w", err)
		}
		id4, _ := newUUID()
		cfg.Drives = append(cfg.Drives,
			Drive{ID: id4, ImageName: guestISO, Type: DriveCD, Interface: IfaceUSB, ReadOnly: true})
	}

	plist, err := cfg.Plist()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.plist"), []byte(plist), 0o644); err != nil {
		return "", err
	}

	ok = true
	return bundle, nil
}

// createSparse makes a file of the given logical size without writing blocks.
// APFS, ext4 and NTFS all keep this sparse, so a 64 GiB disk costs kilobytes
// until the guest writes to it.
func createSparse(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// Close checked: this creates the VM's 64 GB system disk, and a close
	// error here is a disk that looks made and is not.
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	// An immutable source cannot be hardlinked: ln returns EPERM, and the copy
	// below then silently spends 5.27 GB per VM on a file that was meant to be
	// shared. Measured, not assumed — a bundle built from the protected ISO came
	// out at 5.1 GB with install.iso showing one link instead of two.
	//
	// So the flag is lifted just long enough to make the link, and restored
	// whatever happens. uchg is per-inode, so this briefly unprotects the
	// original too; that window is one syscall wide.
	if flags, ok := fileFlags(src); ok && flags&uchgFlag != 0 {
		if uErr := ISOUnprotect(src); uErr == nil {
			linkErr := os.Link(src, dst)
			_ = isoProtect(src)
			if linkErr == nil {
				return nil
			}
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// Close is CHECKED, not deferred away. A deferred close on a file being
	// written discards the error, and that error is where a full disk shows
	// up — the copy reports success and the file is short.
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// randomMAC returns a QEMU-range locally administered address, so it can never
// collide with real hardware on the network.
func randomMAC() (string, error) {
	var b [3]byte
	// Checked, unlike before: on failure every VM got 52:54:00:00:00:00, which
	// is a guaranteed L2 collision between any two of them on a shared network.
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("utmvm: generating a MAC address: %w", err)
	}
	return fmt.Sprintf("52:54:00:%02X:%02X:%02X", b[0], b[1], b[2]), nil
}

// Config describes a VM to generate. Zero values are not useful; use NewConfig.
type Config struct {
	Name       string
	UUID       string
	MemoryMiB  int
	CPUCount   int
	MACAddress string

	// NoGPUAccel selects virtio-ramfb over virtio-ramfb-gl.
	//
	// Off by default, so the default is unchanged: accelerated. Turning it off
	// removes ONE of the two devices that block a durable suspend, and on its
	// own therefore buys nothing — the emulated NVMe blocks it too, and NVMe is
	// not optional (Windows ARM64 Setup finds no drive without it). It is
	// exposed so the experiment is repeatable, not because it is a fix.
	NoGPUAccel bool

	// Drives are emitted in order. Order matters: UTM assigns bootindex by
	// position, so the install medium must precede anything optional.
	Drives []Drive
}

// DisplayHardware is the display device this config will use.
func (c Config) DisplayHardware() string {
	if c.NoGPUAccel {
		return displayRAMFB
	}
	return displayRAMFBAccel
}

// Drive is one entry in the VM's drive list.
type Drive struct {
	ID        string
	ImageName string // filename inside Data/
	Type      DriveType
	Interface DriveInterface
	ReadOnly  bool
}
type DriveType string

const (
	DriveDisk DriveType = "Disk"
	DriveCD   DriveType = "CD"
)

type DriveInterface string

const (
	// NVMe is required for the Windows system disk: Windows ARM64 has no inbox
	// VirtIO driver, so a VirtIO disk is invisible to Setup and it reports that
	// no drive can be found.
	IfaceNVMe   DriveInterface = "NVMe"
	IfaceUSB    DriveInterface = "USB"
	IfaceVirtIO DriveInterface = "VirtIO"
)

// The display must be a RAMFB variant for Windows on aarch64.
//
// virtio-gpu-pci provides no framebuffer until a guest driver loads, and the
// arm "virt" machine has no legacy VGA to fall back on. The guest then boots
// with no output at all, which is indistinguishable from a hang — including
// the "Press any key to boot from CD" prompt being invisible. UTM's own wizard
// picks the ramfb variant for Windows guests for exactly this reason.
//
// The `-gl` suffix is a separate decision on top of that, and it is the one
// that matters here:
//
//	virtio-ramfb      framebuffer, no host GPU acceleration, SNAPSHOTS WORK
//	virtio-ramfb-gl   framebuffer, host GPU acceleration, snapshots REFUSED
//
// UTM says so outright when you try:
//
//	Failed to save VM snapshot. Usually this means at least one device does
//	not support snapshots. suspend is not supported when GPU acceleration
//	is enabled.
//
// Without snapshots, every boot has to be driven through the UEFI shell by
// typing a path and firing eight keypresses — which needs an unlocked Mac, a
// visible display window, and about two minutes, and has destroyed an install
// when surplus keypresses reached Setup's UI.
//
// With them, resuming restores RAM and never reaches firmware: no keystrokes,
// works with the screen locked, and returns in seconds. For a machine whose
// entire job is running console probes and a WebView2 window, host GPU
// acceleration buys nothing worth that.
const (
	displayRAMFB      = "virtio-ramfb"
	displayRAMFBAccel = "virtio-ramfb-gl"
)

// plistTemplate is the UTM v4 QEMU configuration.
//
// Two fields here are load-bearing and easy to lose to a "cleanup":
//
//   - PS2Controller is decoded with a non-optional decode() and has no default.
//     Omitting it fails the whole document with a generic "cannot import" error
//     that names no field.
//   - UsbBusSupport is an enum whose raw values are "2.0" and "3.0". "USB3_0"
//     looks plausible and is rejected the same opaque way.

// Plist renders the configuration as a UTM v4 config.plist.
func (c Config) Plist() (string, error) {
	if c.Name == "" || c.UUID == "" {
		return "", fmt.Errorf("utmvm: config needs a Name and UUID")
	}
	// Escaping cannot rescue a control character: XML 1.0 has no representation
	// for one, so the document would be malformed however it is written. Reject
	// the input instead of emitting a file UTM refuses with its usual
	// field-less "cannot import this VM". Found by fuzzing.
	if i := indexXMLIllegal(c.Name); i >= 0 {
		return "", fmt.Errorf("utmvm: VM name contains a byte XML cannot represent at offset %d (%q)", i, c.Name)
	}
	if len(c.Drives) == 0 {
		return "", fmt.Errorf("utmvm: config needs at least one drive")
	}

	var drives strings.Builder
	for _, d := range c.Drives {
		ro := "false"
		if d.ReadOnly {
			ro = "true"
		}
		_, _ = fmt.Fprintf(&drives, driveTemplate, d.ID, d.ImageName, d.Type, d.Interface, ro)
	}

	return fmt.Sprintf(plistTemplate,
		xmlEscape(c.Name), c.UUID,
		c.MemoryMiB, c.CPUCount,
		drives.String(),
		c.DisplayHardware(),
		c.MACAddress,
	), nil
}

// xmlEscape covers the characters a VM name can plausibly contain. The plist is
// otherwise fixed text, so a full XML encoder would be more machinery than the
// problem needs.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// indexXMLIllegal returns the index of the first rune XML 1.0 cannot encode, or
// -1. Tab, newline and carriage return are the only control characters allowed.
//
// Invalid UTF-8 has to be checked separately: ranging over a string decodes bad
// bytes as RuneError, which is a perfectly legal character, so a lone 0x90 would
// pass the loop below and then produce a document Go's own XML parser rejects.
// Found by fuzzing.
func indexXMLIllegal(s string) int {
	if !utf8.ValidString(s) {
		for i := 0; i < len(s); {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				return i
			}
			i += size
		}
	}
	for i, r := range s {
		if r == 0x09 || r == 0x0A || r == 0x0D {
			continue
		}
		if r < 0x20 || (r >= 0xD800 && r <= 0xDFFF) || r == 0xFFFE || r == 0xFFFF {
			return i
		}
	}
	return -1
}

// PayloadOptions describes what goes onto the unattend medium.
type PayloadOptions struct {

	// SizeMiB sizes the image. It must fit the probes with room to spare.
	SizeMiB int
}

// BuildPayload writes the ISO9660 medium that makes the install unattended.
//
// A single image, because three different readers each need part of it:
// Windows Setup finds autounattend.xml at the root, the UEFI shell finds
// startup.nsh at the root, and the installed Windows finds the probes and
// run-all.cmd there too. Splitting
// them across separate media was the earlier design and it meant two images
// and an extra drive for no benefit.
func BuildPayload(imagePath string, opts PayloadOptions) error {
	if opts.SizeMiB == 0 {
		opts.SizeMiB = 256
	}
	// Deliberately an ISO9660 CD, not a FAT disk. Setup did not read
	// autounattend.xml from a FAT removable disk and silently fell back to an
	// interactive install; from a CD it applies. See isoBuildImage.

	stage, err := os.MkdirTemp("", "irgo-winvm-payload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }() // scratch

	if err := os.WriteFile(filepath.Join(stage, "autounattend.xml"), autounattendXML, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "startup.nsh"), startupNSH, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "run-all.cmd"), runAllCmd, 0o644); err != nil {
		return err
	}

	// Probe binaries go at the ROOT, not in a subdirectory. go-diskfs's Joliet
	// encoding mangles names inside nested directories into UCS-2 garbage —
	// root entries survive intact. Flat is uglier and it works.

	return isoBuildImage(imagePath, stage, opts.SizeMiB)
}

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

// Inspect reports install progress from the host side alone.
func Inspect(vmRef, bundlePath string) Progress {
	var p Progress
	diskPath := DiskPath(bundlePath)
	if used, ok := diskUsage(diskPath); ok {
		p.DiskMiB = used >> 20
	}
	if st, err := os.Stat(bundlePath + "/Data/efi_vars.fd"); err == nil {
		// Written well after creation means the guest, not UTM, wrote it.
		if bi, err2 := os.Stat(bundlePath + "/config.plist"); err2 == nil {
			p.BootEntryWritten = st.ModTime().After(bi.ModTime().Add(time.Minute))
			// Corroborated by the disk, because the mtime alone is a guess: UTM
			// writes efi_vars.fd on the FIRST power-on, so a bundle merely
			// booted once with no Windows on it satisfied the comparison and
			// reported an install that had never happened. Windows cannot have
			// registered a boot entry without having written the disk, so a
			// disk below the copy threshold refutes it.
			if p.BootEntryWritten && p.DiskMiB < copyingMiB {
				p.BootEntryWritten = false
			}
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
			_, _ = fmt.Fprintf(opts.Log, format+"\n", a...)
		}
	}

	// Media mastered with efisys_noprompt.bin boots itself. Driving the UEFI
	// shell then types fs0:, an EFI path and eight Enters into Setup, which is
	// already showing its language dialog — the exact accident that once left
	// EFI-path fragments in the Product key field, and which turns an
	// unattended install into an interactive one.
	//
	// So the media is asked whether it needs driving, rather than assumed to.
	// Asked by inode, not by path. The bundle's install.iso is a hardlink to
	// the media, so it shares the media's identity but not its scan sidecar —
	// inspecting the bundle copy would re-read 5 GB and still not know.
	selfBooting := isoIsSelfBooting(filepath.Join(opts.BundlePath, bundleData, installISO))

	// Always with a display, even for self-booting media. The FIRST boot needs
	// no keystrokes, but the second does — Setup reboots and must be pointed at
	// the ESP — and UTM routes input through the display window, so a headless
	// VM silently swallows them.
	vm := Named(opts.VMRef)
	if !vm.IsRunning() {
		if err := vm.StartWithDisplay(); err != nil {
			return err
		}
		if selfBooting {
			logf("started — this medium boots itself, so nothing is typed at the installer")
		} else {
			logf("started with a display (keystrokes need one)")
		}
	}
	// Let the firmware enumerate and settle on the shell prompt.
	time.Sleep(30 * time.Second)

	deadline := time.Now().Add(opts.Timeout)
	var lastMiB int64 = -1
	var stalls int
	var lastPhase Phase
	var assists int
	var ejected bool

	for time.Now().Before(deadline) {
		p := Inspect(opts.VMRef, opts.BundlePath)

		if p.Phase != lastPhase {
			logf("%s", p)
			// One shot per phase change. An install is 45 minutes of a number
			// going up, and the number cannot tell you that Setup is sitting on
			// a dialog — the picture can.
			if shot, sErr := Shot(opts.VMRef, string(p.Phase)); sErr == nil {
				logf("   %s", Home(shot))
			}
			lastPhase = p.Phase
		}
		if p.AgentUp {
			logf("guest agent is answering — install complete")
			if shot, sErr := Shot(opts.VMRef, "ready"); sErr == nil {
				logf("   %s", Home(shot))
			}
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
				// stall is the post-copy reboot.
				//
				// Take the disc out first. Self-booting media wins the boot
				// order every time and lands at "Start boot option" with no
				// shell to redirect from, so no amount of typing helps until
				// it is gone.
				if !ejected {
					if done, eErr := ejectInstallMedia(opts.BundlePath); eErr == nil && done {
						ejected = true
						logf("Windows is on the disk — removing the install medium so the firmware boots it")
						_ = vm.Stop()
						time.Sleep(5 * time.Second)
						if rErr := RestartUTM(); rErr == nil {
							_ = Named(opts.VMRef).StartWithDisplay()
						}
						stalls = 0
						continue
					}
				}
				target, what = BootInstalled, "installed Windows off the ESP"
			}
			// selfBooting describes the INSTALL MEDIUM, and only the first
			// boot comes off it. Setup then copies files, reboots, and lands
			// back at the UEFI shell needing a boot off the ESP — which no
			// medium does for us. This is checked BEFORE counting an attempt:
			// waiting for a disc to boot itself is not an attempt at anything,
			// and counting it burned the budget the second phase needs.
			if selfBooting && target == BootInstaller {
				logf("%s — waiting; this medium boots itself", p)
				stalls = 0
				continue
			}
			assists++
			if assists > 6 {
				shot, _ := Shot(opts.VMRef, "gave-up")
				return fmt.Errorf("stalled at %s after %d boot attempts.\n"+
					"  What it looks like right now: %s", p, assists, Home(shot))
			}
			logf("stalled at %s — booting %s (attempt %d)", p, what, assists)
			// Photograph BEFORE typing. If the keystrokes go somewhere wrong,
			// this is the only record of where they went.
			if shot, sErr := Shot(opts.VMRef, fmt.Sprintf("stalled-%d", assists)); sErr == nil {
				logf("   %s", Home(shot))
			}
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

// ---- how much room a Windows install needs ----

// SchemaConfigurationVersion is the value UTM v4 accepts. UTM rejects anything
// higher outright, so a major-version jump is a real compatibility signal
// rather than cosmetic drift.
const SchemaConfigurationVersion = 4

// A Windows 11 install consumes roughly this much once it settles. The sparse
// disk starts near zero, so the free-space figure at creation time is
// misleading — the cost arrives later, during install, when failing is most
// expensive.
const WindowsInstallBytes = 30 << 30

// SpaceCheck reports whether a target directory can host an install.
type SpaceCheck struct {
	FreeBytes     int64
	RequiredBytes int64
	ISOBytes      int64 // 0 when the ISO is hardlinked and therefore free
	OK            bool
}

func (s SpaceCheck) String() string {
	return fmt.Sprintf("%s free, ~%s needed", HumanBytes(s.FreeBytes), HumanBytes(s.RequiredBytes))
}

// CheckSpace verifies there is room before a VM is created.
//
// Worth doing up front because the failure mode is so bad: the sparse disk and
// hardlinked ISO make a new bundle look almost free, then Windows Setup runs
// out of space mid-install and leaves a corrupt VM that has to be rebuilt from
// scratch — after a 20-minute wait.
//
// The ISO costs nothing when it can be hardlinked into the same volume, so it
// is only counted when a copy would be needed.
func CheckSpace(targetDir, isoPath string) (SpaceCheck, error) {
	var s SpaceCheck

	free, err := statfsAvailable(targetDir)
	if err != nil {
		return s, fmt.Errorf("checking free space on %s: %w", targetDir, err)
	}
	s.FreeBytes = free
	s.RequiredBytes = WindowsInstallBytes

	if isoPath != "" && !sameVolume(targetDir, isoPath) {
		if n, err := fileSize(isoPath); err == nil {
			s.ISOBytes = n
			s.RequiredBytes += n
		}
	}

	s.OK = s.FreeBytes >= s.RequiredBytes
	return s, nil
}

// sameVolume reports whether two paths live on one filesystem, in which case a
// hardlink works and the ISO is free.
//
// A false answer only costs an over-estimate of the space needed, which is the
// safe direction — so a platform that cannot tell says no.
func sameVolume(a, b string) bool { return sameDevice(a, b) }

func fileSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// ejectInstallMedia removes the install CD from a bundle's config.
//
// A human takes the disc out when Setup finishes copying. Nothing here did, and
// the consequence is exact: media mastered with efisys_noprompt.bin boots
// itself, so after the reboot the firmware picks the CD again, sits at "Start
// boot option", and never reaches the shell where a boot could be redirected.
// The VM looks hung and the install looks failed, with Windows sitting
// complete on the disk.
//
// Editing the plist means UTM must rescan, which is why this is paired with a
// restart at the call site.
func ejectInstallMedia(bundlePath string) (bool, error) {
	plist := filepath.Join(bundlePath, "config.plist")
	b, err := os.ReadFile(plist)
	if err != nil {
		return false, err
	}
	text := string(b)

	// The drive is one <dict> in the Drives array. Find the entry naming
	// install.iso and remove that dict, rather than rewriting the document —
	// UTM rejects a malformed plist with one generic error that names no field.
	marker := "<key>ImageName</key><string>" + installISO + "</string>"
	i := strings.Index(text, marker)
	if i < 0 {
		return false, nil // already ejected
	}
	start := strings.LastIndex(text[:i], "<dict>")
	end := strings.Index(text[i:], "</dict>")
	if start < 0 || end < 0 {
		return false, fmt.Errorf("utmvm: could not find the drive entry for %s in %s", installISO, plist)
	}
	end += i + len("</dict>")
	for end < len(text) && (text[end] == '\n' || text[end] == '\t' || text[end] == ' ') {
		end++
	}
	out := text[:start] + text[end:]
	if err := os.WriteFile(plist, []byte(out), 0o644); err != nil {
		return false, err
	}
	// The file itself stays: it is a hardlink to the media, and removing it
	// would be iso-delete's business, not this.
	return true, nil
}
