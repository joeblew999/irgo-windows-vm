package utmvm

// VMs that must keep running, and what the keeper (`irgo-winvm keeper`,
// internal/keeper) needs from UTM to keep them so: whether UTM is running,
// asked of macOS; opening it by the two-second rule; listing its VMs only
// while it is already running; and a start that never restarts UTM.

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoRecord is a VM this tool has no record of: irgo-win11, a golden image,
// or anything made before records. Marking one keep-running would write a
// record that claims it.
var ErrNoRecord = errors.New("no record of this VM")

// SetKeepRunning marks name keep-running, or clears the mark, in its record,
// and reads the record back to check. The keeper starts a marked VM whenever
// UTM lists it stopped; vm-reap never removes one.
//
// A golden image or its verification clone is refused: each is stopped on
// purpose, and a running one is a VM nobody sealed. A VM with no record is
// refused (ErrNoRecord) rather than given one. Clearing a mark that is not
// there, or on a VM with no record, is success, so the undo can run twice.
func SetKeepRunning(name string, keep bool) (changed bool, err error) {
	if _, golden := goldenGuest(name); golden && keep {
		return false, fmt.Errorf("%s is a golden image, which stays stopped: it is cloned, never run", name)
	}
	r, ok, err := readRecord(name)
	if err != nil {
		return false, err
	}
	if !ok {
		if !keep {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s (made by hand, or before records existed). "+
			"Only a VM made by vm-create can be marked; its record says whose it is", ErrNoRecord, name)
	}
	if r.KeepRunning == keep {
		return false, nil
	}
	r.KeepRunning = keep
	if err := writeRecord(r); err != nil {
		return false, err
	}
	back, ok, err := readRecord(name)
	switch {
	case err != nil:
		return false, fmt.Errorf("reading back the record of %s: %w", name, err)
	case !ok || back.KeepRunning != keep:
		return false, fmt.Errorf("the record of %s does not say keep_running=%v after it was written (%s)", name, keep, Home(recordPath(name)))
	}
	return true, nil
}

// KeeperLock is held by the running keeper for as long as it runs, so a Mac
// has one: two would both start the same stopped VM.
const KeeperLock Lock = "mutation-keeper.lock"

// UTMInstalled is whether UTM is where this tool looks for it.
func UTMInstalled() bool { return utm.installed() }

// UTMRunning asks macOS, never UTM, whether UTM is running, so asking never
// opens it.
func UTMRunning() (bool, error) { return utm.running() }

// OpenUTM opens UTM if it is not running and leaves it to finish launching
// for two seconds before anything is sent to it (ensureOpen), under the UTM
// launch lock. A running UTM is left as it is.
func OpenUTM() error {
	if !utm.installed() {
		return fmt.Errorf("UTM is not installed at %s", AppPath)
	}
	return utm.ensureOpen()
}

// ErrUTMClosed is a request that was not sent because UTM is not running,
// and sending it would have opened it.
var ErrUTMClosed = errors.New("UTM is not running")

// ListIfOpen is List when UTM is already running, and ErrUTMClosed, with
// nothing sent, when it is not. For a caller that must not be what opens UTM:
// the keeper, which opens it only for a VM that must keep running.
//
// UTM quitting between the check and the request still has the request open
// it, as for every request (utmCommand); the window is the 0.04 s check.
func ListIfOpen() ([]Entry, error) {
	if !utm.installed() {
		return nil, ErrUTMClosed
	}
	up, err := utm.running()
	if err != nil {
		return nil, err
	}
	if !up {
		return nil, ErrUTMClosed
	}
	return List()
}

// StartKeeping starts the VM with a display, as StartWithDisplay does, but
// never restarts UTM: a UTM that does not answer the start is reported, not
// quit. The keeper runs unattended beside VMs it does not own, and quitting
// UTM stops every one of them.
func (v VM) StartKeeping(say func(string, ...any)) error {
	s := starter
	s.restart = nil
	return s.startWithDisplay(v.Ref, say)
}

// VMRunning is whether a VM in this utmctl state runs, and so needs the Mac
// awake: not stopped, and not paused, which holds its memory but runs nothing.
func VMRunning(status string) bool {
	return vmUsesMemory(status) && !strings.EqualFold(status, "paused")
}
