package glazecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSuite is a test file whose outcomes are known in advance: one of each
// thing parseEvents must tell apart.
const fakeSuite = `package fake

import "testing"

func TestPasses(t *testing.T) { t.Log("a log line from a passing test") }

func TestRetries(t *testing.T) {
	t.Logf("retry: the first try did not land")
	t.Logf("retry: nor did the second")
}

func TestFails(t *testing.T) { t.Fatal("the reason it failed") }

func TestSkips(t *testing.T) { t.Skip("unsupported here by design") }

func TestParent(t *testing.T) {
	t.Run("fine", func(t *testing.T) {})
	t.Run("broken", func(t *testing.T) { t.Error("the subtest's reason") })
}

func TestParentOwnFailure(t *testing.T) {
	t.Run("fine", func(t *testing.T) {})
	t.Fatal("the parent's own reason")
}
`

// runFake builds src as a test binary and runs it exactly as Check runs the
// suite: -test.v=test2json, then Go's own test2json on what it printed. Built
// rather than canned, so a change in the toolchain's framing shows up here.
func runFake(t *testing.T, src string) []byte {
	t.Helper()
	return mustJSON(t, rawFake(t, src))
}

func mustJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	b, err := toJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rawFake is what the fake suite's binary printed, before test2json.
func rawFake(t *testing.T, src string) []byte {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":       "module fake\n\ngo 1.24\n",
		"fake_test.go": src,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(dir, "fake.test")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "test", "-c", "-o", exe, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the fake suite: %v\n%s", err, out)
	}
	raw, _ := exec.Command(exe, TestArgs...).CombinedOutput() // it fails by design
	return raw
}

// TestParseEventsAsAppCreateReturnsIt: the VM run gets the output through
// utmvm.AppCreate, which trims the trailing newline, and a guest's output file
// may carry CRLF. Neither may lose or change a result.
func TestParseEventsAsAppCreateReturnsIt(t *testing.T) {
	raw := rawFake(t, fakeSuite)
	want, err := parseEvents(TargetMac, mustJSON(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	for name, mangled := range map[string]string{
		"trailing newline trimmed": strings.TrimRight(string(raw), "\r\n"),
		"CRLF":                     strings.ReplaceAll(string(raw), "\n", "\r\n"),
	} {
		got, err := parseEvents(TargetMac, mustJSON(t, []byte(mangled)))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Errorf("%s: %d results, want %d: %+v", name, len(got), len(want), got)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: %+v, want %+v", name, got[i], want[i])
			}
		}
	}
}

// TestParseEventsFromARealBinary: every outcome, the first message of each,
// and a parent failed by its subtest counted once.
//
// Negative controls, run by hand: dropping markInheritedFailures makes the
// Verdict name TestParent as well as TestParent/broken; dropping the isFrame
// filter makes Detail the "--- FAIL:" line instead of the message.
func TestParseEventsFromARealBinary(t *testing.T) {
	rs, err := parseEvents(TargetMac, runFake(t, fakeSuite))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Result{}
	for _, r := range rs {
		got[r.Name] = r
	}
	for _, want := range []Result{
		{Name: "TestPasses", Outcome: Pass},
		{Name: "TestRetries", Outcome: Pass, Retried: "the first try did not land / nor did the second"},
		{Name: "TestFails", Outcome: Fail, Detail: "the reason it failed"},
		{Name: "TestSkips", Outcome: Skip, Detail: "unsupported here by design"},
		{Name: "TestParent", Outcome: Fail, Inherited: true},
		{Name: "TestParent/fine", Outcome: Pass},
		{Name: "TestParent/broken", Outcome: Fail, Detail: "the subtest's reason"},
		{Name: "TestParentOwnFailure", Outcome: Fail, Detail: "the parent's own reason"},
	} {
		r, ok := got[want.Name]
		if !ok {
			t.Errorf("no result for %s; got %v", want.Name, rs)
			continue
		}
		// The message carries file:line in front; the test cares that it is
		// the message, not where it came from.
		if r.Outcome != want.Outcome || r.Inherited != want.Inherited || !strings.HasSuffix(r.Detail, want.Detail) ||
			(want.Detail == "" && r.Detail != "") || r.Retried != want.Retried {
			t.Errorf("%s = %+v, want %+v", want.Name, r, want)
		}
	}
	if _, ok := got["(package)"]; ok {
		t.Error("a (package) row was added although tests account for the failure")
	}
	sec := Section{Results: rs}
	if v := sec.Verdict(); v != "NO: failed: TestFails TestParent/broken TestParentOwnFailure" {
		t.Errorf("verdict %q", v)
	}
}

// TestParseEventsAfterACrash: a binary that dies mid-test leaves that test
// unfinished, and the run is a failure, not a pass with a row missing.
func TestParseEventsAfterACrash(t *testing.T) {
	rs, err := parseEvents(TargetMac, runFake(t, `package fake

import ("os"; "testing")

func TestFirst(t *testing.T) {}

func TestDies(t *testing.T) { os.Exit(3) }

func TestNeverReached(t *testing.T) {}
`))
	if err != nil {
		t.Fatal(err)
	}
	sec := Section{Results: rs}
	if sec.Passed() {
		t.Fatalf("a binary that exited mid-test passed: %s, %+v", sec.Verdict(), rs)
	}
	if !strings.Contains(sec.Verdict(), "TestDies") {
		t.Errorf("the verdict does not name the test that was running: %s (%+v)", sec.Verdict(), rs)
	}
}

// TestKnownUpstream is the policy for a failure recorded in docs/UPSTREAM.md:
// failing is KNOWN BUGS ONLY and lets the gate through, passing is an
// UNEXPECTED PASS that does not, and a new failure beside it is still NO.
//
// Negative control, run by hand: making Passed accept only "YES" fails the
// first case.
func TestKnownUpstream(t *testing.T) {
	defer func(k []Known) { KnownUpstream = k }(KnownUpstream)
	KnownUpstream = []Known{{Target: TargetWindows, Test: "TestParent/broken", Ref: "docs/UPSTREAM.md §9"}}
	events := runFake(t, fakeSuite)

	win, err := parseEvents(TargetWindows, events)
	if err != nil {
		t.Fatal(err)
	}
	// Without TestFails and TestParentOwnFailure, the known one is all that fails.
	var onlyKnown []Result
	for _, r := range win {
		if r.Name != "TestFails" && r.Name != "TestParentOwnFailure" {
			onlyKnown = append(onlyKnown, r)
		}
	}
	for _, c := range []struct {
		name   string
		rs     []Result
		prefix string
		passed bool
	}{
		{"only the known failure", onlyKnown, "KNOWN BUGS ONLY: TestParent/broken (docs/UPSTREAM.md §9)", true},
		{"a new failure beside it", win, "NO: failed: TestFails TestParentOwnFailure", false},
		{"the known failure passes", []Result{{Name: "TestParent/broken", Outcome: Pass, Known: "docs/UPSTREAM.md §9"}}, "UNEXPECTED PASS", false},
	} {
		sec := Section{Results: c.rs}
		if v := sec.Verdict(); !strings.HasPrefix(v, c.prefix) || sec.Passed() != c.passed {
			t.Errorf("%s: verdict %q passed=%t, want prefix %q passed=%t", c.name, v, sec.Passed(), c.prefix, c.passed)
		}
	}

	// On the Mac the same failure is not known: the list is per target.
	mac, err := parseEvents(TargetMac, events)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range mac {
		if r.Known != "" {
			t.Errorf("%s is marked known on the Mac: %q", r.Name, r.Known)
		}
	}
}
