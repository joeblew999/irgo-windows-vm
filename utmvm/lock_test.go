package utmvm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The mutation lock is the one guard that must work across processes, because
// a detached job is a different process from the server that started it. The
// in-process tests are cheap; the cross-process test is the one that proves the
// thing itself.

func TestMutationLockRefusesWhileHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := AcquireMutation()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	if _, err := AcquireMutation(); !errors.Is(err, ErrMutationInProgress) {
		t.Fatalf("second acquire = %v, want ErrMutationInProgress", err)
	}
}

func TestMutationHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if held, err := MutationHeld(); err != nil || held {
		t.Fatalf("MutationHeld with no holder = %v, %v; want false, nil", held, err)
	}

	release, err := AcquireMutation()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()

	if held, err := MutationHeld(); err != nil || !held {
		t.Fatalf("MutationHeld while held = %v, %v; want true, nil", held, err)
	}
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
	if _, err := AcquireMutation(); err == nil {
		t.Fatal("AcquireMutation = nil, want an error when the lock cannot be opened")
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
	release, err := AcquireMutation()
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
	_, err := AcquireMutation()
	switch {
	case errors.Is(err, ErrMutationInProgress):
		os.Exit(42)
	case err != nil:
		os.Exit(1)
	default:
		os.Exit(0)
	}
}
