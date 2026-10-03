package glazecheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// Suite is the conformance suite: its package, relative to the examples
// module, and its import path, which is what test2json events name.
const (
	SuiteDir     = "./conformance"
	SuitePackage = repoModule + "/examples/conformance"
)

// Outcomes a test can have in the record.
const (
	Pass       = "pass"
	Fail       = "fail"
	Skip       = "skip"
	Unfinished = "unfinished" // started, and the binary exited or hung before it ended
)

// Known is a failure recorded in docs/reference/upstream.md (not necessarily reported upstream yet), expected on one target until
// the fix is released. For the VM suite it is a VM property known to be
// missing, with the reason (KnownVM); there a Known with no Target holds on
// every VM.
//
// It is still recorded as a FAIL, with Ref beside it, and the test itself
// still fails: nothing is skipped, and anyone running the suite by hand sees
// it red. What Known changes is only what a run concludes. A run whose only
// failures are known ones answers KNOWN BUGS ONLY and exits 0, so a gate — the
// VM run before a commit, the CI job — stays green for changes that broke
// nothing, and goes red for a failure that is new. A known failure that
// PASSES is reported too, and fails the run: either the fix landed and this
// list is out of date, or the test stopped testing what it names.
type Known struct {
	Target string
	Test   string
	Ref    string
}

// KnownUpstream is every known upstream failure. Keep it short and cited:
// each entry is a bug with a section in docs/reference/upstream.md.
var KnownUpstream = []Known{
	{Target: TargetWindows, Test: "TestAppScheme/absolute_subresources", Ref: "docs/reference/upstream.md §1b"},
}

// event is one line of `go tool test2json` output.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// toJSON converts what a test binary printed under -test.v=test2json into
// test2json events, by running Go's own converter. The framing the binary
// adds is for that tool, and its format is the toolchain's business, not
// something to reimplement here.
func toJSON(raw []byte) ([]byte, error) { return Glaze.toJSON(raw) }

func (s *Suite) toJSON(raw []byte) ([]byte, error) {
	c := exec.Command("go", "tool", "test2json", "-t", "-p", s.Package)
	c.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("go tool test2json: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// parseEvents is Glaze.parse without the facts, which glaze's tests log none of.
func parseEvents(target string, stream []byte) ([]Result, error) {
	rs, _, err := Glaze.parse(target, stream)
	return rs, err
}

// parse turns test2json events into one Result per test and subtest, in
// the order they started, and a Result named "(package)" when the binary
// failed in a way no single test accounts for — a panic in TestMain, a
// -test.timeout, a crash between tests. It also returns every fact a test
// logged ("fact: key=value"), in order, a later value for a key replacing
// an earlier one.
func (s *Suite) parse(target string, stream []byte) ([]Result, []Fact, error) {
	var facts []Fact
	var (
		order    []string
		byName   = map[string]*Result{}
		pkgFail  bool
		pkgLines []string
	)
	get := func(name string) *Result {
		r, ok := byName[name]
		if !ok {
			r = &Result{Name: name, Outcome: Unfinished}
			byName[name] = r
			order = append(order, name)
		}
		return r
	}
	dec := json.NewDecoder(bytes.NewReader(stream))
	for {
		var e event
		if err := dec.Decode(&e); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("reading test2json output: %w", err)
		}
		if e.Test == "" {
			switch e.Action {
			case "fail":
				pkgFail = true
			case "output":
				if l := strings.TrimSpace(e.Output); l != "" && !isFrame(l) {
					pkgLines = append(pkgLines, l)
				}
			}
			continue
		}
		r := get(e.Test)
		switch e.Action {
		case "pass":
			r.Outcome = Pass
		case "fail":
			r.Outcome = Fail
		case "skip":
			r.Outcome = Skip
		case "output":
			// A screenshot line is about the test, not from it: recorded, and
			// never taken for its first message.
			if shotLine(r, e.Output) {
				continue
			}
			// So is a fact: it is about the machine, and goes in the record.
			if k, v, ok := factLine(e.Output); ok {
				facts = setFact(facts, k, v)
				continue
			}
			// And what the test read, kept even when it passes.
			if ev, ok := evidenceLine(e.Output); ok {
				if r.Evidence != "" {
					r.Evidence += " / "
				}
				r.Evidence += ev
				continue
			}
			// So is a retry: recorded even on a pass, never the first message.
			if retryLine(r, e.Output) {
				continue
			}
			// The first line the test itself wrote: its t.Error, t.Fatal or
			// t.Skip message. The runner's own === and --- lines are not it.
			if l := strings.TrimSpace(e.Output); r.Detail == "" && l != "" && !isFrame(l) {
				r.Detail = l
			}
		}
	}

	var out []Result
	anyTestFailed := false
	for _, name := range order {
		r := *byName[name]
		if r.Outcome == Pass {
			r.Detail = "" // a passing test's log line is not a finding
		}
		if r.Outcome == Unfinished && r.Detail == "" {
			r.Detail = "started and never finished: the test binary exited or hung while it ran"
		}
		r.Known = s.knownRef(target, r.Name)
		if r.Outcome == Fail || r.Outcome == Unfinished {
			anyTestFailed = true
		}
		out = append(out, r)
	}
	markInheritedFailures(out)
	if pkgFail && !anyTestFailed {
		// test2json names the package as failed and no test: say what the
		// binary said last, which is where a panic or a timeout lands.
		if len(pkgLines) > 3 {
			pkgLines = pkgLines[len(pkgLines)-3:]
		}
		out = append(out, Result{Name: "(package)", Outcome: Fail, Detail: strings.Join(pkgLines, " / ")})
	}
	return out, facts, nil
}

// isFrame is a line the test runner prints about a test rather than one the
// test printed: === RUN, --- FAIL: and the package's closing PASS, FAIL, ok.
func isFrame(l string) bool {
	return strings.HasPrefix(l, "=== ") || strings.HasPrefix(l, "--- ") ||
		l == "PASS" || l == "FAIL" || strings.HasPrefix(l, "ok  \t") || strings.HasPrefix(l, "FAIL\t")
}

// markInheritedFailures marks a parent that failed only because a subtest did.
//
// A failing subtest fails its parent too, so TestAppScheme fails whenever
// TestAppScheme/absolute_subresources does. Counting both would call one bug
// two, and would count a known one as a new failure under its parent's name.
// A parent is inherited only if it printed nothing of its own: a t.Fatal in
// the parent body is its own failure, and stays one.
func markInheritedFailures(rs []Result) {
	for i := range rs {
		if rs[i].Outcome != Fail || rs[i].Detail != "" {
			continue
		}
		prefix := rs[i].Name + "/"
		for _, c := range rs {
			if strings.HasPrefix(c.Name, prefix) && (c.Outcome == Fail || c.Outcome == Unfinished) {
				rs[i].Inherited = true
				break
			}
		}
	}
}

// rawTail is the last n lines of what a binary printed, for the record when
// nothing structured came out of it.
func rawTail(raw []byte, n int) string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(strings.Trim(sc.Text(), "\x16")); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}

