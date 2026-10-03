package glazecheck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Where the verdict is written, and how runs share one file.
//
// One file per suite, one section per target — for glaze the Mac and
// Windows, for the VM suite each VM checked — each between its own markers.
// A run replaces its own section and copies the others through untouched, so
// checking the Mac after Windows does not erase what Windows said. The markers
// carry the facts the status command needs to decide whether a verdict still
// describes the tree, as key=value pairs, so nothing parses the prose.

// Targets, in the order the file lists them.
const (
	TargetMac     = "mac"
	TargetWindows = "windows"
)

var targets = []string{TargetMac, TargetWindows}

// Result is one test's or subtest's outcome, from its test2json events.
type Result struct {
	Name    string // TestAppScheme/absolute_subresources
	Outcome string // Pass, Fail, Skip or Unfinished

	// Detail is the first line the test wrote itself — its t.Error, t.Fatal
	// or t.Skip message. Empty on a pass.
	Detail string

	// Known is the docs/reference/upstream.md reference when this test is a known
	// upstream failure on this target (KnownUpstream), whatever its outcome.
	Known string

	// Inherited is a parent that failed only because a subtest did, and
	// printed nothing of its own. It is shown, and not counted again.
	Inherited bool

	// Retried is every step the test tried again, and why, from its
	// "retry:" lines, joined with " / ". Kept on a pass: a pass that needed a
	// retry is not the same as one that did not, and the record says so.
	Retried string

	// Shot is the screenshot the test logged, as the suite names it
	// (<os>/<Test>.png, relative to the directory it was given), and ShotNote
	// what it said about the picture. NoShot is why a test that tried took
	// none. All three are empty for a test that opens no window.
	Shot, ShotNote, NoShot string

	// Evidence is what the test logged it read ("evidence: ..."), kept
	// whatever the outcome: for a check that passed, it is what was seen.
	Evidence string
}

func (r Result) failed() bool { return r.Outcome == Fail || r.Outcome == Unfinished }

// Fact is something a run found out and the record keeps, such as the
// Windows build a VM runs. A test logs it as "fact: key=value".
type Fact struct{ Key, Value string }

// Section is one run, as recorded.
type Section struct {
	Target   string
	When     time.Time
	Elapsed  time.Duration
	Platform string
	Tree     Tree
	GoWork   string
	Deps     []Dep

	// BuildError is set when the suite did not compile, in which case
	// nothing ran and Results is empty.
	BuildError string

	// NotRun is why the suite never got to run — the guest agent went away,
	// the lock was held. That is not a glaze failure and must not be recorded
	// as one, and it is not a pass either: "cannot tell".
	NotRun string

	Results []Result
	Facts   []Fact
	Log     string
	JSON    string // the test2json events, beside the log

	// RunURL is the GitHub Actions run this was recorded in, when it was.
	RunURL string

	suite *Suite // nil is Glaze
}

// Suite is the suite this section is a run of.
func (s Section) Suite() *Suite { return s.suite.orDefault() }

// Verdict is one line: YES, KNOWN BUGS ONLY (KNOWN ISSUES ONLY for the VM),
// NO, UNEXPECTED PASS, or CANNOT TELL, with the names behind anything but YES.
func (s Section) Verdict() string {
	su := s.Suite()
	if s.BuildError != "" {
		return "NO: the " + su.Thing + " did not build"
	}
	if s.NotRun != "" {
		return "CANNOT TELL: the suite did not run: " + s.NotRun
	}
	var failed, known, xpass []string
	passed, skipped := 0, 0
	for _, r := range s.Results {
		switch {
		case r.Inherited:
		case r.failed() && r.Known != "":
			known = append(known, r.Name+" ("+r.Known+")")
		case r.failed():
			failed = append(failed, r.Name)
		case r.Known != "":
			xpass = append(xpass, r.Name+" ("+r.Known+")")
		}
		switch r.Outcome {
		case Pass:
			passed++
		case Skip:
			skipped++
		}
	}
	switch {
	case len(failed) > 0:
		return "NO: failed: " + strings.Join(failed, " ")
	case len(xpass) > 0:
		return "UNEXPECTED PASS: " + su.KnownLabel + " failure now passes: " + strings.Join(xpass, " ") + su.XPassTail
	case len(known) > 0:
		return su.KnownVerdict + ": " + strings.Join(known, " ") + su.KnownTail
	case len(s.Results) == 0:
		return "CANNOT TELL: nothing ran"
	default:
		return fmt.Sprintf("YES: %d passed, %d skipped", passed, skipped)
	}
}

