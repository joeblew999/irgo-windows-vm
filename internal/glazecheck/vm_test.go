package glazecheck

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// partsSuite has a test for each part: TestSession* run in the desktop
// session, the rest as SYSTEM, as examples/vmconformance is split.
const partsSuite = `package fake

import "testing"

func TestSystemThing(t *testing.T) {
	t.Log("fact: windows=26100.4349 (24H2)")
	t.Log("evidence: AUOptions=2")
	t.Log("evidence: NoAutoRebootWithLoggedOnUsers=1")
}

func TestSystemBroken(t *testing.T) {
	t.Log("fact: free=12 GiB")
	t.Fatal("the reason it failed")
}

func TestSessionThing(t *testing.T) { t.Log("fact: windows=26100.9999") }
`

// fakeExe builds src as a test binary and returns its path.
func fakeExe(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module fake\n\ngo 1.24\n", "fake_test.go": src} {
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
	return exe
}

func runExe(exe string, args []string) (string, error) {
	out, err := exec.Command(exe, args...).CombinedOutput()
	return string(out), err
}

// TestPartsRunApartAndAreNamed: a check in parts — SYSTEM, then the
// session — lists every part's results in order, keeps one value per fact
// (the later wins), and names a part that ran no test at all, rather than
// losing it or calling it the package.
//
// Negative controls, run by hand: naming the empty part's row "(package)"
// in convert fails the "(session)" check; dropping setFact in runParts lists
// windows twice.
func TestPartsRunApartAndAreNamed(t *testing.T) {
	exe := fakeExe(t, partsSuite)
	quiet := func(string, ...any) {}
	su := VM
	su.ShotsFlag = "" // the fake binary takes no such flag
	o := Options{Suite: &su, Target: "vc1", Parts: []Part{
		{Name: "as SYSTEM", Args: []string{"-test.skip=^TestSession"}, Run: runExe},
		{Name: "session", Args: []string{"-test.run=^TestSession"}, Run: runExe},
	}}
	sec := Section{Target: "vc1", suite: &su}
	events, notRun, err := runParts(o, &su, exe, &sec, quiet)
	if err != nil || notRun != nil {
		t.Fatalf("runParts: %v, %v", err, notRun)
	}
	var names []string
	for _, r := range sec.Results {
		names = append(names, r.Name+"="+r.Outcome)
	}
	if got, want := strings.Join(names, " "), "TestSystemThing=pass TestSystemBroken=fail TestSessionThing=pass"; got != want {
		t.Errorf("results %s, want %s", got, want)
	}
	if got := factString(sec.Facts); got != "windows=26100.9999 free=12 GiB" {
		t.Errorf("facts %q", got)
	}
	for _, r := range sec.Results {
		if r.Name == "TestSystemBroken" && !strings.HasSuffix(r.Detail, "the reason it failed") {
			t.Errorf("a fact line was taken for the first message: %q", r.Detail)
		}
		// A pass keeps what it read; the Detail of a pass is still dropped.
		if r.Name == "TestSystemThing" && (r.Evidence != "AUOptions=2 / NoAutoRebootWithLoggedOnUsers=1" || r.Detail != "") {
			t.Errorf("TestSystemThing = %+v", r)
		}
	}
	if md := sec.markdown(); !strings.Contains(md, "| TestSystemThing | PASS |  | `AUOptions=2 / NoAutoRebootWithLoggedOnUsers=1` |") {
		t.Errorf("the evidence is not in the table:\n%s", md)
	}
	if !strings.Contains(string(events), "TestSessionThing") || !strings.Contains(string(events), "TestSystemThing") {
		t.Error("the events kept beside the log lack a part")
	}

	// The session part could not start: no desktop session. A failure of
	// its own, named, beside the results the other part did get.
	o.Parts[1].Run = func(string, []string) (string, error) {
		return "", errors.New("no desktop session for dev")
	}
	sec = Section{Target: "vc1", suite: &su}
	if _, _, err := runParts(o, &su, exe, &sec, quiet); err != nil {
		t.Fatal(err)
	}
	last := sec.Results[len(sec.Results)-1]
	if last.Name != "(session)" || last.Outcome != Fail || !strings.Contains(last.Detail, "no desktop session") {
		t.Errorf("the part that could not start is %+v", last)
	}
	if sec.Passed() {
		t.Errorf("a check whose session part never ran passed: %s", sec.Verdict())
	}

	// An error NotRun recognises ends the check as CANNOT TELL.
	gone := errors.New("guest agent gone")
	o.NotRun = func(err error) bool { return errors.Is(err, gone) }
	o.Parts[0].Run = func(string, []string) (string, error) { return "", gone }
	sec = Section{Target: "vc1", suite: &su}
	if _, notRun, _ := runParts(o, &su, exe, &sec, quiet); !errors.Is(notRun, gone) {
		t.Errorf("notRun = %v, want the agent error", notRun)
	}
}

