package glazecheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func section(target string, results ...Result) Section {
	return Section{
		Target:   target,
		When:     time.Date(2026, 9, 30, 17, 30, 0, 0, time.UTC),
		Platform: target + "/arm64",
		Tree:     Tree{Commit: "0123456789abcdef0123456789abcdef01234567"},
		Deps: []Dep{
			{Path: "github.com/crgimenes/glaze", Version: "v0.0.61"},
			{Path: "github.com/crgimenes/native", Version: "v0.1.15", Dir: "/src/native", Branch: "fix/x", Commit: "58f48b7c6ef9", Dirty: true},
		},
		Results: results,
		Log:     "~/logs/glaze-" + target + ".log",
	}
}

// TestRecordKeepsTheOtherTarget is the property the file exists for: checking
// the Mac after Windows must not erase what Windows said.
//
// Negative control, run by hand: making Record start from an empty map instead
// of sections(old) fails this — the Windows verdict is replaced by the
// placeholder.
func TestRecordKeepsTheOtherTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	win := section(TargetWindows, Result{Name: "verify", FirstFail: "FAIL: absolute app:// sub-resources never loaded"})
	if _, err := Record(root, win); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(root, section(TargetMac, Result{Name: "verify", Pass: true})); err != nil {
		t.Fatal(err)
	}
	// And again, so a section that was read back from the file survives too,
	// not only one that was just rendered.
	if _, err := Record(root, section(TargetMac, Result{Name: "verify", Pass: true})); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{
		"## On Windows — NO: failed: verify",
		"`FAIL: absolute app:// sub-resources never loaded`",
		"## On the Mac — YES: all 1 passed",
		"native LINKED to /src/native — branch fix/x, commit 58f48b7c6ef9, with uncommitted changes",
		"glaze=v0.0.61 native=linked@58f48b7c6ef9+dirty",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status file lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "<!-- glaze-status:mac "); n != 1 {
		t.Errorf("%d Mac sections, want 1", n)
	}
	if strings.Index(body, "On the Mac") > strings.Index(body, "On Windows") {
		t.Error("the Mac section should come first whatever order the runs happened in")
	}
}

// TestFirstFailIsTheLineThatSaysSo: the four each say "broken" differently, and
// the record wants that line, not the exit status.
//
// Negative control, run by hand: returning err.Error() unconditionally fails
// every case but the last.
func TestFirstFailIsTheLineThatSaysSo(t *testing.T) {
	exit := errors.New("verify.exe exited 1 in the guest")
	cases := []struct{ name, out, want string }{
		{"verify", "PASS: page\n[  12.3s] FAIL: timed out waiting for JS to call Go\nFAIL: later\n", "FAIL: timed out waiting for JS to call Go"},
		{"probe row", "CAPABILITY  STATUS  DETAIL\nclipboard.read   ERROR   no display\n\n1 capability/capabilities ERROR\n", "clipboard.read   ERROR   no display"},
		{"glaze-all row", "menu.Set  FAILED  needs a window\n", "menu.Set  FAILED  needs a window"},
		{"not a status word", "failover OK\nFAILURES are fine to mention? no\n", exit.Error()},
		{"nothing printed", "", exit.Error()},
	}
	for _, c := range cases {
		if got := firstFail(c.out, exit); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestVerdictNeverCallsNotRunAFailure: a guest agent that went away says
// nothing about glaze, and must not be recorded as NO — nor as YES.
func TestVerdictNeverCallsNotRunAFailure(t *testing.T) {
	cases := []struct {
		s    Section
		want string
	}{
		{section(TargetMac, Result{Name: "a", Pass: true}, Result{Name: "b", Pass: true}), "YES: all 2 passed"},
		{section(TargetMac, Result{Name: "a", Pass: true}, Result{Name: "b"}), "NO: failed: b"},
		{section(TargetMac, Result{Name: "a", Pass: true}, Result{Name: "b", NotRun: true}), "CANNOT TELL: did not run: b"},
		{section(TargetMac, Result{Name: "a"}, Result{Name: "b", NotRun: true}), "NO: failed: a"},
		{section(TargetMac), "CANNOT TELL: nothing ran"},
		{Section{BuildError: "x"}, "NO: the examples did not build"},
	}
	for _, c := range cases {
		if got := c.s.Verdict(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
