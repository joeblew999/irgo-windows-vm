package job

// These use real processes, because the thing being tested is whether a real
// process is alive. A fake would be testing the fake.

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestAliveReportsADeadProcess is the reason this package exists.
//
// A handle that answers "still running" forever because nothing checks is worse
// than no handle: an agent waits on a 45-minute install that died in the first
// minute, and nothing ever says so.
//
// It also pins the mistake that would cause exactly that. os.FindProcess on
// Unix succeeds for any pid, alive or not, so `_, err := os.FindProcess(pid);
// return err == nil` always returns true — a liveness check that cannot report
// death, which passes every test written against a running process.
//
// Negative control, run by hand: replace the Signal(0) call with `return err ==
// nil` and this fails on the dead case while every other test here passes.
func TestAliveReportsADeadProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if !alive(pid) {
		t.Fatalf("pid %d was just started and is reported dead", pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Reaped, or it stays a zombie — and a zombie still answers signal 0, so
	// without this the test would pass for the wrong reason.
	_, _ = cmd.Process.Wait()

	if alive(pid) {
		t.Errorf("pid %d was killed and reaped and is still reported alive", pid)
	}
	if alive(0) || alive(-1) {
		t.Error("a nonsense pid is reported alive")
	}
}

// withTempDir points the package at a scratch directory, so tests never touch
// the real jobs under Application Support.
func withTempDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := Dir(); !filepath.IsAbs(got) || !hasPrefix(got, home) {
		t.Fatalf("Dir() is %q, which is not under the test HOME %q — this test would "+
			"otherwise write to the real jobs directory", got, home)
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// withSleep points Start at /bin/sleep, so a job is a real process that stays
// alive long enough to be asked about. The test binary is not usable here: it
// exits at once on an argument it does not know, and every liveness assertion
// would then pass or fail for a reason unrelated to what is being tested.
func withSleep(t *testing.T) {
	t.Helper()
	prev := executable
	executable = func() (string, error) { return "/bin/sleep", nil }
	t.Cleanup(func() { executable = prev })
}

// TestStartRecordsAJobAndStatusReadsItBack.
func TestStartRecordsAJobAndStatusReadsItBack(t *testing.T) {
	withTempDir(t)
	withSleep(t)

	s, err := Start("30", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.ID == "" || s.PID <= 0 {
		t.Fatalf("Start returned %+v", s)
	}

	got, err := Status(s.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.PID != s.PID || got.Command != s.Command {
		t.Errorf("Status returned %+v, started %+v", got, s)
	}
	if got.Elapsed < 0 {
		t.Errorf("elapsed is negative: %v", got.Elapsed)
	}

	// The record must survive on disk, because the process that wrote it is
	// gone by the time anything asks.
	if _, err := os.Stat(path(s.ID)); err != nil {
		t.Errorf("no record on disk: %v", err)
	}
}

// TestStatusOfAFinishedJobSaysSoRatherThanRunning.
//
// The case that matters most: the record still exists, the process does not.
// Reporting from the file alone would say "running" forever.
func TestStatusOfAFinishedJobSaysSoRatherThanRunning(t *testing.T) {
	withTempDir(t)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}

	// A pid that has certainly exited: start something, kill it, reap it.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_, _ = cmd.Process.Wait()

	rec := State{ID: "dead-one", Command: "vm-create", PID: pid, Started: time.Now().Add(-time.Hour)}
	if err := write(rec); err != nil {
		t.Fatal(err)
	}

	got, err := Status("dead-one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Alive {
		t.Error("a job whose process is gone reports as alive; an agent would wait on it forever")
	}
	if got.Elapsed < 59*time.Minute {
		t.Errorf("elapsed is %v, want about an hour", got.Elapsed)
	}
}

// TestStartingTheSameWorkTwiceReturnsTheFirstJob.
//
// A client that timed out will simply ask again. Without this, the second ask
// starts a second 45-minute install against the same VM.
func TestStartingTheSameWorkTwiceReturnsTheFirstJob(t *testing.T) {
	withTempDir(t)
	withSleep(t)

	first, err := Start("30", []string{"31"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Start("30", []string{"31"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("starting the same work twice made two jobs (%s and %s); one VM, two installs",
			first.ID, second.ID)
	}

	// Different arguments are different work and must start separately.
	other, err := Start("30", []string{"32"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Error("different arguments were treated as the same job")
	}
}

// TestNoJobsIsNotAnError — All is called before anything has ever run, and a
// missing directory is the normal case, not a failure.
func TestNoJobsIsNotAnError(t *testing.T) {
	withTempDir(t)
	got, err := All()
	if err != nil {
		t.Errorf("All() on a machine with no jobs returned %v", err)
	}
	if len(got) != 0 {
		t.Errorf("All() returned %d jobs on a fresh machine", len(got))
	}
}

// TestStatusOfAnUnknownJobFails — asking about a job that does not exist must
// say so, not return an empty state that reads as "not running".
func TestStatusOfAnUnknownJobFails(t *testing.T) {
	withTempDir(t)
	if _, err := Status("never-existed"); err == nil {
		t.Error("Status of an unknown job returned no error; an empty state reads as a finished job")
	}
}

// TestFinishedJobsArePruned — jobs/ grew a JSON and a log per run, forever, and
// a 45-minute install's log is not small. Nothing removed either.
//
// Negative control, run by hand: remove the pruneFinished call from Start and
// this fails with 25 records where 20 were expected.
func TestFinishedJobsArePruned(t *testing.T) {
	withTempDir(t)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}

	// Well past the limit, all finished: pid 1 is init, which this process
	// cannot signal, so alive() reports false without inventing a fake.
	for i := 0; i < keepFinished+5; i++ {
		id := "old-" + string(rune('a'+i))
		if err := write(State{ID: id, Command: "vm-create", PID: 1,
			Started: time.Now().Add(-time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(Dir(), id+".log"), []byte("output"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	withSleep(t)
	if _, err := Start("30", nil); err != nil {
		t.Fatal(err)
	}

	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	finished := 0
	for _, s := range all {
		if !s.Alive {
			finished++
		}
	}
	if finished > keepFinished {
		t.Errorf("%d finished jobs kept, limit is %d", finished, keepFinished)
	}

	// The logs must go with the records, or the pruning frees nothing worth
	// freeing — the log is the big half.
	logs, _ := filepath.Glob(filepath.Join(Dir(), "*.log"))
	if len(logs) > keepFinished+1 {
		t.Errorf("%d logs left for %d kept jobs; the records were pruned and the logs were not",
			len(logs), keepFinished)
	}
	t.Logf("%d finished kept, %d logs, %d bytes", finished, len(logs), Size())
}

// TestRunningJobsAreNeverPruned — forgetting work that is still going is worse
// than any amount of disk.
func TestRunningJobsAreNeverPruned(t *testing.T) {
	withTempDir(t)
	withSleep(t)

	// The live jobs are made to look OLD, which is the whole point. Started
	// normally they are the newest records and would survive pruning whether or
	// not the guard exists — the first version of this test did exactly that
	// and passed against a build with the guard removed.
	var live []string
	for i := 0; i < 3; i++ {
		s, err := Start("30", []string{string(rune('a' + i))})
		if err != nil {
			t.Fatal(err)
		}
		// Same real pid, backdated: still alive, now the oldest thing here.
		s.Started = time.Now().Add(-time.Duration(100+i) * time.Hour)
		if err := write(s); err != nil {
			t.Fatal(err)
		}
		live = append(live, s.ID)
	}
	// Enough finished ones to push past the limit.
	for i := 0; i < keepFinished+5; i++ {
		_ = write(State{ID: "done-" + string(rune('a'+i)), Command: "vm-create", PID: 1,
			Started: time.Now().Add(-time.Duration(i) * time.Hour)})
	}
	if _, err := Start("30", []string{"trigger"}); err != nil {
		t.Fatal(err)
	}

	for _, id := range live {
		if _, err := Status(id); err != nil {
			t.Errorf("running job %s was pruned: %v", id, err)
		}
	}
}
