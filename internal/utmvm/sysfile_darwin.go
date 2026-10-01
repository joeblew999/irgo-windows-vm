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

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

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

// cloneFile makes dst an APFS clone of src: a new inode sharing src's blocks
// until either is written. Fails across volumes and on filesystems without
// clones; the caller copies then.
func cloneFile(src, dst string) error { return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) }

// hostMemory is the Mac's physical memory, hw.memsize.
func hostMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }

// processAlive reports whether pid is a running process. Signal 0 checks
// without sending; EPERM is a live process owned by somebody else. A recycled
// pid reads as alive, the same trade job.alive makes.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// apfsUsage is what a file costs on APFS: allocated is every block it uses,
// private the bytes it shares with no other file (ATTR_CMNEXT_PRIVATESIZE),
// and family the clone family it belongs to (ATTR_CMNEXT_CLONEID), equal for
// a file and the clones made from it.
//
// st_blocks counts shared blocks in full, so a clone of the 10 GiB golden
// image reads as 10 GiB the moment it is made, and du over a clone and its
// source counts the same blocks twice. The private size is what deleting the
// file gives back (measured 1 Oct 2026: a clone reading 0.28 GiB private
// freed 0.29 GiB of df when deleted). getattrlist works on a known path in
// UTM's container, where ls does not.
func apfsUsage(path string) (allocated, private int64, family uint64, err error) {
	al := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  attrCmnReturnedAttrs,
		Forkattr:    attrCmnextPrivateSize | attrCmnextCloneID,
	}
	var buf [64]byte
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, 0, 0, err
	}
	//nolint:gosec // getattrlist(2) takes these pointers for the duration of the call
	_, _, errno := syscall.Syscall6(syscall.SYS_GETATTRLIST, uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(unix.FSOPT_ATTR_CMN_EXTENDED|unix.FSOPT_NOFOLLOW), 0)
	if errno != 0 {
		return 0, 0, 0, fmt.Errorf("getattrlist %s: %w", path, errno)
	}
	// u32 length, the returned attribute_set_t (five u32: common, vol, dir,
	// file, fork), then the attributes in bit order: private size (off_t),
	// clone id (u64).
	returned := binary.LittleEndian.Uint32(buf[4+16:])
	if returned&attrCmnextPrivateSize == 0 || returned&attrCmnextCloneID == 0 {
		return 0, 0, 0, fmt.Errorf("%s: the filesystem does not report private size (not APFS?)", path)
	}
	private = int64(binary.LittleEndian.Uint64(buf[24:])) //nolint:gosec // a byte count
	family = binary.LittleEndian.Uint64(buf[32:])
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, 0, 0, err
	}
	return st.Blocks * 512, private, family, nil
}

// From <sys/attr.h>; x/sys/unix does not carry the extended common
// attributes.
const (
	attrCmnReturnedAttrs  = 0x80000000
	attrCmnextPrivateSize = 0x00000008
	attrCmnextCloneID     = 0x00000100
)

// volumeSize is the size of the filesystem holding path, and what is free on
// it for this user, from one statfs.
func volumeSize(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // filesystem counters
}
