package utmvm

import (

	//nolint:gosec // the catalog publishes SHA-1; the choice is Microsoft's

	"fmt"
	"os"
	"path/filepath"
)

// Finding every name for an ISO, protecting it, and removing it.

type isoStatus struct {
	Path      string
	Bytes     int64
	Links     int      // how many names this inode has, per the filesystem
	Found     []string // the ones that could be located
	Protected bool
}

// isoLinks reports an ISO's size, protection, and every other path that shares
// its blocks.
//
// searchIn bounds the hunt for sibling names. There is no reverse index from
// inode to paths on macOS, so they have to be looked for; Links is the count
// the filesystem reports and is authoritative, while Found is only what was
// looked for and located. Links > len(Found) means a name exists somewhere not
// searched — a Time Machine local snapshot, usually — and is not cause for
// alarm.
func isoLinks(path string, searchIn []string) (isoStatus, error) {
	st := isoStatus{Path: path}
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

// isoProtect makes the ISO immutable: it cannot be written, truncated, renamed
// or deleted until ISOUnprotect clears the flag. Idempotent.
func isoProtect(path string) error { return isoChflags(path, true) }

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

// isoCheckWritable refuses to clobber media that something else is using.
//
// Two refusals, both learned the hard way: an immutable file was protected on
// purpose, and a file with more than one name shares its blocks — writing to it
// empties every other name too, including a VM's install.iso.
//
// It lives with the ISO code because it is an ISO question. Routing it through
// a shared Paths helper meant the ISO code had to be handed a struct just to
// answer "may I write here".
func isoCheckWritable(dest string) error {
	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}
	if _, sErr := os.Stat(abs); sErr != nil {
		return nil // does not exist yet, which is the normal case
	}
	if flags, ok := fileFlags(abs); ok && flags&uchgFlag != 0 {
		return fmt.Errorf("utmvm: %s is immutable — it was protected on purpose\n"+
			"  Clear it by hand if you really mean to replace it: chflags nouchg %s",
			Home(abs), Home(abs))
	}
	if _, nlink, ok := inodeInfo(abs); ok && nlink > 1 {
		return fmt.Errorf("utmvm: %s has %d names — it is ONE file shared with %d other place(s),\n"+
			"  and writing here empties all of them", Home(abs), nlink, nlink-1)
	}
	return nil
}
