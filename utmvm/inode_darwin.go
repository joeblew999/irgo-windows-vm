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

// diskUsage returns bytes actually occupied, which for a sparse file is far
// less than its length. A 64 GiB VM disk holding a 12 GiB install reports 64
// GiB from Stat.Size, so using that overstates what deleting it frees — by
// five times, in the case that prompted this.
func diskUsage(path string) (int64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false
	}
	return int64(st.Blocks) * 512, true
}
