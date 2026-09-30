//go:build darwin

package utmvm

// The mutation locks on macOS, built on flock.
//
// flock, not an O_EXCL lockfile, because flock dies with its holder: a
// detached job that is killed leaves nothing behind to be detected as stale. A
// stale lockfile would block every future mutation forever, which is the worse
// failure, and detecting staleness needs a PID plus liveness — the recycled-pid
// problem this project already accepts in job.alive but has no business
// accepting on a lock that would deadlock the whole tool.
//
// The files are never deleted, not even the per-VM ones when the VM goes.
// Unlinking a flock file while another process has it open lets a third open a
// new file of the same name and lock that, and then two holders both believe
// they are alone. An empty file per VM name ever used is the price.

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Acquire takes every lock given, or none of them, and refuses rather than
// waits.
//
// The returned release must be called by the holder when its work is done.
// The locks are also released when the process exits — a killed detached job
// cannot leave one behind — which is the whole reason flock was chosen.
//
// flock belongs to the open file, not the process, so a process that already
// holds a lock and asks for it again is refused like anybody else. Callers
// that hold a lock pass the work down rather than calling back through here.
func Acquire(locks ...Lock) (release func(), err error) {
	if err := os.MkdirAll(Root(), 0o755); err != nil {
		return nil, fmt.Errorf("mutation lock: creating %s: %w", Root(), err)
	}
	var held []*os.File
	releaseAll := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(held[i].Fd()), syscall.LOCK_UN)
			_ = held[i].Close()
		}
	}
	for _, l := range ordered(locks) {
		f, err := take(l)
		if err != nil {
			releaseAll()
			return nil, err
		}
		held = append(held, f)
	}
	return releaseAll, nil
}

// take is one lock, non-blocking.
func take(l Lock) (*os.File, error) {
	p := filepath.Join(Root(), string(l))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mutation lock: opening %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errno, ok := err.(syscall.Errno); ok && (errno == syscall.EWOULDBLOCK || errno == syscall.EAGAIN) {
			return nil, busy(l)
		}
		// "Cannot tell" is not "safe". A lock whose state cannot be read must
		// refuse, or the guard allows exactly when it should not.
		return nil, fmt.Errorf("mutation lock: cannot determine the state of %s: %w", p, err)
	}
	return f, nil
}
