package utmvm

// The filesystem facts this package needs that Go does not expose portably:
// which files share blocks, how much space they really occupy, and whether one
// has been marked immutable.
//
// All three are used for the same purpose — not destroying 5 GB of media that
// took a rate-limited download to obtain — and all three are macOS-specific.
// The counterpart in sysfile_other.go answers honestly rather than guessing, so
// the package compiles for a Windows or Linux developer who only wants
// `targets` to tell them what their machine can do.

import "syscall"

// uchgFlag is UF_IMMUTABLE: the file may not be changed, renamed or deleted
// until the flag is cleared. Truncation is refused too, which is the case that
// matters — a hardlinked ISO can be emptied through any of its names.
const uchgFlag = 0x00000002

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

// fileFlags reports the BSD file flags, of which only UF_IMMUTABLE is used.
func fileFlags(path string) (uint32, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false
	}
	return st.Flags, true
}

func setFileFlags(path string, flags uint32) error {
	return syscall.Chflags(path, int(flags))
}

// statfsAvailable is the space this user can still write on the filesystem
// holding path.
func statfsAvailable(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // filesystem counters
}

// sameDevice reports whether two paths are on one filesystem, which decides
// whether a 5 GB ISO can be hardlinked in for free or must be copied.
func sameDevice(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

const immutableSupported = true
