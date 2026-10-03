package utmvm

// Reaching UTM: the one way a request gets to it, and opening it first.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// utmSettle is how long UTM is left alone after it is opened, before the
// first request.
//
// A UTM that a request has to launch, or that gets one in the first moments
// of its launch, can answer it and then never answer a VM start until it is
// quit and reopened (docs/reference/traps.md). Measured 2 Oct 2026, UTM 4.7.5, with UTM
// closed: an osascript request left every later start hanging, 4 of 4, and
// so did `capacity`, whose first request is one, 2 of 2. After `open -g -a`,
// osascript and utmctl by its path in UTM.app were answered at 0 s with no
// harm, 7 of 7; utmctl through Homebrew's symlink did the harm at 0 s, 10 of
// 10, and none from 0.3 s, 17 of 17. Which requests are safe when is not
// understood, so every one waits, seven times the shortest wait that worked.
//
// The wait is a fixed time on purpose. UTM cannot be asked whether it has
// finished launching: asking is the request that does the harm.
const utmSettle = 2 * time.Second

// utmLaunchLockWait is how long a request waits for another command that is
// opening UTM (utmSettle and a little) or restarting it, before it is refused
// as busy.
const utmLaunchLockWait = 30 * time.Second

// How long a restart waits for UTM to go, and then to list its VMs again, and
// how often it looks.
const (
	utmQuitWait   = 30 * time.Second
	utmLaunchWait = 60 * time.Second
	utmPoll       = 500 * time.Millisecond
)

// utmApp is what opening, checking and restarting UTM needs, as functions so
// the tests can do it to a UTM that is not there without touching the real
// one.
type utmApp struct {
	installed func() bool
	running   func() (bool, error)    // asks macOS, never UTM
	open      func() error            // in the background; returns before UTM has finished launching
	quit      func() error            // asks UTM to quit
	list      func() ([]Entry, error) // asks UTM
	lock      func() (func(), error)  // UTMLaunchLock, waited for
	sleep     func(time.Duration)
	settle    time.Duration
	say       func(string, ...any)
}

// utm is the real one. Set in init, because its list is itself a request to
// UTM and so comes back through here.
var utm utmApp