// Passed reports whether the verdict should let a gate through: YES, or
// only the known failures.
func (s Section) Passed() bool {
	v := s.Verdict()
	return strings.HasPrefix(v, "YES") || strings.HasPrefix(v, s.Suite().KnownVerdict)
}

// markdown renders one section, markers included.
func (s Section) markdown() string {
	su := s.Suite()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s -->\n", su.openMarker(s.Target), strings.Join(su.Marker(s), " "))
	fmt.Fprintf(&b, "## %s — %s\n\n", su.Title(s.Target), s.Verdict())

	dirty := ""
	if s.Tree.Dirty {
		dirty = ", **with uncommitted changes**"
	}
	fmt.Fprintf(&b, "- when: %s, took %s\n", s.When.Format("2006-01-02 15:04 -0700"), s.Elapsed.Round(time.Second))
	fmt.Fprintf(&b, "- platform: %s\n", s.Platform)
	fmt.Fprintf(&b, "- this repository: commit `%s`%s\n", short(s.Tree.Commit), dirty)
	for _, d := range s.Deps {
		fmt.Fprintf(&b, "- %s\n", d.String())
	}
	for _, f := range s.Facts {
		fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
	}
	if s.GoWork != "" {
		fmt.Fprintf(&b, "- workspace: `%s` (go.work — `mise run upstream:unlink` removes it)\n", s.GoWork)
	}
	fmt.Fprintf(&b, "- full log: `%s`\n", s.Log)
	if s.JSON != "" {
		fmt.Fprintf(&b, "- test2json events: `%s`\n", s.JSON)
	}
	if s.RunURL != "" {
		fmt.Fprintf(&b, "- CI run: %s\n", s.RunURL)
	}
	if taken, tried := s.shotCounts(); tried > 0 {
		fmt.Fprintf(&b, su.ShotsBullet, taken, tried)
	}
	b.WriteString("\n")

	switch {
	case s.BuildError != "":
		fmt.Fprintf(&b, "The suite did not build, so nothing ran:\n\n```\n%s\n```\n", s.BuildError)
	case s.NotRun != "":
		fmt.Fprintf(&b, "The suite did not run, which says nothing about %s:\n\n```\n%s\n```\n", su.Subject, s.NotRun)
	default:
		if su.Evidence {
			b.WriteString("| test | result | first message | what it read |\n|---|---|---|---|\n")
		} else {
			b.WriteString("| test | result | first message |\n|---|---|---|\n")
		}
		for _, r := range s.Results {
			if su.Evidence {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.Name, r.label(su), cell(r.message()), cell(r.Evidence))
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Name, r.label(su), cell(r.message()))
		}
	}
	b.WriteString(su.closeMarker(s.Target) + "\n")
	return b.String()
}

// message is the table's message for a result: its first line, then what
// it retried.
func (r Result) message() string {
	switch {
	case r.Retried == "":
		return r.Detail
	case r.Detail == "":
		return "retried: " + r.Retried
	default:
		return r.Detail + " — retried: " + r.Retried
	}
}

// label is how the tables word a result.
func (r Result) label(su *Suite) string {
	switch {
	case r.Inherited:
		return "fail (a subtest failed)"
	case r.failed() && r.Known != "":
		return "**FAIL** — " + su.KnownLabel + ": " + r.Known
	case r.Outcome == Unfinished:
		return "**UNFINISHED**"
	case r.failed():
		return "**FAIL**"
	case r.Outcome == Skip:
		return "skip"
	case r.Known != "":
		return "PASS — **known failure " + r.Known + " no longer fails**"
	case r.Retried != "":
		return "PASS after a retry"
	default:
		return "PASS"
	}
}

