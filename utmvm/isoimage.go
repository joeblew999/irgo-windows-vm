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
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

// BuildISOImage writes an ISO9660 image containing srcDir.
//
// The answer file must be on an ISO9660 CD, not a FAT disk. This is not a
// preference — it was established by regression. An earlier version shipped the
// payload as a FAT removable disk, on the reasoning that Windows Setup scans
// removable drives and the UEFI shell can read FAT from the same image. Setup
// did not pick up autounattend.xml from it, and the install fell back to
// interactive with no error explaining why. Attached as a CD it works: Setup
// applied the DiskConfiguration and partitioned the disk exactly as specified.
//
// Joliet is enabled so long filenames survive; plain ISO9660 would truncate
// autounattend.xml to 8.3 and Setup would never find it.
func BuildISOImage(imagePath, srcDir string, sizeMiB int) error {
	if sizeMiB < 16 {
		sizeMiB = 16
	}
	_ = os.Remove(imagePath)

	// ISO9660 requires a 2048-byte logical block; the 512-byte default is
	// rejected outright.
	d, err := diskfs.Create(imagePath, int64(sizeMiB)<<20, diskfs.SectorSize(2048))
	if err != nil {
		return fmt.Errorf("create image: %w", err)
	}
	defer d.Close()

	fs, err := d.CreateFilesystem(disk.FilesystemSpec{
		Partition:   0,
		FSType:      filesystem.TypeISO9660,
		VolumeLabel: "UNATTEND",
	})
	if err != nil {
		return fmt.Errorf("create ISO9660: %w", err)
	}

	if err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
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
		target := "/" + filepath.ToSlash(rel)
		if info.IsDir() {
			return fs.Mkdir(target)
		}
		return copyIntoISO(fs, path, target)
	}); err != nil {
		return err
	}

	iso, ok := fs.(*iso9660.FileSystem)
	if !ok {
		return fmt.Errorf("unexpected filesystem type %T", fs)
	}
	// Without Finalize the descriptors are never written and the image is not
	// a readable ISO at all.
	return iso.Finalize(iso9660.FinalizeOptions{
		VolumeIdentifier: "UNATTEND",
		RockRidge:        true,
		// Joliet is what Windows reads. Without it the image is 8.3 only and
		// autounattend.xml becomes AUTOUNAT.XML — a name Setup never looks for,
		// so the install silently runs interactive.
		Joliet:          true,
		DeepDirectories: true,
	})
}

func copyIntoISO(fs filesystem.FileSystem, hostPath, target string) error {
	src, err := os.Open(hostPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := fs.OpenFile(target, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("create %s in image: %w", strings.TrimPrefix(target, "/"), err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}
