package utmvm

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	cfg := Config{
		Name:       opts.Name,
		UUID:       uuid,
		MemoryMiB:  opts.MemoryMiB,
		CPUCount:   opts.CPUCount,
		MACAddress: randomMAC(),
	}

	id1, _ := newUUID()
	id2, _ := newUUID()
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
func randomMAC() string {
	var b [3]byte
	rand.Read(b[:])
	return fmt.Sprintf("52:54:00:%02X:%02X:%02X", b[0], b[1], b[2])
}
