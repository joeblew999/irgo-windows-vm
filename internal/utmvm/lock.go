package utmvm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The mutation locks. A command that changes state on disk takes the ones it
// touches, and is refused (never queued) while another holds one:
//
//   - MachineLock: what every VM shares, the media (iso-*) and the golden
//     image (vm-golden-*). vm-create takes it only while it writes or clones
//     the bundle, seconds, so it cannot race vm-golden-delete and does not hold
//     other VMs up for the length of a boot.
//   - VMLock(name): one VM. vm-create, vm-delete, vm-repair, app-create and
//     app-delete on different VMs run side by side, which is what several
//     agents each with their own VM need.
//   - StageLock: bin/, which app-upload writes and app-delete clears.
//
// The primitive that releases on process death differs per platform, hence
// lock_darwin.go and lock_other.go.

// ErrMutationInProgress is the refusal to start work while another mutation
// holds a lock this one needs.
var ErrMutationInProgress = errors.New("another mutation is in progress")

// Lock is one thing a mutation can hold. Its value is the lock file's name
// without the directory.
type Lock string

const (
	// MachineLock keeps the name the single lock had, so a binary from before
	// the split and one from after still exclude each other on the machine-wide
	// work they both do.
	MachineLock Lock = "mutation.lock"

	// StageLock guards bin/, where app-upload stages binaries for app-create.
	StageLock Lock = "mutation-stage.lock"
)

// vmLockPrefix is what every per-VM lock file starts with. "vm-" is part of it
// so that no VM name can make a per-VM lock collide with StageLock: a VM named
// "stage" locks mutation-vm-stage.lock.
const vmLockPrefix = "mutation-vm-"

// plainVMName is a VM name that can be a file name as it is.
var plainVMName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// VMLock is the lock for one VM, by name.
//
// Folded to lower case, because UTM's names are matched case-insensitively
// (Find uses EqualFold): `-vm A1` and `-vm a1` are one VM, and two locks for
// it would let two commands mutate it at once. A name that cannot be a file
// name as it is — a slash, a space, anything outside [a-z0-9._-] — is hashed
// instead of escaped, so no two names can map to one file.
func VMLock(name string) Lock {
	n := strings.ToLower(name)
	if plainVMName.MatchString(n) {
		return Lock(vmLockPrefix + n + ".lock")
	}
	sum := sha256.Sum256([]byte(n))
	return Lock(vmLockPrefix + hex.EncodeToString(sum[:8]) + ".lock")
}

// uuidRef is how utmctl prints a VM's UUID, which every -vm flag also accepts.
var uuidRef = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// VMLockFor is VMLock for whatever a -vm flag was given, name or UUID.
//
// A UUID is resolved to the VM's name first, or `app-create -vm <uuid>` and
// `app-create -vm a1` would take two different locks on the same VM. A UUID
// UTM does not know stays as it is: the command itself is about to say
// "no such VM", and there is nothing to protect.
func VMLockFor(ref string) Lock {
	if uuidRef.MatchString(ref) {
		if e, err := Find(ref); err == nil {
			return VMLock(e.Name)
		}
	}
	return VMLock(ref)
}

// String names the lock for a person: which thing is busy.
func (l Lock) String() string {
	switch {
	case l == MachineLock:
		return "the machine-wide lock (media and golden image)"
	case l == StageLock:
		return "the staged binaries"
	case strings.HasPrefix(string(l), vmLockPrefix):
		return "VM " + strings.TrimSuffix(strings.TrimPrefix(string(l), vmLockPrefix), ".lock")
	}
	return string(l)
}

// busy is the refusal, naming what was busy.
func busy(l Lock) error {
	return fmt.Errorf("%w: %s is held by another command", ErrMutationInProgress, l)
}

// ordered dedupes and sorts, so every caller takes a set of locks in one
// order. They are all taken without blocking, so there is no deadlock to
// prevent; the order only makes which one is reported busy repeatable.
func ordered(locks []Lock) []Lock {
	seen := map[Lock]bool{}
	var out []Lock
	for _, l := range locks {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
