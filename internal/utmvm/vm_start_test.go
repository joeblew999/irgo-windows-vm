package utmvm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// What osascript printed on 2 Oct 2026 when UTM was up and did not answer a
// start, and what it prints for a name UTM does not know.
const (
	sayTimedOut = `36:81: execution error: UTM got an error: AppleEvent timed out. (-1712)`
	sayNoSuchVM = `36:81: execution error: UTM got an error: Can’t get virtual machine named "x". (-1728)`
)

// fakeUTM is a UTM for utmStarter: starts answers each start request in turn
// ("" is a start that worked, anything else is what osascript printed as it
// failed), and events records every call in order.
type fakeUTM struct {
	starts     []string
	entries    []Entry
	listErr    error
	lockErr    error
	restartErr error
	events     []string
	said       []string
}

func (f *fakeUTM) starter() utmStarter {
	return utmStarter{
		start: func(name string) (string, error) {
			f.events = append(f.events, "start "+name)
			if len(f.starts) == 0 {
				return "", errors.New("the test did not expect another start")
			}
			out := f.starts[0]
			f.starts = f.starts[1:]
			if out != "" {
				return out, errors.New("exit status 1")
			}
			return "", nil
		},
		list: func() ([]Entry, error) { return f.entries, f.listErr },
		lock: func() (func(), error) {
			if f.lockErr != nil {
				return nil, f.lockErr
			}
			f.events = append(f.events, "lock")
			return func() { f.events = append(f.events, "unlock") }, nil
		},
		restart: func(vm string, _ func(string, ...any)) error {
			f.events = append(f.events, "restart for "+vm)
			return f.restartErr
		},
	}
}

func (f *fakeUTM) say(format string, a ...any) { f.said = append(f.said, fmt.Sprintf(format, a...)) }

func (f *fakeUTM) did() string { return strings.Join(f.events, ", ") }

func stopped(names ...string) []Entry {
	var es []Entry
	for i, n := range names {
		es = append(es, Entry{UUID: fmt.Sprintf("0000000%d-0000-4000-8000-000000000000", i), Status: "stopped", Name: n})
	}
	return es
}

// TestStartRestartsAUTMThatDoesNotAnswer: the defect of 2 Oct 2026. UTM was
// up, took the start request and never replied; vm-create waited two minutes
// and failed with osascript's text, when quitting and reopening UTM was all it
// needed. With every VM stopped the tool does that itself, once, says so, and
// holds the restart lock until its VM has started.
//
// Proven here against a fake only. No run of this has been made against a
// real UTM in that state.
//
// Negative control, run by hand: make startWithDisplay skip the recovery
// (`if false && err != nil && strings.Contains(...)`) and this fails with the
// AppleEvent text. Releasing the lock before the second start (`release()` in
// place of `defer release()`) fails the order check.
func TestStartRestartsAUTMThatDoesNotAnswer(t *testing.T) {
	f := &fakeUTM{starts: []string{sayTimedOut, ""}, entries: stopped("irgo-golden", "a1")}
	if err := f.starter().startWithDisplay("a1", f.say); err != nil {
		t.Fatalf("a UTM that did not answer, with no VM running, was not recovered: %v", err)
	}
	if got, want := f.did(), "start a1, lock, restart for a1, start a1, unlock"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	said := strings.Join(f.said, "\n")
	for _, want := range []string{"did not answer", "(-1712)", "every one is stopped", "restarting UTM", "starting a1 again"} {
		if !strings.Contains(said, want) {
			t.Errorf("the progress output does not say %q:\n%s", want, said)
		}
	}
}

// TestStartNeverRestartsUTMUnlessEveryVMIsStopped: restarting UTM stops every
// VM it runs, so the guard has three answers and only "every VM is stopped"
// restarts. A VM in any other state refuses, naming it; a list that fails, is
// empty or lacks the VM being started is "cannot tell" and refuses too. Each
// refusal names the cause and the command that restarts UTM by hand, and
// leaves the lock free.
//
// Negative controls, run by hand: drop the `len(up) > 0` refusal in
// recoverUTM and the four states restart UTM; drop the `!found` one and the
// two lists without the VM do. Dropping the `err != nil` one after u.list()
// does not restart UTM, because a failed list has no VM in it and `!found`
// refuses; it fails the failed-list case on the reason given.
func TestStartNeverRestartsUTMUnlessEveryVMIsStopped(t *testing.T) {
	with := func(status string) []Entry {
		es := stopped("a1", "other vm")
		es[1].Status = status
		return es
	}
	for name, tc := range map[string]struct {
		entries []Entry
		listErr error
		want    string
	}{
		"another VM started":   {entries: with("started"), want: "other vm (started)"},
		"another VM paused":    {entries: with("paused"), want: "other vm (paused)"},
		"another VM starting":  {entries: with("starting"), want: "other vm (starting)"},
		"a state nobody knows": {entries: with("???"), want: "other vm (???)"},
		"the list fails":       {listErr: errors.New("utmctl list: exit status 1"), want: "could not be listed (utmctl list: exit status 1)"},
		"the list is empty":    {want: "its list of 0 VMs does not have a1 in it"},
		"the list lacks it":    {entries: stopped("b2"), want: "its list of 1 VMs does not have a1 in it"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeUTM{starts: []string{sayTimedOut, ""}, entries: tc.entries, listErr: tc.listErr}
			err := f.starter().startWithDisplay("a1", f.say)
			if got, want := f.did(), "start a1, lock, unlock"; got != want {
				t.Fatalf("did:  %s\nwant: %s", got, want)
			}
			if !errors.Is(err, errUTMNotAnswering) {
				t.Fatalf("the error is not errUTMNotAnswering: %v", err)
			}
			for _, want := range []string{"UTM is not answering", tc.want, utmRestartCommand} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q:\n%v", want, err)
				}
			}
		})
	}
}

