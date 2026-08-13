package utmvm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	r.Running = strings.EqualFold(e.Status, "started")

	dir, err := DefaultVMDir()
	if err != nil {
		return r, err
	}
	r.Path = filepath.Join(dir, e.Name+".utm")
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
func Delete(ref string, force bool) (Removal, error) {
	r, err := InspectRemoval(ref)
	if err != nil {
		return r, err
	}
	if r.Running {
		if !force {
			return r, fmt.Errorf("%s is running; stop it first or pass -force", r.Name)
		}
		vm := Named(ref)
		_ = vm.Stop()
		for i := 0; i < 15; i++ {
			if st, _ := vm.Status(); !strings.EqualFold(st, "started") {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	// Immutable media has to be released before anything can unlink it.
	//
	// ProtectISO sets uchg on the ISO's INODE, and Create hardlinks that same
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
	reprotect := releaseImmutable(immutable, append(ISOSearchDirs(), dp.Cache, dp.Work))
	defer func() {
		for _, p := range reprotect {
			_ = ProtectISO(p)
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
	_ = exec.Command("utmctl", "delete", r.UUID).Run()
	if _, err := os.Stat(r.Path); err == nil {
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
		if st, err := ISOLinks(p, searchIn); err == nil {
			for _, other := range st.Found {
				if other != p {
					survivors = append(survivors, other)
				}
			}
		}
		_ = UnprotectISO(p)
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
