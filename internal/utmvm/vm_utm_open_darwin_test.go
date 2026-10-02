//go:build darwin

package utmvm

import (
	"errors"
	"testing"
	"time"
)

// TestASecondCommandWaitsWhileUTMIsBeingOpened: two commands racing to a
// closed UTM. The first opens it and waits; the second, arriving during that
// wait, would find UTM running and send its request into the launch. With the
// real lock it cannot get as far as asking: it waits for the lock, and here,
// given 50 ms, is refused as busy.
//
// Negative control, run by hand: release the lock before the wait in
// ensureOpen (`release()` before `return a.openAndSettle`, in place of the
// defer) and the second command asks whether UTM is running and goes ahead.
func TestASecondCommandWaitsWhileUTMIsBeingOpened(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := &fakeApp{up: []bool{false, true}}
	second := &fakeApp{up: []bool{true}}
	var secondErr error
	first.duringWait = func() {
		b := second.app()
		b.lock = func() (func(), error) { return acquireWithin(50*time.Millisecond, UTMLaunchLock) }
		secondErr = b.ensureOpen()
	}
	a := first.app()
	a.lock = func() (func(), error) { return Acquire(UTMLaunchLock) }
	if err := a.ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if got, want := first.did(), "running?, open, wait 2s, running?"; got != want {
		t.Fatalf("the first command did:  %s\nwant: %s", got, want)
	}
	if !errors.Is(secondErr, ErrMutationInProgress) {
		t.Errorf("the second command was not held off while UTM was being opened: %v", secondErr)
	}
	if got := second.did(); got != "" {
		t.Errorf("the second command did %q during the first's wait", got)
	}

	// And once the first has finished, the lock is free again.
	b := second.app()
	b.lock = func() (func(), error) { return acquireWithin(50*time.Millisecond, UTMLaunchLock) }
	if err := b.ensureOpen(); err != nil {
		t.Errorf("after the first command finished, the second was still refused: %v", err)
	}
}

// TestARestartsOwnRequestsPassItsLock: the command restarting UTM holds the
// lock until its VM has started, and sends UTM requests of its own meanwhile
// (the quit, the list, the start). flock would refuse them, so they pass
// without taking it; another command's do not.
//
// Negative control, run by hand: drop the utmLaunchLockHeld test from
// passUTMLaunchLock and the holder's own request is refused, after
// utmLaunchLockWait.
func TestARestartsOwnRequestsPassItsLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := holdUTMLaunchLock()
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	pass, err := passUTMLaunchLock()
	if err != nil {
		t.Fatalf("the holder's own request was refused: %v", err)
	}
	pass()
	if d := time.Since(began); d > time.Second {
		t.Errorf("the holder's own request waited %s", d)
	}
	if _, err := Acquire(UTMLaunchLock); !errors.Is(err, ErrMutationInProgress) {
		t.Errorf("the lock is not held against others during a restart: %v", err)
	}
	release()
	free, err := Acquire(UTMLaunchLock)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	free()
	if utmLaunchLockHeld.Load() {
		t.Error("the process still passes its own requests after releasing the lock")
	}
}