func factString(fs []Fact) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Key+"="+f.Value)
	}
	return strings.Join(out, " ")
}

func vmSection(vm string, results ...Result) Section {
	s := section(vm, results...)
	s.Deps = nil
	s.suite = &VM
	return s
}

// TestVMRecordKeepsEveryVM: one section per VM, irgo-win11 first (a
// placeholder until it is checked) and the others in name order; a run
// replaces only its own VM's section; glaze's file takes no such target.
//
// Negative control, run by hand: setting VM.Open to false fails this — vc1
// is refused.
func TestVMRecordKeepsEveryVM(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	vc1 := vmSection("vc1", Result{Name: "TestHibernationOff", Outcome: Fail, Detail: "C:\\hiberfil.sys is there"})
	vc1.Facts = []Fact{{"windows", "26100.4349 (24H2)"}}
	for _, s := range []Section{
		vc1,
		vmSection("irgo-golden-verify", Result{Name: "TestHibernationOff", Outcome: Pass}),
	} {
		if _, err := Record(root, s); err != nil {
			t.Fatal(err)
		}
	}
	body := read(t, filepath.Join(root, VMStatusFile))
	for _, want := range []string{
		"# VM status",
		"## irgo-win11 — not recorded yet",
		"Run `irgo-winvm vm-check -vm irgo-win11`.",
		"## vc1 — NO: failed: TestHibernationOff",
		"- windows: 26100.4349 (24H2)",
		"## irgo-golden-verify — YES: 1 passed, 0 skipped",
		"<!-- vm-status:vc1 commit=0123456789abcdef0123456789abcdef01234567 source-dirty=false when=2026-09-30T17:30:00Z -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("VM status file lacks %q:\n%s", want, body)
		}
	}
	if !(strings.Index(body, "## irgo-win11") < strings.Index(body, "## irgo-golden-verify") &&
		strings.Index(body, "## irgo-golden-verify") < strings.Index(body, "## vc1")) {
		t.Errorf("sections out of order:\n%s", body)
	}

	// vc1 again, now passing: irgo-golden-verify's section is untouched.
	if _, err := Record(root, vmSection("vc1", Result{Name: "TestHibernationOff", Outcome: Pass})); err != nil {
		t.Fatal(err)
	}
	body = read(t, filepath.Join(root, VMStatusFile))
	if strings.Count(body, "<!-- vm-status:vc1 ") != 1 || !strings.Contains(body, "## vc1 — YES") ||
		!strings.Contains(body, "## irgo-golden-verify — YES") {
		t.Errorf("re-recording vc1 did not replace only vc1:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(root, StatusFile)); err == nil {
		t.Error("the VM suite wrote glaze's status file")
	}
	if _, err := Record(root, section("vc1")); err == nil {
		t.Error("glaze's file took a section for a target it does not have")
	}
}

// TestVMVerdicts: the same vocabulary as glaze, with the VM's word for a
// known failure, a Known with no target holding on every VM, and a gate that
// lets KNOWN ISSUES ONLY through and nothing else but YES.
//
// Negative control, run by hand: making Passed test for "KNOWN BUGS ONLY"
// instead of the suite's KnownVerdict fails the first case.
func TestVMVerdicts(t *testing.T) {
	defer func(k []Known) { KnownVM = k }(KnownVM)
	KnownVM = []Known{{Test: "TestHibernationOff", Ref: "installed before sealing turned it off"}}

	ref := VM.knownRef("any-vm", "TestHibernationOff")
	if ref == "" {
		t.Fatal("a Known with no Target does not hold on every VM")
	}
	if Glaze.knownRef(TargetWindows, "TestHibernationOff") != "" {
		t.Fatal("the VM's known list leaked into glaze's")
	}
	for _, c := range []struct {
		name   string
		s      Section
		prefix string
		passed bool
	}{
		{"only the known one fails", vmSection("v", Result{Name: "TestHibernationOff", Outcome: Fail, Known: ref}, Result{Name: "TestA", Outcome: Pass}),
			"KNOWN ISSUES ONLY: TestHibernationOff (installed before sealing turned it off) fail, known VM issues", true},
		{"the known one passes", vmSection("v", Result{Name: "TestHibernationOff", Outcome: Pass, Known: ref}),
			"UNEXPECTED PASS: known failure now passes: TestHibernationOff", false},
		{"a new failure", vmSection("v", Result{Name: "TestB", Outcome: Fail}), "NO: failed: TestB", false},
		{"all pass", vmSection("v", Result{Name: "TestA", Outcome: Pass}), "YES: 1 passed, 0 skipped", true},
		{"did not run", func() Section { s := vmSection("v"); s.NotRun = "guest agent gone"; return s }(), "CANNOT TELL: the suite did not run", false},
		{"did not build", func() Section { s := vmSection("v"); s.BuildError = "x"; return s }(), "NO: the VM conformance suite did not build", false},
	} {
		if v := c.s.Verdict(); !strings.HasPrefix(v, c.prefix) || c.s.Passed() != c.passed {
			t.Errorf("%s: %q passed=%t, want prefix %q passed=%t", c.name, v, c.s.Passed(), c.prefix, c.passed)
		}
	}
	// Glaze keeps its own words.
	g := section(TargetWindows, Result{Name: "TestA", Outcome: Fail, Known: "docs/UPSTREAM.md §1"})
	if v := g.Verdict(); !strings.HasPrefix(v, "KNOWN BUGS ONLY") || !g.Passed() {
		t.Errorf("glaze's known verdict became %q", v)
	}
}

