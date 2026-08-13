package utmvm

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
		if err := BuildPayload(unattendImg, PayloadOptions{ProbeDir: opts.ProbeDir}); err != nil {
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

// utmctl is UTM's CLI, shipped inside the app bundle and not on PATH.
func utmctlPath() string { return filepath.Join(AppPath, utmBinDir, "utmctl") }

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
	_, err := v.IPAddress()
	return err == nil
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

	vm := Named(opts.VMRef)
	if !vm.IsRunning() {
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
					"open the VM in UTM to see where the guest is", p, assists)
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

// VMStageDir is where `vm` looks for binaries to stage onto the payload medium.
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

// Install reports how UTM got onto the machine, for messages to the user.
type Install struct {
	Path    string
	Version string

	// Compatible is false when the installed major version differs from the one
	// the schema was verified against. Generation is still attempted — refusing
	// outright would be worse than trying and reporting a clear failure — but
	// the caller should say so up front, because that is the likely cause of
	// any "cannot import" that follows.
	Compatible bool
}

// AppPath is where UTM is installed.
const AppPath = utmAppPath

// DetectUTM reports the installed UTM, or ErrUTMNotInstalled.
func DetectUTM() (Install, error) {
	var in Install
	if _, err := os.Stat(AppPath); err != nil {
		return in, ErrUTMNotInstalled
	}
	in.Path = AppPath

	// plutil ships with every macOS install and this file only ever runs on
	// macOS, so reaching for it is not a dependency a developer must satisfy.
	// App Info.plist files are usually binary, so plain XML parsing would fail
	// on most machines — the alternative is a binary-plist decoder for one
	// string.
	out, err := exec.Command("plutil", "-extract", "CFBundleShortVersionString",
		"raw", "-o", "-", AppPath+"/Contents/Info.plist").Output()
	if err != nil {
		// Present but unreadable: still usable, just unknown.
		in.Version = "unknown"
		in.Compatible = true
		return in, nil
	}
	in.Version = strings.TrimSpace(string(out))
	in.Compatible = sameMajor(in.Version, VerifiedVersion)
	return in, nil
}

// EnsureUTM returns the installed UTM, installing it if missing.
//
// A developer should not have to read a prerequisites list before the tool
// works: UTM's signed .dmg is
// downloaded from its GitHub release and the app copied out, so nothing has to
// be installed first.
func EnsureUTM() (Install, error) {
	in, err := DetectUTM()
	if err == nil {
		return in, nil
	}
	if !errors.Is(err, ErrUTMNotInstalled) {
		return in, err
	}

	// Downloaded directly, not via a package manager.
	//
	// "Install a package manager, then install UTM" is two prerequisites for a tool
	// whose whole claim is that a single binary sets the machine up. UTM
	// publishes a signed .dmg on its GitHub releases, so there is nothing to
	// require: fetch it, mount it, copy the app out.
	//
	if dlErr := InstallUTMFromRelease(nil); dlErr != nil {
		return in, dlErr
	}
	return DetectUTM()
}

// EnsureGuestTools returns the guest tools ISO, downloading it if UTM has not
// already cached one.
//
// It writes to UTM's own cache location, so UTM and every VM generated
// afterwards pick it up exactly as if the GUI had fetched it.
func EnsureGuestTools() (string, error) {
	p, err := GuestToolsISO()
	if err == nil {
		return p, nil
	}
	return FetchGuestTools(nil)
}

// FetchGuestTools downloads the guest tools into UTM's cache and returns the
// path. progress, if non-nil, is called as bytes arrive.
func FetchGuestTools(progress func(done, total int64)) (string, error) {
	dest, err := guestToolsPath()
	if err != nil {
		return "", err
	}
	if _, sErr := os.Stat(dest); sErr == nil {
		return dest, nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
		return "", mkErr
	}

	// No SHA to check against: UTM publishes none, and the file it serves
	// changes with every guest-tools release. The size check below is the only
	// guard available — a truncated download here presents later as a VM with
	// no network, which is a long way from the cause.
	if dErr := isoDownload(GuestToolsURL, dest, "", progress); dErr != nil {
		return "", fmt.Errorf("downloading UTM guest tools: %w\n"+
			"  Alternatively, open UTM once and choose \"Install Windows guest tools\" "+
			"from any VM's menu; it caches the ISO in the same place", dErr)
	}
	fi, sErr := os.Stat(dest)
	if sErr != nil {
		return "", sErr
	}
	if fi.Size() < 10<<20 {
		_ = os.Remove(dest)
		return "", fmt.Errorf("guest tools download was only %s, which is too small to be the ISO",
			HumanBytes(fi.Size()))
	}
	return dest, nil
}

// sameMajor compares leading version components. A patch or minor difference
// from the verified version has never broken the schema in practice; a major
// difference is exactly when UTM has changed ConfigurationVersion before.
func sameMajor(got, want string) bool {
	g, w := majorOf(got), majorOf(want)
	return g != -1 && g == w
}
func majorOf(v string) int {
	part, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(part)
	if err != nil {
		return -1
	}
	return n
}

// guestToolsPath is where UTM caches the guest tools, and therefore where a
// download of our own has to land for UTM and every generated VM to find it.
func guestToolsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(utmContainerDir(home),
		"Library", "Application Support", utmToolsDir, utmToolsISO), nil
}