// TestStartRestartsUTMOnlyOnce: a UTM that still does not answer after the
// restart is reported, not restarted again.
//
// Negative control, run by hand: loop the recovery in startWithDisplay (`for`
// in place of `if`, `continue` in place of the return after the second start)
// and the fake is restarted twice.
func TestStartRestartsUTMOnlyOnce(t *testing.T) {
	f := &fakeUTM{starts: []string{sayTimedOut, sayTimedOut, ""}, entries: stopped("a1")}
	err := f.starter().startWithDisplay("a1", f.say)
	if got, want := f.did(), "start a1, lock, restart for a1, start a1, unlock"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	if !errors.Is(err, errUTMNotAnswering) || !strings.Contains(err.Error(), "after UTM was restarted") {
		t.Errorf("the error does not say UTM was restarted and still did not answer: %v", err)
	}
}

// TestStartRefusesWhileAnotherCommandRestartsUTM: a second command recovering
// at the same moment is refused as busy (exit 6, worth retrying) before it
// looks at anything, because the first has quit UTM or is about to start its
// VM in it.
//
// Negative control, run by hand: ignore u.lock()'s error in recoverUTM and
// this one goes on to the restart, then panics releasing a lock it never had.
func TestStartRefusesWhileAnotherCommandRestartsUTM(t *testing.T) {
	f := &fakeUTM{starts: []string{sayTimedOut, ""}, entries: stopped("a1"), lockErr: busy(UTMLaunchLock)}
	err := f.starter().startWithDisplay("a1", f.say)
	if got, want := f.did(), "start a1"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	if !errors.Is(err, ErrMutationInProgress) || !errors.Is(err, errUTMNotAnswering) {
		t.Errorf("the error is not both busy and UTM not answering: %v", err)
	}
}

// TestStartSaysWhenTheRestartFailed: a restart that did not bring UTM back
// ends the command with the reason and the command to run, and the VM is not
// started in whatever is left.
//
// Negative control, run by hand: ignore u.restart's error in recoverUTM and
// the start is sent again.
func TestStartSaysWhenTheRestartFailed(t *testing.T) {
	f := &fakeUTM{starts: []string{sayTimedOut, ""}, entries: stopped("a1"),
		restartErr: errors.New("UTM was asked to quit and is still running after 30s")}
	err := f.starter().startWithDisplay("a1", f.say)
	if got, want := f.did(), "start a1, lock, restart for a1, unlock"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	if err == nil {
		t.Fatal("a failed restart was reported as a started VM")
	}
	for _, want := range []string{"UTM is not answering", "still running after 30s", utmRestartCommand} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
}

// TestStartLeavesEveryOtherFailureAlone: only the AppleEvent timeout is a UTM
// that does not answer. Any other failure is returned as it was, with no lock
// taken and nothing restarted; and a UUID still falls back to the VM's name,
// which is what the recovery then starts.
//
// Negative control, run by hand: recover on any error (drop the
// strings.Contains test before recoverUTM) and the first case restarts UTM.
func TestStartLeavesEveryOtherFailureAlone(t *testing.T) {
	f := &fakeUTM{starts: []string{sayNoSuchVM}, entries: stopped("a1")}
	err := f.starter().startWithDisplay("a1", f.say)
	if got, want := f.did(), "start a1"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	if err == nil || errors.Is(err, errUTMNotAnswering) || !strings.Contains(err.Error(), "starting a1 with a display: exit status 1: "+sayNoSuchVM) {
		t.Errorf("another failure was not returned as it was: %v", err)
	}

	es := stopped("a1")
	uuid := es[0].UUID
	f = &fakeUTM{starts: []string{sayNoSuchVM, sayTimedOut, ""}, entries: es}
	if err := f.starter().startWithDisplay(uuid, f.say); err != nil {
		t.Fatalf("a start by UUID was not recovered under the VM's name: %v", err)
	}
	if got, want := f.did(), "start "+uuid+", start a1, lock, restart for a1, start a1, unlock"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
}