// TestVMShotFromTheHost: a picture a host-side result names (the desktop,
// photographed with vm-screen after the suite) goes where the suite's
// pictures go, and the Screenshots table has a column per VM.
func TestVMShotFromTheHost(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vm := range []string{"vc1", "irgo-win11"} {
		s := vmSection(vm, Result{Name: "TestA", Outcome: Pass}, Result{Name: "Host/DesktopScreenshot", Outcome: Pass, Shot: "vm/Host_DesktopScreenshot.png"})
		if err := collectShots(root, &s, func(string) ([]byte, error) { return fakePNG, nil }, func(string, ...any) {}); err != nil {
			t.Fatal(err)
		}
		if _, err := Record(root, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "docs/screens/vm-conformance/vc1/Host_DesktopScreenshot.png")); err != nil {
		t.Errorf("the desktop picture is not in the VM's directory: %v", err)
	}
	body := read(t, filepath.Join(root, VMStatusFile))
	for _, want := range []string{
		"| test | irgo-win11 | vc1 |\n|---|---|---|\n",
		`<img src="screens/vm-conformance/vc1/Host_DesktopScreenshot.png" width="280" alt="Host/DesktopScreenshot on vc1">`,
		"- screenshots: 1 of 1 taken",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("VM status file lacks %q:\n%s", want, body)
		}
	}
}

// TestVMFreshness: a VM record is stale when the checks changed, cannot tell
// when it was made from uncommitted checks, and otherwise is never called
// current — only how old it is, because the VM changes under any record.
//
// Negative control, run by hand: comparing "examples" instead of vmSource in
// vmFresh makes the change to examples/conformance (not the VM suite) stale.
func TestVMFreshness(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("examples/vmconformance/a_test.go", "package a\n")
	write("examples/conformance/b_test.go", "package b\n")
	git("add", ".")
	git("commit", "-q", "-m", "one")
	commit := git("rev-parse", "HEAD")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rec := map[string]string{"commit": commit, "source-dirty": "false", "when": "2026-10-01T10:30:00Z"}

	write("examples/conformance/b_test.go", "package b // changed\n")
	if got := vmFresh(root, rec, now); !strings.HasPrefix(got, "checks unchanged since") || !strings.Contains(got, "1h30m ago") {
		t.Errorf("unchanged checks: %q", got)
	}
	write("examples/vmconformance/a_test.go", "package a // changed\n")
	if got := vmFresh(root, rec, now); !strings.HasPrefix(got, "STALE — examples/vmconformance has changed") {
		t.Errorf("changed checks: %q", got)
	}
	rec["source-dirty"] = "true"
	if got := vmFresh(root, rec, now); !strings.HasPrefix(got, "CANNOT TELL") {
		t.Errorf("dirty record: %q", got)
	}
	if got := vmFresh(root, map[string]string{}, now); !strings.HasPrefix(got, "CANNOT TELL") {
		t.Errorf("no commit: %q", got)
	}
}

// TestChanged: what a repair fixed and what it broke, by test name; a
// parent failed only by its subtest is not counted.
func TestChanged(t *testing.T) {
	before := vmSection("v",
		Result{Name: "TestA", Outcome: Fail}, Result{Name: "TestB", Outcome: Pass},
		Result{Name: "TestC", Outcome: Fail, Inherited: true}, Result{Name: "TestC/x", Outcome: Fail},
		Result{Name: "TestD", Outcome: Fail})
	after := vmSection("v",
		Result{Name: "TestA", Outcome: Pass}, Result{Name: "TestB", Outcome: Fail},
		Result{Name: "TestC", Outcome: Pass}, Result{Name: "TestC/x", Outcome: Pass},
		Result{Name: "TestD", Outcome: Fail})
	fixed, broke := Changed(before, after)
	if strings.Join(fixed, " ") != "TestA TestC/x" || strings.Join(broke, " ") != "TestB" {
		t.Errorf("fixed %v, broke %v", fixed, broke)
	}
}
