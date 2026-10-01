//go:build !unix

package ledger

// Elsewhere there is no flock, as for the mutation locks (utmvm/lock_other.go):
// the tool's VM commands are macOS-only, so concurrent writers to one spool
// are not expected. Without the lock, an event appended during a flush's
// rename could be lost; nothing else changes.

func lock(string) (func(), error) { return func() {}, nil }

func tryLock(string) (func(), bool) { return func() {}, true }
