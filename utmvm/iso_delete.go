package utmvm

import (

	//nolint:gosec // the catalog publishes SHA-1; the choice is Microsoft's

	"fmt"
	"os"
	"path/filepath"
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

// ISOProtect makes the ISO immutable: it cannot be written, truncated, renamed
// or deleted until ISOUnprotect clears the flag. Idempotent.
func ISOProtect(path string) error { return isoChflags(path, true) }

// ISOUnprotect clears the immutable flag, so the ISO can be replaced or the VM
// holding a hardlink to it can be deleted. Idempotent.
//
// `irgo-winvm vm-delete` needs this: rm refuses an immutable file, and the VM
// bundle's install.iso is the same inode as the protected one.
func ISOUnprotect(path string) error { return isoChflags(path, false) }

func isoChflags(path string, set bool) error {
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
