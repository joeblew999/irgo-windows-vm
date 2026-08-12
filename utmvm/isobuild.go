package utmvm

// Building bootable Windows ARM64 media, as a command rather than a recipe.
//
// The steps are not hard. Getting them wrong is silent, which is why they are
// here in Go with the reasons attached, instead of in a shell script somebody
// has to read before they can trust it:
//
//   - the ISO must be UDF, or ISO9660 level 3, because install.wim is 4.099 GiB
//     and ISO9660's file-size limit is 4 GiB;
//   - the El Torito entry must be marked EFI (platform 0xEF), not BIOS;
//   - the boot image should be efisys_noprompt.bin, not efisys.bin, because the
//     difference is whether the disc stops at "Press any key to boot from CD";
//   - and `hdiutil makehybrid` cannot do it at all — measured, twice, see
//     RESULTS.md. An external masterer is required.
//
// Only the masterer is external. Everything else — mounting, copying, checking,
// refusing to overwrite media in use — is done here, so the part that can
// damage something is the part we control.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool is an external program this package needs but cannot replace.
type Tool struct {
	Name    string // executable name
	Formula string // Homebrew formula that provides it
	Why     string
	Path    string // resolved, empty when missing
}

// Found reports whether the tool is installed.
func (t Tool) Found() bool { return t.Path != "" }

// resolve looks the executable up on PATH, recording where it was found. The
// one place in this package that asks that question about an external tool.
func (t *Tool) resolve() bool {
	p, err := exec.LookPath(t.Name)
	if err != nil {
		t.Path = ""
		return false
	}
	t.Path = p
	return true
}

// Install is the command that provides it.
func (t Tool) Install() string { return "brew install " + t.Formula }

// isoMasterers are the programs that can write a bootable Windows ISO, in
// order of preference.
//
// xorriso is first because it is the smaller, more actively maintained
// dependency and its ISO9660 level 3 multi-extent support covers the 4 GiB
// problem. cdrtools is the fallback for the case where Windows Setup turns out
// to need genuine UDF — Microsoft ships UDF, and their own CDFS driver handles
// multi-extent poorly, so this is a real possibility rather than a hedge.
//
// `hdiutil` is deliberately absent. It is built into macOS and would need no
// installation, and it does not work: two images mastered with it enumerate
// correctly in UTM's firmware and then refuse to boot. CrystalFetch reached the
// same conclusion — it bundles mkisofs and has its hdiutil line commented out.
func isoMasterers() []Tool {
	return []Tool{
		{
			Name:    "xorriso",
			Formula: "xorriso",
			Why:     "writes the bootable ISO. ISO9660 level 3 carries the 4.099 GiB install.wim as a multi-extent file.",
		},
		{
			Name:    "mkisofs",
			Formula: "cdrtools",
			Why:     "writes the bootable ISO, with real UDF — needed if Windows Setup rejects multi-extent ISO9660.",
		},
	}
}

// WimTool is the one thing with no Go alternative: reading LZMS-compressed ESD
// archives. No Go implementation exists that is worth trusting.
func WimTool() Tool {
	t := Tool{
		Name:    "wimlib-imagex",
		Formula: "wimlib",
		Why:     "reads Microsoft's .esd archives (LZMS compression, which no Go library implements).",
	}
	t.resolve()
	return t
}

// FindMasterer returns the first available ISO masterer, or the list of
// candidates when none is installed.
func FindMasterer() (Tool, []Tool) {
	all := isoMasterers()
	for i := range all {
		if all[i].resolve() {
			return all[i], all
		}
	}
	return Tool{}, all
}

