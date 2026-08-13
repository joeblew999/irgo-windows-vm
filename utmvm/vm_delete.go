package utmvm

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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
	r.Running = strings.EqualFold(e.Status, statusStarted)

	dir, err := DefaultVMDir()
	if err != nil {
		return r, err
	}
	r.Path = filepath.Join(dir, e.Name+bundleExt)
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
func Delete(ref string, force bool, log func(string, ...any)) (Removal, error) {
	step := func(f string, a ...any) {
		if log != nil {
			log(f, a...)
		}
	}
	r, err := InspectRemoval(ref)
	if err != nil {
		return r, err
	}
	step("· found %s (%s)", r.Name, HumanBytes(r.TotalBytes))
	if r.Running {
		if !force {
			return r, fmt.Errorf("%s is running; stop it first or pass -force", r.Name)
		}
		step("… stopping it")
		vm := Named(ref)
		_ = vm.Stop()
		for i := 0; i < 15; i++ {
			if !vm.IsRunning() {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	// Immutable media has to be released before anything can unlink it.
	//
	// isoProtect sets uchg on the ISO's INODE, and Create hardlinks that same
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
	if len(immutable) > 0 {
		step("… releasing %d protected file(s) so the bundle can be removed", len(immutable))
	}
	reprotect := releaseImmutable(immutable, append(isoSearchDirs(), dp.Cache, dp.Work))
	defer func() {
		for _, p := range reprotect {
			_ = isoProtect(p)
		}
		if len(reprotect) > 0 {
			step("· re-protected %d shared file(s)", len(reprotect))
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
	step("… asking UTM to delete it, so no phantom entry is left")
	_ = exec.Command("utmctl", "delete", r.UUID).Run()
	if _, err := os.Stat(r.Path); err == nil {
		step("… UTM left the bundle behind; removing it")
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
		if st, err := isoLinks(p, searchIn); err == nil {
			for _, other := range st.Found {
				if other != p {
					survivors = append(survivors, other)
				}
			}
		}
		_ = ISOUnprotect(p)
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
		"A VM started with `utmctl start` has no display window — use `irgo-winvm vm`, "+
		"which starts it through UTM so a window exists", vmName, strings.Join(titles, ", "))
}

// The bundle layout, declared once.
//
// These four names were spelled out at ten call sites across the package and
// the CLI — filepath.Join(dir, e.Name+".utm") and bundle+"/Data/disk.img" — so
// UTM's on-disk layout was knowledge every caller had to carry, and one of them
// used string concatenation rather than filepath.Join.
const (
	bundleExt   = ".utm"
	bundleData  = "Data"
	diskImage   = "disk.img"
	installISO  = "install.iso"
	unattendISO = "unattend.iso"
)

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

// isoSearchDirs are the places worth looking for other names for an ISO: where
// a browser puts a download, and where UTM keeps the bundles that use it.
//
// It lives here rather than with the ISO code because knowing about VM bundles
// is vm-delete's business. Getting media has nothing to do with UTM.
func isoSearchDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs := []string{filepath.Join(home, "Downloads")}
	if vmDir, err := DefaultVMDir(); err == nil {
		dirs = append(dirs, vmDir)
	}
	return dirs
}
