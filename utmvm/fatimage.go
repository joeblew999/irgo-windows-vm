package utmvm

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
)

// BuildFATImage writes a FAT32 disk image containing srcDir.
//
// One image serves three readers, which is why FAT and not ISO9660:
//
//   - Windows Setup scans removable drives for autounattend.xml. It reads FAT
//     natively; an ISO9660 image attached as a disk is not reliably scanned.
//   - The UEFI shell reads FAT to find startup.nsh, which is what makes the VM
//     boot itself instead of sitting at a prompt.
//   - Windows, once installed, reads it as an ordinary drive to pick up the
//     probe binaries.
//
// Replaces the earlier `hdiutil create -fs MS-DOS` call so the tool works on
// any host Go runs on rather than only macOS.
func BuildFATImage(imagePath, srcDir string, sizeMiB int) error {
	if sizeMiB < 64 {
		// FAT32 needs a minimum cluster count; below ~32 MiB go-diskfs will
		// produce something Windows refuses to mount. 64 is a safe floor and
		// costs nothing on a sparse filesystem.
		sizeMiB = 64
	}
	_ = os.Remove(imagePath)

	size := int64(sizeMiB) << 20
	d, err := diskfs.Create(imagePath, size, diskfs.SectorSizeDefault)
	if err != nil {
		return fmt.Errorf("create image: %w", err)
	}
	defer d.Close()

	fs, err := d.CreateFilesystem(disk.FilesystemSpec{
		Partition:   0, // whole disk, no partition table — simplest thing all three readers accept
		FSType:      filesystem.TypeFat32,
		VolumeLabel: "UNATTEND",
	})
	if err != nil {
		return fmt.Errorf("create FAT32: %w", err)
	}

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// FAT paths are backslash-free and rooted; normalise whatever the host
		// gave us.
		target := "/" + filepath.ToSlash(rel)

		if info.IsDir() {
			return fs.Mkdir(target)
		}
		return copyIntoFS(fs, path, target)
	})
}

func copyIntoFS(fs filesystem.FileSystem, hostPath, target string) error {
	src, err := os.Open(hostPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := fs.OpenFile(target, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("create %s in image: %w", target, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// GuestToolsISO returns UTM's downloaded guest tools image, if present.
//
// Installing these inside the guest is what gives the QEMU guest agent, and
// therefore `utmctl exec` and `utmctl ip-address`. Without it a VM boots fine
// but cannot be driven from the host at all — which defeats the point of
// generating one from a script. UTM downloads the ISO on first use; there is
// no supported way to fetch it ourselves, so a missing file is reported rather
// than worked around.
// guestToolsPath is where UTM caches the guest tools, and therefore where a
// download of our own has to land for UTM and every generated VM to find it.
func guestToolsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Containers", "com.utmapp.UTM", "Data",
		"Library", "Application Support", "GuestSupportTools", "utm-guest-tools-latest.iso"), nil
}

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

// GuestToolsInstallCommand is the command an answer file should run at first
// logon to install the tools silently. Exposed so the answer-file template and
// the drive wiring cannot drift apart.
//
// The installer is an NSIS package; /S is its silent switch. It is found by
// letter-scan because the CD letter depends on how many volumes Windows
// enumerated ahead of it, which varies with how many drives the VM has.
func GuestToolsInstallCommand() string {
	var b strings.Builder
	b.WriteString(`cmd /c for %i in (D E F G H) do @if exist %i:\utm-guest-tools-*.exe start /wait %i:\utm-guest-tools-*.exe /S`)
	return b.String()
}
