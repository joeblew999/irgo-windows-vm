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

	// The probe runner ships with the tool rather than being supplied by the
	// caller. It was previously expected to appear in the -probes directory,
	// which meant `probe` could never work as shipped: nothing generated it.
	//go:embed assets/run-all.cmd
	runAllCmd []byte
)

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
	// interactive install; from a CD it applies. See BuildISOImage.

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

	return BuildISOImage(imagePath, stage, opts.SizeMiB)
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
