package utmvm

import "errors"

// The one mutation lock, shared by every command that changes state on disk.
//
// iso, vm and app are coupled and their mutations touch the same machine, so
// there is one lock for all of them rather than one per stage: two clients
// cannot mutate anything at once, and the loser is refused rather than queued.
//
// ErrMutationInProgress is the refusal. The lock itself is platform-specific —
// see lock_darwin.go and lock_other.go — because the primitive that releases
// on process death is a different syscall on every platform.

// ErrMutationInProgress is the refusal to start work while another mutation
// holds the lock.
var ErrMutationInProgress = errors.New("another mutation is in progress")
