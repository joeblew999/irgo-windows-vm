package utmvm

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- from bundle.go ----
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
	return filepath.Join(home, "Library", "Containers", "com.utmapp.UTM", "Data", "Documents"), nil
}

// Options controls bundle creation.
type Options struct {
	Name         string // VM name; also the bundle filename
	InstallISO   string // path to the Windows ARM64 ISO
	UnattendISO  string // prebuilt answer-file medium; leave empty to generate one
	ProbeDir     string // Windows test binaries to embed in the generated medium
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

	bundle := filepath.Join(opts.OutDir, opts.Name+".utm")
	if _, err := os.Stat(bundle); err == nil {
		return "", fmt.Errorf("%s already exists; remove it or choose another name", bundle)
	}
	data := filepath.Join(bundle, "Data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return "", err
	}
	// Anything below this point that fails leaves a half-built bundle, which
	// UTM would try to load and reject confusingly. Clean up instead.
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(bundle)
		}
	}()

	if err := createSparse(filepath.Join(data, "disk.img"), int64(opts.DiskGiB)<<30); err != nil {
		return "", fmt.Errorf("system disk: %w", err)
	}
	if err := linkOrCopy(opts.InstallISO, filepath.Join(data, "install.iso")); err != nil {
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
		Drive{ID: id1, ImageName: "disk.img", Type: DriveDisk, Interface: IfaceNVMe},
		Drive{ID: id2, ImageName: "install.iso", Type: DriveCD, Interface: IfaceUSB, ReadOnly: true},
	)

	// The unattend medium is generated unless the caller supplied one. This is
	// what carries autounattend.xml, startup.nsh and the probe binaries.
	unattendImg := filepath.Join(data, "unattend.iso")
	if opts.UnattendISO != "" {
		if err := linkOrCopy(opts.UnattendISO, unattendImg); err != nil {
			return "", fmt.Errorf("unattend medium: %w", err)
		}
	} else {
		if err := BuildPayload(unattendImg, PayloadOptions{ProbeDir: opts.ProbeDir}); err != nil {
			return "", fmt.Errorf("building unattend medium: %w", err)
		}
	}
	id3, _ := newUUID()
	// A CD, not a removable disk. Attached as a FAT disk, Setup ignored
	// autounattend.xml and ran interactively with no diagnostic; as a CD it is
	// read and applied. Do not "improve" this back to a disk.
	cfg.Drives = append(cfg.Drives,
		Drive{ID: id3, ImageName: "unattend.iso", Type: DriveCD, Interface: IfaceUSB, ReadOnly: true})

	// Guest tools give the QEMU guest agent, and with it utmctl exec and
	// ip-address. A VM without them boots but cannot be driven from the host,
	// which defeats the purpose of generating one from code — so this is on by
	// default and its absence is reported, not silently skipped.
	if !opts.NoGuestTools {
		gt, err := GuestToolsISO()
		if err != nil {
			return "", err
		}
		if err := linkOrCopy(gt, filepath.Join(data, "guest-tools.iso")); err != nil {
			return "", fmt.Errorf("guest tools: %w", err)
		}
		id4, _ := newUUID()
		cfg.Drives = append(cfg.Drives,
			Drive{ID: id4, ImageName: "guest-tools.iso", Type: DriveCD, Interface: IfaceUSB, ReadOnly: true})
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
	defer f.Close()
	return f.Truncate(size)
}

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
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

// ---- from config.go ----
// Package utmvm generates UTM virtual machine bundles without the UTM GUI.
//
// A .utm is a directory holding config.plist plus a Data/ subdirectory. UTM
// discovers bundles in its Documents folder at launch, so writing one there is
// equivalent to clicking through the wizard — but repeatable, diffable and
// runnable from CI.
//
// The plist is emitted as plain XML rather than through a plist library: the
// document is a fixed shape, and the schema is version-specific enough that
// being able to read the exact bytes we produce is worth more than the
// abstraction. Every field below was verified against UTM's own Swift source
// at the v4.7.5 tag, not main — main was two majors ahead and disagreed.
// The plist templates live in assets/ as real files rather than inline Go
// strings. They are XML that gets compared against UTM's own output when
// something breaks, and diffing a file beats diffing a quoted constant.
var (
	//go:embed assets/config.plist.tmpl
	plistTemplate string

	//go:embed assets/drive.plist.tmpl
	driveTemplate string
)

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
//	not support snapshots. Suspend is not supported when GPU acceleration
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
		fmt.Fprintf(&drives, driveTemplate, d.ID, d.ImageName, d.Type, d.Interface, ro)
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

// ---- from control.go ----
// utmctl is UTM's CLI, shipped inside the app bundle and not on PATH.
func utmctlPath() string { return AppPath + "/Contents/MacOS/utmctl" }

