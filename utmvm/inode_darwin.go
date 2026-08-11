package utmvm

import "syscall"

// inodeInfo returns the inode number and link count, so hardlinked files shared
// between bundles are only counted once when estimating reclaimable space.
func inodeInfo(path string) (ino uint64, nlink uint64, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, 0, false
	}
	return st.Ino, uint64(st.Nlink), true
}