// GuestToolsISO returns UTM's downloaded guest tools image, if present.
//
// Installing these inside the guest is what gives the QEMU guest agent, and
// therefore `utmctl exec` and `utmctl ip-address`. Without it a VM boots fine
// but cannot be driven from the host at all — which defeats the point of
// generating one from a script.
func GuestToolsISO() (string, error) {
	p, err := guestToolsPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("UTM guest tools not downloaded yet: %w\n"+
			"Open UTM once and let it fetch them, or the guest agent will be unavailable", err)
	}
	return p, nil
}

// InstallUTMFromRelease downloads UTM's .dmg and copies the app to
// /Applications.
//
// It refuses rather than overwrites if UTM is already there: replacing an
// application somebody installed deliberately, possibly a different version
// they are testing against, is not a setup command's decision to make.
func InstallUTMFromRelease(progress func(done, total int64)) error {
	if _, err := os.Stat(AppPath); err == nil {
		return fmt.Errorf("utmvm: %s already exists; not replacing it", AppPath)
	}

	url, err := latestUTMDMG()
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "irgo-utm-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // best effort; the download already succeeded or failed

	dmg := filepath.Join(tmp, "UTM.dmg")
	fmt.Fprintf(os.Stderr, "downloading UTM from %s\n", url)
	if dErr := isoDownload(url, dmg, "", progress); dErr != nil {
		return fmt.Errorf("utmvm: downloading UTM: %w", dErr)
	}

	mnt := filepath.Join(tmp, "mnt")
	if mkErr := os.MkdirAll(mnt, 0o755); mkErr != nil {
		return mkErr
	}
	// -nobrowse keeps it out of Finder, -readonly is all that is needed, and
	// -noverify skips a checksum pass that adds a minute to a 238 MB image the
	// system will verify the signature of anyway when the app is launched.
	attach := exec.Command("hdiutil", "attach", dmg,
		"-nobrowse", "-readonly", "-noverify", "-mountpoint", mnt)
	attach.Stderr = os.Stderr
	if aErr := attach.Run(); aErr != nil {
		return fmt.Errorf("utmvm: mounting the UTM disk image: %w", aErr)
	}
	defer func() {
		detach := exec.Command("hdiutil", "detach", mnt, "-quiet")
		_ = detach.Run()
	}()

	src := filepath.Join(mnt, "UTM.app")
	if _, sErr := os.Stat(src); sErr != nil {
		return fmt.Errorf("utmvm: the disk image has no UTM.app in it: %w", sErr)
	}

	// ditto, not a Go file walk: an application bundle carries extended
	// attributes, symlinks and a code signature, and copying it byte-by-byte
	// without them produces a bundle Gatekeeper refuses to launch with a
	// message about being damaged.
	fmt.Fprintf(os.Stderr, "copying UTM.app to %s\n", AppPath)
	cp := exec.Command("ditto", src, AppPath)
	cp.Stdout, cp.Stderr = os.Stderr, os.Stderr
	if cErr := cp.Run(); cErr != nil {
		return fmt.Errorf("utmvm: copying UTM.app to /Applications: %w\n"+ //nolint:staticcheck // ST1005: multi-line on purpose — these tell the user the next command to run
			"  If this is a permissions error, /Applications may need admin rights;\n"+
			"  download UTM from https://mac.getutm.app and drag it across instead.", cErr)
	}
	return nil
}

// latestUTMDMG finds the macOS build in UTM's newest release.
//
// By name rather than by position: the release also carries .ipa builds for
// iOS and visionOS and a .deb, and any of them would download happily and be
// useless.
func latestUTMDMG() (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, utmReleaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("utmvm: asking GitHub for UTM's latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("utmvm: GitHub returned %s for UTM's releases", resp.Status)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("utmvm: parsing UTM's release: %w", err)
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, "UTM.dmg") {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("utmvm: UTM release %s has no UTM.dmg among its %d assets",
		rel.TagName, len(rel.Assets))
}

// ---- the unattend medium a VM installs from ----

// PayloadOptions describes what goes onto the unattend medium.
type PayloadOptions struct {
	// ProbeDir is an optional directory whose top-level files are copied to the
	// image ROOT — not a subdirectory. go-diskfs mangles Joliet names inside
	// nested directories into UCS-2 garbage; root entries survive.
	ProbeDir string

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
	if opts.ProbeDir != "" {
		dst := stage
		entries, err := os.ReadDir(opts.ProbeDir)
		if err != nil {
			return fmt.Errorf("probe dir: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := copyFile(filepath.Join(opts.ProbeDir, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	}

	return isoBuildImage(imagePath, stage, opts.SizeMiB)
}

// EnsureReady brings a VM to a state where commands can be run in it.
//
// A Windows guest reboots on its own — Windows Update did so mid-session here,
// dropping the agent with "Port is not connected" — and UTM's firmware does not
// reliably auto-boot afterwards. So "is the VM ready" cannot be assumed just
// because it was ready a minute ago, and every entry point that talks to the
// guest has to be able to recover rather than fail.
func EnsureReady(vmRef, bundlePath string, timeout time.Duration) error {
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
		if err := vm.waitForAgent(timeout); err == nil {
			return nil
		}
	}
	// Running but unreachable means it is sitting in the UEFI shell after a
	// reboot. Drive it out the same way an install would.
	return RunInstall(InstallOptions{VMRef: vmRef, BundlePath: bundlePath, Timeout: timeout})
}
func copyFile(src, dst string) error {
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

// utmContainerDir is UTM's sandbox container, where it keeps everything it
// owns: the bundles it boots and the guest tools it downloads.
func utmContainerDir(home string) string {
	return filepath.Join(home, "Library", "Containers", utmContainer, "Data")
}
