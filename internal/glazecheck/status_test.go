package glazecheck

import (
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
	win := section(TargetWindows, Result{Name: "TestAppScheme/absolute_subresources", Outcome: Fail, Detail: "app://home/abs.js did not run"})
	if _, err := Record(root, win); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(root, section(TargetMac, Result{Name: "TestAppScheme/absolute_subresources", Outcome: Pass})); err != nil {
		t.Fatal(err)
	}
	// And again, so a section that was read back from the file survives too,
	// not only one that was just rendered.
	if _, err := Record(root, section(TargetMac, Result{Name: "TestAppScheme/absolute_subresources", Outcome: Pass})); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{
		"## On Windows — NO: failed: TestAppScheme/absolute_subresources",
		"| TestAppScheme/absolute_subresources | **FAIL** | `app://home/abs.js did not run` |",
		"## On the Mac — YES: 1 passed, 0 skipped",
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

// TestVerdictNeverCallsNotRunAFailure: a guest agent that went away says
// nothing about glaze, and must not be recorded as NO — nor as YES.
func TestVerdictNeverCallsNotRunAFailure(t *testing.T) {
	notRun := section(TargetWindows)
	notRun.NotRun = "the guest agent is not answering"
	cases := []struct {
		s    Section
		want string
	}{
		{section(TargetMac, Result{Name: "a", Outcome: Pass}, Result{Name: "b", Outcome: Skip}), "YES: 1 passed, 1 skipped"},
		{section(TargetMac, Result{Name: "a", Outcome: Pass}, Result{Name: "b", Outcome: Fail}), "NO: failed: b"},
		{section(TargetMac, Result{Name: "a", Outcome: Unfinished}), "NO: failed: a"},
		{notRun, "CANNOT TELL: the suite did not run: the guest agent is not answering"},
		{section(TargetMac), "CANNOT TELL: nothing ran"},
		{Section{BuildError: "x"}, "NO: the conformance suite did not build"},
	}
	for _, c := range cases {
		if got := c.s.Verdict(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
		if c.s.Passed() != strings.HasPrefix(c.want, "YES") {
			t.Errorf("%q: Passed() = %t", c.want, c.s.Passed())
		}
	}
}

// TestForkIsNamed: a module replaced by another published module — native
// from the joeblew999 fork — must be recorded as the fork, not as the version
// go.mod requires, which is not what was built.
//
// Negative control, run by hand: dropping the Fork case from Dep.String and
// Dep.Key fails this — the record says "native v0.1.15 (released)".
func TestForkIsNamed(t *testing.T) {
	d := Dep{Path: "github.com/crgimenes/native", Version: "v0.1.15", Fork: "github.com/joeblew999/native@v0.1.16-0.20260930085437-93363ebf8e8e"}
	if got, want := d.Key(), "github.com/joeblew999/native@v0.1.16-0.20260930085437-93363ebf8e8e"; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
	if got := d.String(); !strings.Contains(got, "joeblew999/native@v0.1.16") || strings.Contains(got, "(released)") {
		t.Errorf("String = %q: it must name the fork and must not say released", got)
	}
	// A go.work link overrides the fork, and then the clone is what was built.
	d.Dir, d.Commit = "/src/native", "58f48b7c6ef9"
	if got := d.Key(); got != "linked@58f48b7c6ef9" {
		t.Errorf("linked Key = %q, want linked@58f48b7c6ef9", got)
	}
}
