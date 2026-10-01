//go:build darwin

package utmvm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The mutation locks are the one guard that must work across processes,
// because a detached job is a different process from the server that started
// it. The in-process tests are cheap; the cross-process test is the one that
// proves the thing itself.

func TestMutationLockRefusesWhileHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := Acquire(MachineLock)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	_, err = Acquire(MachineLock)
	if !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("second acquire = %v, want ErrMutationInProgress", err)
	}
	// The refusal says what was busy. "Another mutation is in progress" with
	// several VMs on the machine does not tell an agent whether to wait for
	// its own VM or for somebody else's golden image.
	if !strings.Contains(err.Error(), "machine") {
		t.Errorf("refusal %q does not name the lock that was busy", err)
	}
}

// TestTwoVMsDoNotShareALock is the reason the lock was split: an app-create on
// one agent's VM must not refuse an app-create on another's.
//
// Negative control, run by hand: make VMLock return one constant and the
// second acquire is refused.
func TestTwoVMsDoNotShareALock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ra, err := Acquire(VMLock("a1"))
	if err != nil {
		t.Fatal(err)
	}
	defer ra()
	rb, err := Acquire(VMLock("a2"))
	if err != nil {
		t.Fatalf("a second VM was refused while the first was held: %v", err)
	}
	defer rb()

	if _, err := Acquire(VMLock("A1")); !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("the same VM, spelt in another case, was not refused: %v", err)
	}
	// Nor does a VM's lock stand in for the machine's: vm-golden-delete and a
	// VM that happens to be called "mutation" must not meet.
	rm, err := Acquire(MachineLock)
	if err != nil {
		t.Fatalf("the machine lock was refused while only VM locks were held: %v", err)
	}
	rm()
}

// TestVMLockNamesCannotCollide: two different names must never share a file,
// and no name can reach the machine or stage lock.
func TestVMLockNamesCannotCollide(t *testing.T) {
	names := []string{"a/b", "a_b", "a b", "stage", "mutation", "irgo-win11", "IRGO-WIN11", "../x", ""}
	seen := map[Lock]string{}
	for _, n := range names {
		l := VMLock(n)
		if strings.ContainsAny(string(l), `/\ `) {
			t.Errorf("VMLock(%q) = %q, which is not a plain file name", n, l)
		}
		if l == MachineLock || l == CapacityLock || l == StageLockFor(n) {
			t.Errorf("VMLock(%q) = %q, which is not a VM's lock", n, l)
		}
		if prev, ok := seen[l]; ok && !strings.EqualFold(prev, n) {
			t.Errorf("VMLock(%q) and VMLock(%q) are both %q", prev, n, l)
		}
		seen[l] = n
	}
	if VMLock("irgo-win11") != VMLock("IRGO-WIN11") {
		t.Error("UTM matches names without case, so the lock must too")
	}
}

// TestAcquireIsAllOrNothing: refused on the second lock, the first must not be
// left held, or a refused command would block everything after it until it
// exits.
//
// Negative control, run by hand: drop releaseAll() from the failure path in
// Acquire and the last acquire here is refused.
func TestAcquireIsAllOrNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rv, err := Acquire(VMLock("busy"))
	if err != nil {
		t.Fatal(err)
	}
	defer rv()

	// The stage lock sorts before any VM's, so it is taken first and the busy
	// VM refuses second — which is the case that can leak. With the order the
	// other way round the free lock is never taken and the test proves nothing,
	// as it did the first time it was written (with the machine lock, which
	// sorts after "mutation-vm-").
	if StageLockFor("x") >= VMLock("busy") {
		t.Fatalf("test assumes %q is taken before %q", StageLockFor("x"), VMLock("busy"))
	}
	if _, err := Acquire(VMLock("busy"), StageLockFor("x")); !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("acquire with one busy lock = %v, want ErrMutationInProgress", err)
	}
	rs, err := Acquire(StageLockFor("x"))
	if err != nil {
		t.Fatalf("the stage lock was left held by a refused acquire: %v", err)
	}
	rs()
}

// TestMutationLockRefusesWhenItCannotOpen refuses, rather than guessing "free",
// when the lock file cannot even be created. A guard that allows when it cannot
// tell is the failure this whole file exists to prevent.
func TestMutationLockRefusesWhenItCannotOpen(t *testing.T) {
	// Root()'s parent is a regular file, so MkdirAll cannot make the root and
	// the lock cannot be opened.
	blocker := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blocker)
	if _, err := Acquire(VMLock("a1")); err == nil {
		t.Fatal("Acquire = nil, want an error when the lock cannot be opened")
	}
}

// TestMutationLockIsCrossProcess is the assertion that matters: a lock that
// only refused two goroutines in one process would let a detached job and the
// server mutate at once. The child re-runs this test binary and tries to take
// the lock; while the parent holds it, that must fail as busy.
//
// Negative control, the repo's standing rule: break the flock — take
// LOCK_EX|LOCK_NB out and just return success — and the child exits 0 instead
// of 42, failing this test.
func TestMutationLockIsCrossProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := Acquire(VMLock("cross"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if code := runLockHelper(t); code != 42 {
		t.Fatalf("helper exit = %d, want 42 (busy) while the lock is held", code)
	}

	release()

	if code := runLockHelper(t); code != 0 {
		t.Fatalf("helper exit = %d, want 0 after release", code)
	}
}

func runLockHelper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestAcquireLockHelper")
	cmd.Env = append(os.Environ(), "GO_WANT_ACQUIRE_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	t.Fatalf("running helper: %v (%s)", err, out)
	return -1
}

// TestAcquireLockHelper is a helper process, not a test. It is run by
// TestMutationLockIsCrossProcess with GO_WANT_ACQUIRE_HELPER=1 and exits 42 for
// busy, 1 for any other failure, 0 for acquired.
func TestAcquireLockHelper(t *testing.T) {
	if os.Getenv("GO_WANT_ACQUIRE_HELPER") != "1" {
		t.Skip("helper process")
	}
	_, err := Acquire(VMLock("cross"))
	switch {
	case errors.Is(err, ErrMutationInProgress):
		os.Exit(42)
	case err != nil:
		os.Exit(1)
	default:
		os.Exit(0)
	}
}