// ExpandESD lays a Microsoft .esd archive out as a directory of installation
// media, ready for BuildISO.
//
// The sequence is not guessable and each step has a reason. It follows the one
// shipped, working reference — CrystalFetch's `esd2iso.sh`, itself Paul
// Rockwell's `w11arm_esd2iso` — because the alternative is rediscovering its
// scars:
//
//	image 1      the media layout: boot files, setup.exe, efi/
//	image 2      Windows PE                  -> sources/boot.wim
//	image 3      Windows Setup, MARKED BOOT  -> sources/boot.wim
//	images 4..N  the Windows editions        -> sources/install.wim
//
// Image 3 must be exported with --boot. Without it Setup fails, and it fails
// late, after the disc has booted and looked fine.
//
// boot.wim is LZX because Windows PE's loader reads it before anything that
// understands LZMS exists; install.wim is LZMS because it is the 4 GB one and
// compression is what keeps it near the disc size at all.
func ExpandESD(esd, dir string, progress func(step string)) error {
	wim := WimTool()
	if eErr := wim.Ensure(); eErr != nil {
		return eErr
	}
	if _, err := os.Stat(esd); err != nil {
		return fmt.Errorf("utmvm: %s: %w", esd, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	say := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	n, err := esdImageCount(wim.Path, esd)
	if err != nil {
		return err
	}
	if n < 4 {
		return fmt.Errorf("utmvm: %s holds %d images; Windows media has at least 4", esd, n)
	}

	run := func(what string, args ...string) error {
		say(what)
		cmd := exec.Command(wim.Path, args...) //nolint:gosec // args are built here
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("utmvm: %s: %w", what, err)
		}
		return nil
	}

	if err := run("laying out the media (image 1)", "apply", esd, "1", dir); err != nil {
		return err
	}

	bootWim := filepath.Join(dir, "sources", "boot.wim")
	if err := os.MkdirAll(filepath.Dir(bootWim), 0o755); err != nil {
		return err
	}
	if err := run("exporting Windows PE (image 2)",
		"export", esd, "2", bootWim, "--compress=LZX", "--chunk-size", "32K"); err != nil {
		return err
	}
	// --boot is the one that bites: Setup fails without it, long after boot.
	if err := run("exporting Windows Setup (image 3, marked bootable)",
		"export", esd, "3", bootWim, "--compress=LZX", "--chunk-size", "32K", "--boot"); err != nil {
		return err
	}

	installWim := filepath.Join(dir, "sources", "install.wim")
	for i := 4; i <= n; i++ {
		if err := run(fmt.Sprintf("exporting edition (image %d of %d)", i, n),
			"export", esd, fmt.Sprint(i), installWim, "--compress=LZMS", "--chunk-size", "128K"); err != nil {
			return err
		}
	}
	return nil
}

// esdImageCount reads how many images an ESD holds. There is no fixed number:
// it varies by release and by which editions Microsoft bundled.
func esdImageCount(wimPath, esd string) (int, error) {
	out, err := exec.Command(wimPath, "info", esd).Output() //nolint:gosec // wimPath resolved by LookPath
	if err != nil {
		return 0, fmt.Errorf("utmvm: reading %s: %w", esd, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "Image Count:") {
			continue
		}
		var n int
		if _, sErr := fmt.Sscanf(strings.TrimSpace(line), "Image Count: %d", &n); sErr == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("utmvm: %s: could not find an image count in wimlib's output", esd)
}

// RemasterOptions configures BuildISO.
type RemasterOptions struct {
	// Source is a directory holding the complete media layout.
	Source string

	// Output is the ISO to write. It must not exist, must not be immutable,
	// must not be hardlinked, and must not be inside a VM bundle.
	Output string

	// Label is the volume identifier. Windows does not require a particular
	// one, but keeping the source's makes a rebuilt image comparable to the
	// original.
	Label string

	// NoPrompt uses efisys_noprompt.bin when the media provides it, so the disc
	// boots without "Press any key to boot from CD or DVD".
	//
	// This is the reason building our own media is worth doing at all: the
	// alternative, which this repository does today, is typing a path at the
	// UEFI shell and firing eight keypresses over six seconds — a hack the
	// README documents as costing hours and as having once destroyed an install
	// when surplus presses reached Setup's UI.
	NoPrompt bool
}

// BootImage returns the El Torito image to use, and whether it is the
// no-prompt variant.
func (o RemasterOptions) BootImage() (rel string, noPrompt bool) {
	const dir = "efi/microsoft/boot"
	if o.NoPrompt {
		if _, err := os.Stat(filepath.Join(o.Source, dir, "efisys_noprompt.bin")); err == nil {
			return dir + "/efisys_noprompt.bin", true
		}
	}
	return dir + "/efisys.bin", false
}

// BuildISO writes a bootable Windows ARM64 ISO from a directory of media.
func BuildISO(opts RemasterOptions, paths Paths) error {
	if fi, err := os.Stat(opts.Source); err != nil || !fi.IsDir() {
		return fmt.Errorf("utmvm: source %s is not a directory", opts.Source)
	}
	if err := paths.CheckWritable(opts.Output); err != nil {
		return err
	}
	if _, err := os.Stat(opts.Output); err == nil {
		return fmt.Errorf("utmvm: %s already exists — this never overwrites; move it aside", opts.Output)
	}

	tool, candidates := FindMasterer()
	if !tool.Found() {
		tool = candidates[0]
		if eErr := tool.Ensure(); eErr != nil {
			return eErr
		}
	}

	boot, noPrompt := opts.BootImage()
	if _, err := os.Stat(filepath.Join(opts.Source, boot)); err != nil {
		return fmt.Errorf("utmvm: %s has no El Torito boot image at %s — is it Windows media?", opts.Source, boot)
	}

	label := opts.Label
	if label == "" {
		label = "WINDOWS_ARM64"
	}

	var args []string
	switch tool.Name {
	case "xorriso":
		// -e, not -b: it marks the El Torito entry as EFI (platform 0xEF),
		// which is what a UEFI-only ARM64 disc boots from. -b describes a BIOS
		// entry and the firmware ignores it.
		//
		// -iso-level 3 is what permits install.wim past 4 GiB, as a multi-extent
		// file. No -udf: xorriso does not write UDF at all (that is cdrtools).
		args = []string{
			"-as", "mkisofs",
			"-iso-level", "3",
			"-V", label,
			"-e", boot, "-no-emul-boot",
			"-o", opts.Output, opts.Source,
		}
	case "mkisofs":
		// cdrtools does write UDF, which is what Microsoft's own media uses.
		args = []string{
			"-udf", "-iso-level", "3",
			"-V", label,
			"-b", boot, "-no-emul-boot",
			"-o", opts.Output, opts.Source,
		}
	}

	cmd := exec.Command(tool.Path, args...) //nolint:gosec // arguments are built above, not user-supplied strings
	cmd.Stdout = os.Stderr                  // progress belongs on stderr; stdout is for results
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// A partial ISO is worse than none: it is the right size to look
		// plausible and will fail as an unbootable VM later.
		_ = os.Remove(opts.Output)
		return fmt.Errorf("utmvm: %s failed: %w", tool.Name, err)
	}

	fi, err := os.Stat(opts.Output)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s) with %s\n", opts.Output, HumanBytes(fi.Size()), tool.Name)
	if noPrompt {
		fmt.Printf("  boot image: %s — no \"Press any key to boot from CD\"\n", boot)
	} else {
		fmt.Printf("  boot image: %s — this disc WILL stop at \"Press any key to boot from CD\"\n", boot)
	}
	return nil
}