// The lines a suite logs about its screenshots (examples/shots: OK and
// None), after the file:line prefix t.Logf adds. A picture's
// line may end in a parenthesised note about it.
var (
	shotTaken  = regexp.MustCompile(`(?:^|: )screenshot: (\S+\.png)(?: \((.*)\))?$`)
	shotMissed = regexp.MustCompile(`(?:^|: )screenshot not captured: (.+)$`)
)

// retryTaken is the line examples/conformance logs when it tries a step
// again (drive_test.go: retried), after the file:line prefix t.Logf adds.
var retryTaken = regexp.MustCompile(`(?:^|: )retry: (.+)$`)

// retryLine records a retry line on r and reports whether l was one.
func retryLine(r *Result, l string) bool {
	m := retryTaken.FindStringSubmatch(strings.TrimSpace(l))
	if m == nil {
		return false
	}
	if r.Retried != "" {
		r.Retried += " / "
	}
	r.Retried += m[1]
	return true
}

// shotLine records a screenshot line on r and reports whether l was one.
func shotLine(r *Result, l string) bool {
	l = strings.TrimSpace(l)
	if m := shotTaken.FindStringSubmatch(l); m != nil {
		r.Shot, r.ShotNote, r.NoShot = m[1], m[2], ""
		return true
	}
	if m := shotMissed.FindStringSubmatch(l); m != nil {
		r.Shot, r.ShotNote, r.NoShot = "", "", m[1]
		return true
	}
	return false
}

// factMark is the line a test logs to put a fact in the record, after the
// file:line prefix t.Logf adds: "fact: windows=26100.4349 (24H2)".
var factMark = regexp.MustCompile(`(?:^|: )fact: ([A-Za-z0-9_.-]+)=(.*)$`)

// factLine reads a fact from one line of a test's output.
func factLine(l string) (key, value string, ok bool) {
	m := factMark.FindStringSubmatch(strings.TrimSpace(l))
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// evidenceMark is the line a test logs to say what it read: "evidence: ...".
var evidenceMark = regexp.MustCompile(`(?:^|: )evidence: (.+)$`)

// evidenceLine reads what a test said it read from one line of its output.
func evidenceLine(l string) (string, bool) {
	m := evidenceMark.FindStringSubmatch(strings.TrimSpace(l))
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// setFact sets key in facts, in place when it is there already.
func setFact(facts []Fact, key, value string) []Fact {
	for i := range facts {
		if facts[i].Key == key {
			facts[i].Value = value
			return facts
		}
	}
	return append(facts, Fact{key, value})
}
