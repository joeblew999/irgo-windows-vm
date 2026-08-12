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
package utmvm

import (
	_ "embed"
	"fmt"
	"strings"
	"unicode/utf8"
)

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