func init() {
	utm = utmApp{
		installed: func() bool { _, err := os.Stat(AppPath); return err == nil },
		running:   utmRunning,
		open: func() error {
			if out, err := exec.Command("open", "-g", "-a", AppPath).CombinedOutput(); err != nil {
				return fmt.Errorf("open -g -a %s: %w: %s", AppPath, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		quit: func() error {
			_, err := utmScript(`quit app "UTM"`, utmQuitWait)
			return err
		},
		list:   List,
		lock:   passUTMLaunchLock,
		sleep:  time.Sleep,
		settle: utmSettle,
		say:    progress,
	}
}

// utmCommand is the command for one request to UTM, through utmctl or
// osascript, and the only place either is run for UTM (TestEveryRequestToUTM
// GoesThroughUTMCommand). It makes sure UTM is open and has been left to
// finish launching before the request is sent; if that fails the command
// fails with the reason, wherever its caller runs it.
//
// The time that takes comes out of ctx's: two seconds when UTM had to be
// opened, 0.04 s (measured) when it was running.
func utmCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	err := utm.ensureOpen()
	cmd := exec.CommandContext(ctx, name, args...)
	if err != nil {
		cmd.Err = err
	}
	return cmd
}

// ensureOpen returns once UTM may be sent a request: it is running, or it was
// opened here and left alone for the settle time. A UTM that is not installed
// is passed through; the request says so itself.
//
// UTMLaunchLock is held from the check to the end of the wait. Without it a
// second command would find the UTM the first has just opened running, and
// send its request into the launch. Whether UTM is running is asked of macOS:
// asking UTM would launch it with exactly that request. "Cannot tell" sends
// nothing.
//
// Not covered: a UTM that something else opened a moment ago (its owner, a
// login item) is running, and is not waited for; and whatever else on the Mac
// sends UTM a request, `utmctl` typed at a shell included.
func (a utmApp) ensureOpen() error {
	if !a.installed() {
		return nil
	}
	release, err := a.lock()
	if err != nil {
		return fmt.Errorf("UTM is being opened or restarted by another command, and nothing may be sent to it until that is done: %w", err)
	}
	defer release()
	up, err := a.running()
	if err != nil {
		return fmt.Errorf("whether UTM is running is not known, so nothing was sent to it: %w", err)
	}
	if up {
		return nil
	}
	return a.openAndSettle("UTM is not running", a.say)
}

// openAndSettle opens UTM in the background, sends it nothing for the settle
// time, and checks with macOS that it is running. The caller holds
// UTMLaunchLock. why is what it says first: the reason UTM is being opened.
func (a utmApp) openAndSettle(why string, say func(string, ...any)) error {
	say("%s: opening %s in the background, then waiting %s before asking it anything (a request that has to launch UTM leaves it not answering VM starts)",
		why, AppPath, a.settle)
	if err := a.open(); err != nil {
		return fmt.Errorf("opening UTM: %w", err)
	}
	a.sleep(a.settle)
	up, err := a.running()
	if err != nil {
		return fmt.Errorf("UTM was opened, and whether it is running is not known: %w", err)
	}
	if !up {
		return fmt.Errorf("UTM was opened and is not running %s later", a.settle)
	}
	return nil
}

// restart quits UTM, checks with macOS that it has gone, opens it again as
// openAndSettle does, and returns once it lists vm. Only recoverUTM calls it,
// holding UTMLaunchLock, after checking that no VM is running.
//
// The list is asked for only after the settle time, as every request is. It
// used to be polled from the moment UTM was opened. That was not seen to do
// harm (utmctl by its path, at 0 s: 4 of 4 answered and the start worked),
// and is not relied on.
func (a utmApp) restart(vm string, say func(string, ...any)) error {
	began := time.Now()
	if err := a.quit(); err != nil {
		return fmt.Errorf("asking UTM to quit: %w", err)
	}
	for waited := time.Duration(0); ; waited += utmPoll {
		up, err := a.running()
		if err != nil {
			return fmt.Errorf("UTM was asked to quit, and whether it has is not known: %w", err)
		}
		if !up {
			break
		}
		if waited >= utmQuitWait {
			return fmt.Errorf("UTM was asked to quit and is still running after %s", utmQuitWait)
		}
		a.sleep(utmPoll)
	}
	if err := a.openAndSettle(fmt.Sprintf("UTM has quit (%.1fs)", time.Since(began).Seconds()), say); err != nil {
		return err
	}
	for waited := time.Duration(0); ; waited += utmPoll {
		if _, ok := entryFor(a.list, vm); ok {
			break
		}
		if waited >= utmLaunchWait {
			return fmt.Errorf("UTM was opened again and, %s after the %s it was left alone for, does not list %s",
				utmLaunchWait, a.settle, vm)
		}
		a.sleep(utmPoll)
	}
	say("UTM is back and lists %s (%.1fs)", vm, time.Since(began).Seconds())
	return nil
}

// utmRunning asks macOS, not UTM, whether UTM is running, so it answers when
// UTM does not and never launches it: "Accessing an application's running
// property returns a Boolean value without launching the application or
// sending it an event" (AppleScript Language Guide, the application class),
// and measured 2 Oct 2026, with UTM closed: "false", and no UTM process
// afterwards. UTM's container is named by its bundle id, which is what this
// asks by. Not through utmCommand: it is what utmCommand asks first.
func utmRunning() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), utmctlTimeout)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "osascript", "-e",
		fmt.Sprintf(`application id %q is running`, utmContainer)).CombinedOutput()
	out := strings.TrimSpace(string(raw))
	if ctx.Err() != nil {
		return false, fmt.Errorf("asked macOS whether UTM is running, no answer within %s", utmctlTimeout)
	}
	if err != nil {
		return false, fmt.Errorf("asked macOS whether UTM is running: %w: %s", err, out)
	}
	switch out {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("asked macOS whether UTM is running, osascript said %q", out)
}

// utmLaunchLockHeld is whether this process holds UTMLaunchLock for a restart
// (holdUTMLaunchLock). flock refuses a second open of a file its own process
// has locked, and a restart sends UTM requests of its own, so those pass
// without taking it. Nothing in this package sends UTM requests from two
// goroutines at once; if that changes, a request from the other goroutine
// would pass here too.
var utmLaunchLockHeld atomic.Bool

// holdUTMLaunchLock takes UTMLaunchLock for a restart of UTM and the start
// that follows it, and lets this process's own requests through meanwhile.
func holdUTMLaunchLock() (func(), error) {
	release, err := acquireWithin(utmLaunchLockWait, UTMLaunchLock)
	if err != nil {
		return nil, err
	}
	utmLaunchLockHeld.Store(true)
	return func() {
		utmLaunchLockHeld.Store(false)
		release()
	}, nil
}

// passUTMLaunchLock takes UTMLaunchLock for one request's check, waiting for
// a command that is opening or restarting UTM.
func passUTMLaunchLock() (func(), error) {
	if utmLaunchLockHeld.Load() {
		return func() {}, nil
	}
	return acquireWithin(utmLaunchLockWait, UTMLaunchLock)
}
