package utmvm

import (
	"bytes" //nolint:gosec // the catalog publishes SHA-1; the choice is Microsoft's
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

// What a Windows ISO is, and the external tools that read one.

// Every fact about Windows media lives here, so nothing else has to know the
// shape of it. These were spread across four files, which is how the built-ISO
// name came to be spelled three times.
const (
	// ISOCatalogURL is Microsoft's ISOGet Creation ISOTool catalog for Windows 11.
	//
	// linkid=2156292 is Windows 11; LinkId=841361 is Windows 10 and looks close
	// enough to grab by mistake — which is why the wrong one is named below
	// rather than left as a number somebody might reintroduce.
	ISOCatalogURL = "https://go.microsoft.com/fwlink/?linkid=2156292"

	// ISOCatalogURLWindows10 exists to be recognised, never fetched.
	ISOCatalogURLWindows10 = "https://go.microsoft.com/fwlink/?LinkId=841361"

	// isoName is what a downloaded or built Windows ISO is called on disk.
	isoName = "win11-arm64.iso"

	// builtISOName distinguishes media this tool mastered from media Microsoft
	// served, because the two fail differently and telling them apart matters
	// when a boot goes wrong.
	builtISOName = "win11-arm64-built.iso"

	// esdName is the compressed image the catalog serves, before expansion.
	esdName = "win11-arm64.esd"

	// scanSuffix names the sidecar holding a cached scan verdict. Answering
	// "is this ARM64" means reading the whole 5.27 GB, which cost 77 seconds on
	// every command until the answer was kept.
	scanSuffix = ".scan"

	// brewBin is where Homebrew puts executables on Apple Silicon. Named here
	// because the ISO tools are the only thing this project installs, and
	// PATH alone cannot answer "was this installed by us and where".
	brewBin = "/opt/homebrew/bin"

	// isoDirName is where every ISO artefact lives, under the user's data
	// directory. The ISO code owns its own location rather than being handed
	// one: "where does the ISO go" is an ISO question, and routing it through
	// a shared Paths struct is how it came to be answerable six different ways
	// by six environment variables.
	isoDirName = "media"

	// minWindowsISOBytes separates Windows media from the small ISOs that share
	// a directory with it: the generated answer file is 32 MB and UTM's guest
	// tools are 121 MB, and neither will ever install an operating system.
	minWindowsISOBytes = 1 << 30
)

type ISOInfo struct {
	// IsARM64 reports whether the ARM64 UEFI bootloader is present. An x86-64
	// ISO on Apple Silicon boots to a black screen with no diagnostic at all,
	// so this is the single most valuable check here.
	IsARM64 bool

	// HasNoPromptLoader reports whether cdboot_noprompt.efi is present. The
	// default loader (efi/boot/bootaa64.efi) prints "Press any key to boot from
	// CD or DVD" and gives up after about five seconds, which stops an
	// "unattended" install before it starts. The _noprompt variant is what
	// makes hands-off installation possible.
	HasNoPromptLoader bool

	// SizeBytes is the image size, useful for a sanity check: a Windows 11
	// image is several GB, and a few hundred MB means a truncated download.
	SizeBytes int64
}

// ISOInspect reports what matters about Windows install media.
//
// Implementation note, because the obvious approach does not work: Windows 11
// ISOs are UDF, not ISO9660 — they have to be, since install.wim exceeds
// ISO9660's 4 GB file limit. Filesystem libraries that read the ISO9660
// descriptor find only a stub and fail to traverse it.
//
// Rather than embed a UDF reader for two yes/no questions, this streams the
// image looking for the filenames in the directory structures. UDF stores them
// as either 8-bit or UTF-16BE strings, so both encodings are searched. That is
// a heuristic — it proves a name is present, not that it is at a particular
// path — but for "is there an ARM64 bootloader in here" it is decisive, and it
// is honest about being a scan rather than pretending to be a parse.
// isoVerdict caches what a scan found, keyed by size and mtime. The ISO is
// 5.27 GB and immutable; re-reading all of it on every command to answer "is
// this ARM64" cost 77 seconds per invocation.
func isoCachedVerdict(path string) (ISOInfo, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return ISOInfo{}, false
	}
	b, rErr := os.ReadFile(path + scanSuffix)
	if rErr != nil {
		return ISOInfo{}, false
	}
	var size, mod int64
	var arm int
	if _, sErr := fmt.Sscanf(string(b), "%d %d %d", &size, &mod, &arm); sErr != nil {
		return ISOInfo{}, false
	}
	if size != fi.Size() || mod != fi.ModTime().UnixNano() {
		return ISOInfo{}, false
	}
	return ISOInfo{SizeBytes: fi.Size(), IsARM64: arm == 1}, true
}