// shotCounts is how many tests took a picture, of those that tried.
func (s Section) shotCounts() (taken, tried int) {
	for _, r := range s.Results {
		if r.Shot != "" {
			taken++
		}
		if r.Shot != "" || r.NoShot != "" {
			tried++
		}
	}
	return taken, tried
}

// cell makes a line safe inside a table cell: pipes split cells, and a
// backtick in the line would end the code span early.
func cell(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "`", "'")
	return "`" + s + "`"
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// placeholder is a section nobody has run yet.
func (s *Suite) placeholder(target string) string {
	return fmt.Sprintf("%s -->\n## %s — not recorded yet\n\n%s\n%s\n",
		s.openMarker(target), s.Title(target), s.Placeholder(target), s.closeMarker(target))
}

// sections splits an existing file into its recorded sections, by target.
// A file that is missing or has no markers yields none, and the missing
// fixed sections are then written as placeholders.
func (s *Suite) sections(body string) map[string]string {
	out := map[string]string{}
	for _, m := range s.anyMarkerRE().FindAllStringSubmatchIndex(body, -1) {
		t := body[m[2]:m[3]]
		if _, dup := out[t]; dup || !s.accepts(t) {
			continue
		}
		i := m[0]
		end := s.closeMarker(t)
		j := strings.Index(body[i:], end)
		if j < 0 {
			continue
		}
		out[t] = body[i:i+j+len(end)] + "\n"
	}
	return out
}

// sections is the glaze file's sections.
func sections(body string) map[string]string { return Glaze.sections(body) }

// Record writes sec into its suite's status file under root, keeping the
// other targets' sections as they were.
//
// Written whole to a temporary file and renamed, and the close checked: this
// is the file both an owner and an agent will trust, and a half-written one
// would be believed.
func Record(root string, sec Section) (string, error) {
	su := sec.Suite()
	if !su.accepts(sec.Target) {
		return "", fmt.Errorf("%q is not a target of the %s suite", sec.Target, su.Name)
	}
	return su.record(root, sec.Target, sec.markdown())
}

// record writes one target's section, already rendered, into the status file
// under root, keeps the others, and redraws the Screenshots table from every
// target's manifest. Import calls it with a section read from a CI run's
// file.
func (s *Suite) record(root, target, section string) (string, error) {
	path := filepath.Join(root, s.StatusFile)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	have := s.sections(string(old))
	have[target] = section

	var b strings.Builder
	b.WriteString(s.Header)
	order := s.order(have)
	for i, t := range order {
		if i > 0 {
			b.WriteString("\n")
		}
		sec, ok := have[t]
		if !ok {
			sec = s.placeholder(t)
		}
		b.WriteString(sec)
	}
	shots, err := s.gallery(root, order)
	if err != nil {
		return "", err
	}
	if shots != "" {
		b.WriteString("\n" + shots)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+s.Name+"-status-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// Read returns the glaze status file under root.
func Read(root string) (string, error) { return Glaze.Read(root) }

// Read returns the suite's status file under root.
func (s *Suite) Read(root string) (string, error) {
	path := filepath.Join(root, s.StatusFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no %s check has been recorded: %s does not exist. Run irgo-winvm %s", s.Name, path, s.Command)
	}
	return string(b), err
}

// Freshness says, for each recorded glaze section, whether it still
// describes the tree: yes, no, or cannot tell.
func Freshness(root, body string) []string { return Glaze.Freshness(root, body) }

// Freshness says, for each recorded section of the suite's file, whether it
// still describes the tree, as the suite's Fresh decides.
func (s *Suite) Freshness(root, body string) []string {
	var lines []string
	var check func(map[string]string) string
	for _, m := range s.markerRE().FindAllStringSubmatch(body, -1) {
		target, rec := m[1], map[string]string{}
		for _, f := range strings.Fields(m[2]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				rec[k] = v
			}
		}
		if check == nil {
			check = s.Fresh(root)
		}
		lines = append(lines, target+": "+check(rec))
	}
	if len(lines) == 0 {
		lines = append(lines, "nothing recorded yet: run irgo-winvm "+s.Command)
	}
	return lines
}
