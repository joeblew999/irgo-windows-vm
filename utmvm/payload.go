package utmvm

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// The answer file and boot script are embedded so the binary is self-contained:
// a developer clones, builds, runs. Shipping them as loose files next to the
// executable is how tools break when moved.
var (
	//go:embed assets/autounattend.xml
	autounattendXML []byte

	//go:embed assets/startup.nsh
	startupNSH []byte
)

// PayloadOptions describes what goes onto the unattend medium.
type PayloadOptions struct {
	// ProbeDir is an optional directory of files copied to \probe on the image.
	// Typically the cross-compiled Windows test binaries.
	ProbeDir string

	// SizeMiB sizes the image. It must fit the probes with room to spare.
	SizeMiB int
}

// BuildPayload writes the FAT image that makes the install unattended.
//
// A single image, because three different readers each need part of it:
// Windows Setup finds autounattend.xml at the root, the UEFI shell finds
// startup.nsh at the root, and the installed Windows finds \probe. Splitting
// them across separate media was the earlier design and it meant two images
// and an extra drive for no benefit.
func BuildPayload(imagePath string, opts PayloadOptions) error {
	if opts.SizeMiB == 0 {
		opts.SizeMiB = 256
	}

	stage, err := os.MkdirTemp("", "irgo-winvm-payload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	if err := os.WriteFile(filepath.Join(stage, "autounattend.xml"), autounattendXML, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "startup.nsh"), startupNSH, 0o644); err != nil {
		return err
	}

	if opts.ProbeDir != "" {
		dst := filepath.Join(stage, "probe")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
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

	return BuildFATImage(imagePath, stage, opts.SizeMiB)
}

func copyFile(src, dst string) error {
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

// AnswerFile exposes the embedded answer file so callers can inspect or
// override it without reaching into the package.
func AnswerFile() []byte { return autounattendXML }

// StartupScript exposes the embedded UEFI boot script for the same reason.
func StartupScript() []byte { return startupNSH }