func isoStoreVerdict(path string, info ISOInfo) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	arm := 0
	if info.IsARM64 {
		arm = 1
	}
	_ = os.WriteFile(path+scanSuffix, []byte(fmt.Sprintf("%d %d %d", fi.Size(), fi.ModTime().UnixNano(), arm)), 0o644)
}

func ISOInspect(path string) (ISOInfo, error) {
	if v, ok := isoCachedVerdict(path); ok {
		return v, nil
	}
	info, err := isoInspectSlow(path)
	if err == nil {
		isoStoreVerdict(path, info)
	}
	return info, err
}

func isoInspectSlow(path string) (ISOInfo, error) {
	var info ISOInfo

	f, err := os.Open(path)
	if err != nil {
		return info, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return info, err
	}
	info.SizeBytes = st.Size()

	needles := [][]byte{
		isoEncode8("BOOTAA64.EFI"), isoEncode16be("bootaa64.efi"),
		isoEncode8("CDBOOT_NOPROMPT.EFI"), isoEncode16be("cdboot_noprompt.efi"),
	}
	found := make([]bool, len(needles))

	// A modest buffer with an overlap, so a name spanning a chunk boundary is
	// still matched. The overlap must exceed the longest needle.
	upNeedles := make([][]byte, len(needles))
	for i, n := range needles {
		upNeedles[i] = bytes.ToUpper(n)
	}

	const chunk = 1 << 20
	const overlap = 128
	buf := make([]byte, chunk+overlap)
	var carry int

	for {
		n, err := io.ReadFull(f, buf[carry:chunk+overlap])
		if n > 0 {
			window := buf[:carry+n]
			// Uppercased ONCE per chunk, and the needles once at the top —
			// bytes.ToUpper on binary data leaves its ASCII fast path and
			// re-encodes every invalid byte as U+FFFD, allocating up to 3 MB
			// per megabyte scanned. Doing that per needle per chunk is what
			// made checking a cached 5 GB ISO take 77 seconds.
			up := bytes.ToUpper(window)
			for i, needle := range upNeedles {
				if found[i] {
					continue
				}
				if bytes.Contains(up, needle) {
					found[i] = true
				}
			}
			if isoAllTrue(found) {
				break
			}
			// Keep the tail so the next pass can match across the seam.
			if len(window) > overlap {
				copy(buf, window[len(window)-overlap:])
				carry = overlap
			} else {
				carry = len(window)
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return info, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	info.IsARM64 = found[0] || found[1]
	info.HasNoPromptLoader = found[2] || found[3]
	return info, nil
}

func isoAllTrue(b []bool) bool {
	for _, v := range b {
		if !v {
			return false
		}
	}
	return true
}

// isoEncode8 is the plain 8-bit form UDF uses for names that fit in Latin-1.
func isoEncode8(s string) []byte { return []byte(strings.ToUpper(s)) }

// isoEncode16be is UDF's UTF-16BE form. Note big-endian: UDF differs from the
// little-endian UTF-16 used elsewhere in Windows, and searching for the wrong
// byte order silently finds nothing.
func isoEncode16be(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range utf16.Encode([]rune(s)) {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

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

// ISOTool is an external program this package needs but cannot replace.
type ISOTool struct {
	Name    string // executable name
	Formula string // Homebrew formula that provides it
	Why     string
	Path    string // resolved, empty when missing
}

// Found reports whether the tool is installed.
func (t ISOTool) Found() bool { return t.Path != "" }

// Where is the tool's location: where it is, or where it would go.
//
// Never empty. "not installed" with no path cannot be checked and cannot be
// undone by hand, which is the whole reason these commands print locations.
func (t *ISOTool) Where() string {
	if t.resolve() {
		return t.Path
	}
	return filepath.Join(brewBin, t.Name) + " (not installed)"
}

// resolve looks the executable up on PATH, recording where it was found. The
// one place in this package that asks that question about an external tool.
func (t *ISOTool) resolve() bool {
	p, err := exec.LookPath(t.Name)
	if err != nil {
		t.Path = ""
		return false
	}
	t.Path = p
	return true
}

// Install is the command that provides it.
func (t ISOTool) Install() string { return "brew install " + t.Formula }

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
func isoMasterers() []ISOTool {
	return []ISOTool{
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

// ISOWimTool is the one thing with no Go alternative: reading LZMS-compressed ESD
// archives. No Go implementation exists that is worth trusting.
func ISOWimTool() ISOTool {
	t := ISOTool{
		Name:    "wimlib-imagex",
		Formula: "wimlib",
		Why:     "reads Microsoft's .esd archives (LZMS compression, which no Go library implements).",
	}
	t.resolve()
	return t
}

// ISOFindMasterer returns the first available ISO masterer, or the list of
// candidates when none is installed.
func ISOFindMasterer() (ISOTool, []ISOTool) {
	all := isoMasterers()
	for i := range all {
		if all[i].resolve() {
			return all[i], all
		}
	}
	return ISOTool{}, all
}

// ISOExpandESD lays a Microsoft .esd archive out as a directory of installation
// media, ready for ISOBuild.
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
func ISOExpandESD(esd, dir string, progress func(step string)) error {
	wim := ISOWimTool()
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

	n, err := isoESDImageCount(wim.Path, esd)
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

// isoESDImageCount reads how many images an ESD holds. There is no fixed number:
// it varies by release and by which editions Microsoft bundled.
func isoESDImageCount(wimPath, esd string) (int, error) {
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

// ISORemasterOptions configures ISOBuild.
type ISORemasterOptions struct {
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
func (o ISORemasterOptions) BootImage() (rel string, noPrompt bool) {
	const dir = "efi/microsoft/boot"
	if o.NoPrompt {
		if _, err := os.Stat(filepath.Join(o.Source, dir, "efisys_noprompt.bin")); err == nil {
			return dir + "/efisys_noprompt.bin", true
		}
	}
	return dir + "/efisys.bin", false
}

// ISOBuild writes a bootable Windows ARM64 ISO from a directory of media.
func ISOBuild(opts ISORemasterOptions) error {
	if fi, err := os.Stat(opts.Source); err != nil || !fi.IsDir() {
		return fmt.Errorf("utmvm: source %s is not a directory", opts.Source)
	}
	if err := isoCheckWritable(opts.Output); err != nil {
		return err
	}
	if _, err := os.Stat(opts.Output); err == nil {
		return fmt.Errorf("utmvm: %s already exists — this never overwrites; move it aside", opts.Output)
	}

	tool, candidates := ISOFindMasterer()
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
		//
		// -e, not -b, for the same reason as above: -b marks a BIOS entry, which
		// a UEFI-only ARM64 machine ignores. This branch said -b and so produced
		// a correctly sized, correctly named, non-bootable ISO.
		args = []string{
			"-udf", "-iso-level", "3",
			"-V", label,
			"-e", boot, "-no-emul-boot",
			"-o", opts.Output, opts.Source,
		}
	default:
		return fmt.Errorf("utmvm: no argument recipe for ISO masterer %q", tool.Name)
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

// ISOBuildImage writes an ISO9660 image containing srcDir.
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
func ISOBuildImage(imagePath, srcDir string, sizeMiB int) error {
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
		return isoCopyInto(fs, path, target)
	}); err != nil {
		return err
	}

	iso, ok := fs.(*iso9660.FileSystem)
	if !ok {
		return fmt.Errorf("unexpected filesystem type %T", fs)
	}
	// Without Finalize the descriptors are never written and the image is not
	// a readable ISO at all.
	if err := iso.Finalize(iso9660.FinalizeOptions{
		VolumeIdentifier: "UNATTEND",
		RockRidge:        true,
		// Joliet is what Windows reads. Without it the image is 8.3 only and
		// autounattend.xml becomes AUTOUNAT.XML — a name Setup never looks for,
		// so the install silently runs interactive.
		Joliet:          true,
		DeepDirectories: true,
	}); err != nil {
		return err
	}
	return isoTrimToVolumeSize(imagePath)
}

// isoTrimToVolumeSize cuts the image down to the size its Primary Volume
// Descriptor declares.
//
// go-diskfs needs a size up front and leaves whatever is left over as a tail of
// zeros past the end of the volume. Readers disagree about that: macOS mounts
// such an image happily, which is why it looked fine, while Windows Setup did
// not pick up autounattend.xml from one and fell back to an interactive install
// with no error. The working reference image, built by hdiutil, has no tail.
//
// Volume space size lives at offset 0x8050 as a little-endian block count, with
// 2048-byte logical blocks.
func isoTrimToVolumeSize(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	var b [4]byte
	if _, err := f.ReadAt(b[:], 0x8050); err != nil {
		return fmt.Errorf("reading volume size: %w", err)
	}
	blocks := int64(binary.LittleEndian.Uint32(b[:]))
	if blocks == 0 {
		return fmt.Errorf("ISO reports zero volume blocks; image is malformed")
	}
	want := blocks * 2048

	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= want {
		return nil
	}
	return f.Truncate(want)
}

func isoCopyInto(fs filesystem.FileSystem, hostPath, target string) error {
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

// Protecting the working ISO from the tooling that will replace it.
//
// The Windows ISO is hardlinked, deliberately: ~/Downloads has the download,
// .cache/win11-arm64.iso is what this repo refers to, and the VM bundle's
// Data/install.iso is what UTM boots. One set of blocks, three names, 5 GB
// once instead of 15.
//
// That is also the trap. A hardlink is not a copy — every name is the same
// inode — so anything that opens ANY of those paths for writing destroys the
// other two. `fetch-iso` is the obvious candidate: the natural implementation
// downloads to .cache/win11-arm64.iso, and the natural way to do that is
// O_CREATE|O_TRUNC, which empties the file UTM is booting from. The failure is
// silent until the next install, and the ISO is 5 GB of gated download to get
// back.
//
// So the file is made immutable instead of trusted to nobody's carelessness.
// macOS's uchg flag is per-inode, which is exactly the granularity wanted: set
// it through one path and all three names are protected, including the one
// inside UTM's container that this repo never mentions.

// ISOStatus is what is known about one ISO and every other name for it.

// Ensure makes the tool available, installing it with Homebrew if it is not.
//
// Building an ISO is the only thing in this project that needs software it
// cannot ship, so this is the only place that installs any. It is paired with
// Remove: whatever `iso` puts on the machine, `iso-delete` takes off again.
func (t *ISOTool) Ensure() error {
	if t.resolve() {
		return nil
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		return fmt.Errorf("%s is needed to %s, and Homebrew is not here to install it.\n"+
			"  Install it yourself: %s", t.Name, t.Why, t.Install())
	}
	fmt.Fprintf(os.Stderr, "installing %s…\n", t.Formula)
	cmd := exec.Command(brew, "install", t.Formula) //nolint:gosec // from this package's own table
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if rErr := cmd.Run(); rErr != nil {
		return fmt.Errorf("installing %s: %w\n  Try it by hand: %s", t.Name, rErr, t.Install())
	}
	if !t.resolve() {
		// Homebrew's bin is not always on the PATH this process inherited.
		if _, sErr := os.Stat(filepath.Join(brewBin, t.Name)); sErr == nil {
			t.Path = filepath.Join(brewBin, t.Name)
			return nil
		}
		return fmt.Errorf("%s installed but %s is not on PATH", t.Formula, t.Name)
	}
	return nil
}

// Remove uninstalls the tool, and reports whether it actually went.
//
// Only Homebrew installs are removed. A binary somewhere else was put there by
// somebody for their own reasons, and a command called iso-delete has no
// business deciding it knows better — so it says where it is and leaves it.
func (t *ISOTool) Remove() (string, error) {
	if !t.resolve() {
		return "", nil // not here; nothing to undo
	}
	where := t.Path
	brew, err := exec.LookPath("brew")
	if err != nil {
		return "", fmt.Errorf("%s is at %s and Homebrew is not here to remove it", t.Name, where)
	}
	out, uErr := exec.Command(brew, "uninstall", t.Formula).CombinedOutput() //nolint:gosec // own table
	if uErr != nil {
		return "", fmt.Errorf("uninstalling %s: %w: %s", t.Formula, uErr, strings.TrimSpace(string(out)))
	}
	return where, nil
}

// ISOTools are the external programs building an ISO needs: the WIM expander,
// and ONE masterer.
//
// One, not both. isoMasterers lists xorriso and cdrtools in preference order
// because either will do — returning the whole list made `iso` install the
// fallback as well, which is 200 MB of cdrtools nobody asked for on a machine
// that already had xorriso.
//
// In one place so `iso` and `iso-delete` cannot disagree about what they are.
func ISOTools() []ISOTool {
	tools := []ISOTool{ISOWimTool()}
	masterers := isoMasterers()
	for i := range masterers {
		if masterers[i].resolve() {
			return append(tools, masterers[i])
		}
	}
	if len(masterers) > 0 {
		return append(tools, masterers[0]) // none present: install the preferred one
	}
	return tools
}

// ISOFiles is everything `iso` can leave in the media directory, so
// `iso-delete` cannot disagree with `iso` about what "the media" is.
//
// It did: iso built win11-arm64-built.iso and left a 4.2 GB .esd behind, while
// iso-delete only knew win11-arm64.iso — so deleting the media removed nothing
// and reported success.
func ISOFiles() []string {
	var out []string
	for _, f := range []string{ISOPath(), ISOBuiltPath(), ISOESDPath()} {
		out = append(out, f, f+scanSuffix)
	}
	return out
}

// ISODir is where ISO artefacts live. One place, and this file decides it.
func ISODir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Library", "Application Support", "irgo-winvm", isoDirName)
}

// ISOPath is the Windows media this tool downloads or builds.
func ISOPath() string { return filepath.Join(ISODir(), isoName) }

// ISOBuiltPath is media this tool mastered itself, kept under a separate name
// because the two fail differently and telling them apart matters when a boot
// goes wrong.
func ISOBuiltPath() string { return filepath.Join(ISODir(), builtISOName) }

// ISOESDPath is the compressed image the catalog serves, before expansion.
func ISOESDPath() string { return filepath.Join(ISODir(), esdName) }
