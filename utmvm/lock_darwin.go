//go:build darwin

package utmvm

// The mutation lock on macOS, built on flock.
//
// flock, not an O_EXCL lockfile, because flock dies with its holder: a
// detached job that is killed leaves nothing behind to be detected as stale. A
// stale lockfile would block every future mutation forever, which is the worse
// failure, and detecting staleness needs a PID plus liveness — the recycled-pid
// problem this project already accepts in job.alive but has no business
// accepting on a lock that would deadlock the whole tool.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// AcquireMutation takes the lock or refuses.
//
// The returned release must be called by the holder when its work is done. The
// lock is also released when the process exits — a killed detached job cannot
// leave it behind — which is the whole reason flock was chosen.
func AcquireMutation() (release func(), err error) {
	if err := os.MkdirAll(Root(), 0o755); err != nil {
		return nil, fmt.Errorf("mutation lock: creating %s: %w", Root(), err)
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mutation lock: opening %s: %w", lockPath(), err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errno, ok := err.(syscall.Errno); ok && (errno == syscall.EWOULDBLOCK || errno == syscall.EAGAIN) {
			return nil, ErrMutationInProgress
		}
		// "Cannot tell" is not "safe". A lock whose state cannot be read must
		// refuse, or the guard allows exactly when it should not.
		return nil, fmt.Errorf("mutation lock: cannot determine lock state: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// MutationHeld reports whether another mutation holds the lock now.
//
// It is the prompt refusal for a detached job: the caller asks before forking,
// and the job child takes the lock itself, which is the source of truth. A
// caller that skips the probe still gets refused, just later, in the child's
// log rather than at call time.
func MutationHeld() (bool, error) {
	release, err := AcquireMutation()
	if err == nil {
		release()
		return false, nil
	}
	if errors.Is(err, ErrMutationInProgress) {
		return true, nil
	}
	return false, err
}
