package utmvm

// Starting a VM with a display, and what to do when UTM does not answer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errUTMNotAnswering is UTM taking a start request and never replying. The
// process is there and `utmctl list` and `status` answer at once; `start` and
// `ip-address` time out. A UTM gets that way from a request that had to
// launch it, or that reached it as it launched (docs/TRAPS.md). utmCommand
// sends neither; this is for a UTM something else did it to.
var errUTMNotAnswering = errors.New("UTM is not answering start requests (AppleEvent timed out, -1712)")

// appleEventTimeout is how osascript reports errAETimeout, after waiting the
// AppleEvent default of two minutes. Matched by number: the words around it
// are localized.
const appleEventTimeout = "(-1712)"

// utmRestartCommand is what restarts UTM by hand, for the messages that
// decline to do it. The second sleep is utmSettle: whatever is typed next
// must not reach UTM as it launches.
const utmRestartCommand = `osascript -e 'quit app "UTM"' && sleep 2 && open -g -a UTM && sleep 2`

// utmStarter is what starting a VM needs from UTM, as functions so the tests
// can run a start against a UTM that does not answer without touching the
// real one.
type utmStarter struct {
	start   func(name string) (string, error) // has UTM start the VM of that name; what osascript printed
	list    func() ([]Entry, error)
	lock    func() (func(), error) // UTMLaunchLock, kept until the VM has started
	restart func(vm string, say func(string, ...any)) error
}

// starter is the real one.
var starter = utmStarter{
	start:   startScript,
	list:    List,
	lock:    holdUTMLaunchLock,
	restart: func(vm string, say func(string, ...any)) error { return utm.restart(vm, say) },
}

// StartWithDisplay powers on the VM through UTM itself so a display window
// opens.
//
// Discovered the hard way: identical VMs booted with utmctl start ignored every
// keystroke, while the same VM started from the app accepted them. utmctl
// starts the machine headless, and without a display there is nowhere for input
// to go. Since UTM's aarch64 firmware always drops to the interactive UEFI
// shell, a Windows VM is unusable without this.
//
// UTM is opened first if it is not running, and left to finish launching
// (utmCommand): the start request must not be what launches it. A UTM that
// still does not answer the request is restarted, once, when no VM is running,
// and the start is tried again (recoverUTM).
func (v VM) StartWithDisplay(say func(string, ...any)) error {
	return starter.startWithDisplay(v.Ref, say)
}

func (u utmStarter) startWithDisplay(ref string, say func(string, ...any)) error {
	name := ref
	start := func() (string, error) {
		out, err := u.start(name)
		if err != nil && !strings.Contains(out, appleEventTimeout) {
			// Fall back from the UUID to the name; AppleScript matches on name only.
			if e, ok := entryFor(u.list, name); ok && !strings.EqualFold(e.Name, name) {
				name = e.Name
				return u.start(name)
			}
		}
		return out, err
	}
	out, err := start()
	if err != nil && strings.Contains(out, appleEventTimeout) {
		say("UTM did not answer the request to start %s: %s", name, firstLine(out))
		release, rErr := u.recoverUTM(name, say)
		if rErr != nil {
			return rErr
		}
		defer release()
		say("starting %s again", name)
		if out, err = start(); err != nil && strings.Contains(out, appleEventTimeout) {
			return fmt.Errorf("starting %s with a display: %w, again, after UTM was restarted for it; "+
				"not restarting it a second time: %s", name, errUTMNotAnswering, out)
		}
	}
	if err != nil {
		return fmt.Errorf("starting %s with a display: %w: %s", name, err, out)
	}
	return nil
}

// recoverUTM restarts a UTM that does not answer start requests, if that is
// safe, and returns what releases UTMLaunchLock once the caller has started
// its VM.
//
// Restarting UTM stops every VM it runs, so it is done only when UTM lists
// the VM being started and lists every VM as stopped. A list that fails or
// lacks that VM is "cannot tell", and refuses like a running VM does.
//
// The lock is held from that check until the caller's start returns: a second
// command recovering at the same moment would see no VM running in the
// seconds between this one's restart and its start, and quit UTM under it.
// It is the lock every request to UTM waits for (ensureOpen), so nothing from
// another command reaches UTM while it is quit and reopened either.
func (u utmStarter) recoverUTM(vm string, say func(string, ...any)) (release func(), err error) {
	unlock, err := u.lock()
	if err != nil {
		return nil, fmt.Errorf("%w, and another command is restarting it; run this again when that has finished: %w",
			errUTMNotAnswering, err)
	}
	defer func() {
		if err != nil {
			unlock()
		}
	}()

	entries, err := u.list()
	if err != nil {
		return nil, notRestarting(fmt.Sprintf("its VMs could not be listed (%v), so whether one is running is not known", err))
	}
	var found bool
	var up []string
	for _, e := range entries {
		if strings.EqualFold(e.Name, vm) {
			found = true
		}
		// Anything but stopped: started, starting, paused. The same test the
		// capacity model counts a running VM by.
		if vmUsesMemory(e.Status) {
			up = append(up, fmt.Sprintf("%s (%s)", e.Name, e.Status))
		}
	}
	if len(up) > 0 {
		return nil, notRestarting("these are not stopped: " + strings.Join(up, ", "))
	}
	if !found {
		return nil, notRestarting(fmt.Sprintf("its list of %d VMs does not have %s in it, so the list cannot be trusted to say none is running",
			len(entries), vm))
	}

	say("UTM lists %d VMs and every one is stopped, so restarting UTM (quit, then open)", len(entries))
	if err := u.restart(vm, say); err != nil {
		return nil, fmt.Errorf("%w, and restarting it failed: %w.\n"+
			"  Restart it by hand and run this again:\n    %s", errUTMNotAnswering, err, utmRestartCommand)
	}
	return unlock, nil
}

// notRestarting is the refusal to restart UTM, with the reason and the command
// that does it by hand.
func notRestarting(why string) error {
	return fmt.Errorf("%w.\n"+
		"  Restarting UTM fixes that and stops every VM it is running, so it was not done: %s.\n"+
		"  If no VM is running, or those that are can be stopped, restart UTM and run this again:\n    %s",
		errUTMNotAnswering, why, utmRestartCommand)
}

// entryFor is Find over a given list function. Any failure is "not found":
// its caller only wants another name to try.
func entryFor(list func() ([]Entry, error), ref string) (Entry, bool) {
	entries, err := list()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, ref) || strings.EqualFold(e.UUID, ref) {
			return e, true
		}
	}
	return Entry{}, false
}

// startScript has UTM start the VM of that name and bring its window up. Not
// bounded here: osascript gives up by itself after two minutes, with -1712.
func startScript(name string) (string, error) {
	script := fmt.Sprintf(`tell application "UTM"
  activate
  start virtual machine named %q
end tell`, name)
	out, err := utmCommand(context.Background(), "osascript", "-e", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
