package utmvm

import (

	//nolint:gosec // the catalog publishes SHA-1; the choice is Microsoft's

	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Finding every name for an ISO, protecting it, and removing it.

type ISOStatus struct {
	Path      string
	Bytes     int64
	Links     int      // how many names this inode has, per the filesystem
	Found     []string // the ones that could be located
	Protected bool
}

// ISOLinks reports an ISO's size, protection, and every other path that shares
// its blocks.
//
// searchIn bounds the hunt for sibling names. There is no reverse index from
// inode to paths on macOS, so they have to be looked for; Links is the count
// the filesystem reports and is authoritative, while Found is only what was
// looked for and located. Links > len(Found) means a name exists somewhere not
// searched — a Time Machine local snapshot, usually — and is not cause for
// alarm.
func ISOLinks(path string, searchIn []string) (ISOStatus, error) {
	st := ISOStatus{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return st, err
	}
	st.Bytes = fi.Size()

	ino, nlink, ok := inodeInfo(path)
	if !ok {
		return st, fmt.Errorf("utmvm: cannot stat %s", path)
	}
	st.Links = int(nlink)

	if flags, ok := fileFlags(path); ok {
		st.Protected = flags&uchgFlag != 0
	}

	for _, dir := range searchIn {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable subtree just yields no matches
			}
			if got, _, ok := inodeInfo(p); ok && got == ino {
				st.Found = append(st.Found, p)
			}
			return nil
		})
	}
	return st, nil
}

// ProtectISO makes the ISO immutable: it cannot be written, truncated, renamed
// or deleted until UnprotectISO clears the flag. Idempotent.
func ProtectISO(path string) error { return chflags(path, true) }

// UnprotectISO clears the immutable flag, so the ISO can be replaced or the VM
// holding a hardlink to it can be deleted. Idempotent.
//
// `irgo-winvm vm-delete` needs this: rm refuses an immutable file, and the VM
// bundle's install.iso is the same inode as the protected one.
func UnprotectISO(path string) error { return chflags(path, false) }

func chflags(path string, set bool) error {
	if !immutableSupported {
		return setFileFlags(path, 0) // reports the platform honestly
	}
	flags, ok := fileFlags(path)
	if !ok {
		return fmt.Errorf("utmvm: stat %s", path)
	}
	if set {
		flags |= uchgFlag
	} else {
		flags &^= uchgFlag
	}
	if err := setFileFlags(path, flags); err != nil {
		return fmt.Errorf("utmvm: setting flags on %s: %w", path, err)
	}
	return nil
}

// ISOSearchDirs are the places worth looking for other names for an ISO: where
// a browser puts a download, and where UTM keeps the bundles that use it.
func ISOSearchDirs() []string {
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

// FoundISO is one installation image found on this machine.
type FoundISO struct {
	Path  string
	Bytes int64
	Inode uint64
	Links int
	InUse bool // shares its blocks with a VM bundle or the repo's cache
}

// ScanISOs finds every large ISO in the usual places and says which are
// actually used.
//
// It exists because these are 5 GB each, they are produced by a GUI tool that
// names them after a Windows build rather than anything meaningful, and a
// second one is invisible until the disk fills. Asking "which of these is the
// one that works?" from filenames alone is not answerable — but "does it share
// blocks with a VM bundle" is, and that is what InUse reports.
//
// minBytes filters out the small ISOs that are not Windows media: the answer
// file this repo generates is 32 MB and the guest tools are 121 MB.
func ScanISOs(extraDirs []string, minBytes int64) []FoundISO {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs := []string{
		filepath.Join(home, "Downloads"),
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Desktop"),
	}
	dirs = append(dirs, extraDirs...)

	// Inodes reachable from a VM bundle, which is what "in use" means. Anything
	// UTM boots is here, whatever it is called elsewhere.
	used := map[uint64]bool{}
	if vmDir, dErr := DefaultVMDir(); dErr == nil {
		_ = filepath.WalkDir(vmDir, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
				return nil //nolint:nilerr
			}
			if ino, _, ok := inodeInfo(p); ok {
				used[ino] = true
			}
			return nil
		})
	}

	seen := map[uint64]bool{}
	var out []FoundISO
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
				return nil //nolint:nilerr
			}
			if !strings.EqualFold(filepath.Ext(p), ".iso") {
				return nil
			}
			info, iErr := d.Info()
			if iErr != nil || info.Size() < minBytes {
				return nil
			}
			ino, nlink, ok := inodeInfo(p)
			if !ok || seen[ino] {
				return nil
			}
			seen[ino] = true
			out = append(out, FoundISO{
				Path:  p,
				Bytes: info.Size(),
				Inode: ino,
				Links: int(nlink),
				InUse: used[ino],
			})
			return nil
		})
	}
	return out
}