// VM identifies a virtual machine by name or UUID. utmctl accepts either, so
// callers can use whichever they have.
type VM struct{ Ref string }

// Named returns a handle to a VM. Nothing is validated until a call is made.
func Named(ref string) VM { return VM{Ref: ref} }

func (v VM) run(args ...string) (string, error) {
	cmd := exec.Command(utmctlPath(), append(args, v.Ref)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	combined := strings.TrimSpace(out.String() + errb.String())
	if err != nil {
		return combined, fmt.Errorf("utmctl %s: %w: %s", strings.Join(args, " "), err, combined)
	}
	return combined, nil
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

// Suspend pauses the VM with its RAM intact, so Resume returns it in seconds
// without going near firmware.
//
// This is the fast path and the reason it matters: a cold boot on UTM's aarch64
// firmware has to be DRIVEN — the firmware never auto-selects a boot entry, so
// something must type a path at the UEFI shell and fire eight keypresses. That
// needs an unlocked Mac and a visible display window, takes about two minutes,
// and has destroyed an install when surplus keypresses reached Setup's UI.
// Resuming reaches none of it.
//
// The state lives in memory, so it survives neither quitting UTM nor rebooting
// the host. SuspendToDisk is the durable version — where it is permitted.
func (v VM) Suspend() error { _, err := v.run("suspend"); return err }

// SuspendToDisk is `utmctl suspend --save-state`, and it is NOT SAFE on this
// hardware. It is exported only so the finding below is checkable; nothing in
// this repository calls it, and the CLI does not expose it.
//
// It is supposed to write the VM's state to disk so it survives UTM quitting.
// Measured on Windows 11 ARM64 under UTM 4.7.5, it does one of two things and
// you do not get to choose which:
//
//  1. Refuses honestly, naming a device that cannot be snapshotted:
//
//     Suspend is not supported when GPU acceleration is enabled.
//     Suspend is not supported when an emulated NVMe device is active.
//
//     Removing GPU acceleration (Options.NoGPUAccel) clears the first and
//     reveals the second. NVMe is not removable: Windows ARM64 Setup has no
//     inbox VirtIO storage driver and reports "no drive found" without it.
//
//  2. **Reports success and silently power-cuts the guest.** Exit status 0,
//     "suspended to disk", no state file written anywhere in the bundle, VM
//     left `stopped` — and the guest's next boot goes through "Diagnosing your
//     PC", because what actually happened was a hard power-off.
//
// The second is the dangerous one: it looks like a clean suspend, it is
// indistinguishable from one by exit code, and it risks whatever the guest had
// in flight. A command that can do that must not be offered as a convenience.
//
// Use Suspend instead — in-memory, genuinely instant to resume (measured at
// 300–500 ms to a live guest agent), and it does not lie. The only thing it
// cannot do is survive UTM quitting.
func (v VM) SuspendToDisk() error { _, err := v.run("suspend", "--save-state"); return err }

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
	_, err := v.IPAddress()
	return err == nil
}

// WaitForAgent blocks until the guest agent answers or the timeout elapses.
//
// This is the honest "is the VM actually usable" check. Status reports
// "started" the instant QEMU launches, long before Windows has booted — so
// polling status tells you nothing about whether you can do anything with it.
func (v VM) WaitForAgent(timeout time.Duration) error {
	return v.WaitForAgentEvery(timeout, 10*time.Second)
}

// WaitForAgentEvery is WaitForAgent with the poll interval exposed.
//
// The interval matters more than it looks, because the two things worth waiting
// for differ by two orders of magnitude: a cold boot takes about a minute, so
// polling every ten seconds costs nothing, while a resume is back in ~400 ms
// and a ten-second poll would report it as four hundred times slower than it is.
func (v VM) WaitForAgentEvery(timeout, interval time.Duration) error {
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
		if err := exec.Command("pgrep", "-f", AppPath+"/Contents/MacOS/UTM").Run(); err != nil {
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

// ---- from install.go ----
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

// ---- from bootassist.go ----
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

// ---- from cleanup.go ----
// Removal describes what deleting a VM would reclaim.
type Removal struct {
	Name       string
	UUID       string // what utmctl matches on; the name is not unique
	Path       string
	TotalBytes int64 // bytes on disk, counting hardlinks once
	Running    bool
}

// InspectRemoval reports what a Delete would do, without doing it.
//
// Worth having as a separate step because the apparent size of a bundle is
// misleading: `du` counts the hardlinked install ISO at full size, but removing
// the bundle only frees it if no other VM still links to it. This reports the
// space that would actually come back.
func InspectRemoval(ref string) (Removal, error) {
	var r Removal

	e, err := Find(ref)
	if err != nil {
		return r, err
	}
	r.Name = e.Name
	r.UUID = e.UUID
	r.Running = strings.EqualFold(e.Status, "started")

	dir, err := DefaultVMDir()
	if err != nil {
		return r, err
	}
	r.Path = filepath.Join(dir, e.Name+".utm")
	if _, err := os.Stat(r.Path); err != nil {
		return r, fmt.Errorf("bundle not found at %s", r.Path)
	}
	r.TotalBytes, _ = walkBundle(r.Path)
	return r, nil
}

// walkBundle visits a bundle once and answers both questions Delete needs:
// how much space comes back, and which files are immutable.
//
// One walk, not two. A file with more than one link is shared with another
// bundle, so deleting this one frees nothing — counting it would overstate the
// saving. Immutable files are collected because uchg is per-inode and blocks
// unlink, so they must be released before anything can remove the bundle.
func walkBundle(root string) (bytes int64, immutable []string) {
	seen := map[uint64]bool{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // an unreadable entry contributes nothing
		}
		if flags, ok := fileFlags(p); ok && flags&uchgFlag != 0 {
			immutable = append(immutable, p)
		}
		ino, nlink, ok := inodeInfo(p)
		if ok {
			if nlink > 1 {
				return nil // shared with another VM; deleting this one frees nothing
			}
			if seen[ino] {
				return nil
			}
			seen[ino] = true
		}
		if used, ok := diskUsage(p); ok {
			bytes += used // blocks actually occupied, not the sparse length
		} else {
			bytes += info.Size()
		}
		return nil
	})
	return bytes, immutable
}

// Delete stops a VM if needed and removes its bundle.
//
// The stop comes first and is deliberate: removing files under a running QEMU
// leaves the process writing to deleted inodes, so the space is not returned
// until it exits and UTM is left with a phantom entry.
func Delete(ref string, force bool) (Removal, error) {
	r, err := InspectRemoval(ref)
	if err != nil {
		return r, err
	}
	if r.Running {
		if !force {
			return r, fmt.Errorf("%s is running; stop it first or pass -force", r.Name)
		}
		vm := Named(ref)
		_ = vm.Stop()
		for i := 0; i < 15; i++ {
			if st, _ := vm.Status(); !strings.EqualFold(st, "started") {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	// Immutable media has to be released before anything can unlink it.
	//
	// ProtectISO sets uchg on the ISO's INODE, and Create hardlinks that same
	// inode into the bundle as Data/install.iso — so the flag is on the copy in
	// here too, and unlink returns EPERM. Measured: `rm -rf` on such a bundle
	// fails with "Operation not permitted" and leaves the directory behind.
	//
	// The flag is per-inode, so clearing it here also unprotects the original.
	// That is why the surviving names are recorded now and re-protected at the
	// end, including when removal fails.
	// Search the cache and work dirs too, not just Downloads and the VM dir:
	// this project's own ISO normally lives under IRGO_CACHE_DIR, and a survivor
	// that is not found is a survivor that is not re-protected.
	dp := DefaultPaths()
	_, immutable := walkBundle(r.Path)
	reprotect := releaseImmutable(immutable, append(ISOSearchDirs(), dp.Cache, dp.Work))
	defer func() {
		for _, p := range reprotect {
			_ = ProtectISO(p)
		}
	}()

	// UTM owns the registry, so UTM does the delete.
	//
	// Removing the bundle behind its back leaves an entry pointing at nothing,
	// and utmctl cannot recover from that: it refuses to drop the entry because
	// it cannot remove a bundle that is not there. That is exactly how the
	// `snap-test` phantom on this machine was made. Recovering it needed an
	// empty stub recreated at the expected path so UTM had something to remove.
	//
	// utmctl's failure is not visible in its exit status — `utmctl delete` on a
	// phantom prints "couldn't be removed" and exits 0 — so the bundle is
	// checked afterwards rather than the error being trusted.
	_ = exec.Command("utmctl", "delete", r.UUID).Run()
	if _, err := os.Stat(r.Path); err == nil {
		if rmErr := os.RemoveAll(r.Path); rmErr != nil {
			return r, fmt.Errorf("removing %s: %w", r.Path, rmErr)
		}
	}
	return r, nil
}

// releaseImmutable clears uchg on the given files and returns the OTHER names
// for those inodes, so they can be re-protected once the bundle is gone.
// Removing the bundle unlinks one name; the inode survives via the others and
// should not be left unprotected.
func releaseImmutable(paths, searchIn []string) []string {
	var survivors []string
	for _, p := range paths {
		// Find the siblings BEFORE clearing, while this link still exists.
		if st, err := ISOLinks(p, searchIn); err == nil {
			for _, other := range st.Found {
				if other != p {
					survivors = append(survivors, other)
				}
			}
		}
		_ = UnprotectISO(p)
	}
	return survivors
}

// Prune removes generated artefacts that are not VMs: staged payload images and
// probe builds. It never touches downloaded ISOs, which are expensive to
// re-fetch and cheap to keep.
// ourTempPrefixes are the names this package gives the things it leaves in the
// system temp directory. Every os.CreateTemp/MkdirTemp call here uses one.
//
// Prune matches on these and nothing else. It used to remove any `*.img` or
// `*.dmg` it found, which in a shared /tmp means somebody else's disk image —
// a VM they were mid-way through building, or a downloaded installer. Deleting
// files this project did not create, on the grounds that they look similar, is
// not a cleanup command; it is a hazard with a friendly name.
//
// The cost of being strict is a stale file surviving. The cost of being loose
// is unbounded, so the trade is not close.
var ourTempPrefixes = []string{
	"irgo-winvm-payload-", // staged payload trees      (payload.go)
	"irgo-catalog-",       // extracted Microsoft catalogs (catalog.go)
	"irgo-utm-",           // the UTM .dmg download       (installutm.go)
	"irgo-script-",        // batch files pushed to guests (run.go)
	"utmvm-windowid-",     // AppleScript screenshot helper (screenshot.go)
}

func isOurArtefact(name string) bool {
	for _, p := range ourTempPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func Prune(dirs ...string) (int64, []string, error) {
	var freed int64
	var removed []string
	var errs []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			// Reported, not swallowed: an unreadable directory used to yield
			// "0 removed, no error", which reads as "nothing to do".
			errs = append(errs, fmt.Sprintf("%s: %v", d, err))
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !isOurArtefact(name) {
				continue
			}
			p := filepath.Join(d, name)
			// Size BEFORE removal, counted only if removal succeeds, and by
			// walking: these are directories, whose own entry size is ~96 bytes
			// while the tree beneath can be gigabytes. That number is printed
			// to the user as "reclaimed".
			size, _ := walkBundle(p)
			if rmErr := os.RemoveAll(p); rmErr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", p, rmErr))
				continue
			}
			freed += size
			removed = append(removed, p)
		}
	}
	if len(errs) > 0 {
		return freed, removed, fmt.Errorf("utmvm: prune: %s", strings.Join(errs, "; "))
	}
	return freed, removed, nil
}

// ---- from screenshot.go ----
//
//go:embed assets/windowid.swift
var windowIDSwift string

// Screenshot captures a VM's display window to a PNG.
//
// This is the only reliable way to see inside a guest that has no working guest
// agent — which is every guest until the tools are installed, and any guest
// that is still in the UEFI shell or mid-install.
//
// The naive approach does not work. Plain `screencapture` grabs whichever Space
// is frontmost, so it returns the developer's editor; `osascript` activation
// does not switch Spaces; and the accessibility API refuses to enumerate UTM's
// windows without a permission a CLI cannot grant itself. Capturing by window
// ID has none of those problems, and does not steal focus.
func Screenshot(vmName, outPath string) error {
	id, err := windowID(vmName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	// -x silences the shutter, -o omits the window shadow.
	out, err := exec.Command("screencapture", "-x", "-o", "-l", strconv.Itoa(id), outPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("capturing window %d: %w: %s", id, err, strings.TrimSpace(string(out)))
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return fmt.Errorf("capture produced no file: %w", err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("capture produced an empty file; the VM window may have closed")
	}
	return nil
}

// windowID finds the CoreGraphics window ID for a VM's display window.
//
// Shells out to `swift`, which ships with the Xcode command line tools. Reading
// CGWindowList from Go directly would mean cgo, and this project is deliberately
// cgo-free — a scripting dependency that is already on any Mac able to build for
// Apple platforms is the cheaper trade.
func windowID(vmName string) (int, error) {
	f, err := os.CreateTemp("", "utmvm-windowid-*.swift")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(windowIDSwift); err != nil {
		return 0, err
	}
	f.Close()

	out, err := exec.Command("swift", f.Name()).Output()
	if err != nil {
		return 0, fmt.Errorf("listing UTM windows (needs Xcode command line tools): %w", err)
	}

	var titles []string
	for _, line := range strings.Split(string(out), "\n") {
		id, title, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		titles = append(titles, title)
		if strings.EqualFold(title, vmName) {
			n, convErr := strconv.Atoi(id)
			if convErr != nil {
				return 0, fmt.Errorf("bad window id %q: %w", id, convErr)
			}
			return n, nil
		}
	}
	// UTM's main window is always present; a missing VM window means the display
	// was never opened, which is what `utmctl start` does. Say so, because the
	// symptom otherwise looks like the VM being absent.
	return 0, fmt.Errorf("no UTM window titled %q (found: %s).\n"+
		"A VM started with `utmctl start` has no display window — use `irgo-winvm boot`, "+
		"which starts it through UTM so a window exists", vmName, strings.Join(titles, ", "))
}
