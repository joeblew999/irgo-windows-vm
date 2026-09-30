package glazecheck

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Where the verdict is written, and how two runs share one file.
//
// One file, two sections — the Mac and Windows — each between its own markers.
// A run replaces its own section and copies the other through untouched, so
// checking the Mac after Windows does not erase what Windows said. The markers
// carry the facts glaze-status needs to decide whether a verdict still
// describes the tree, as key=value pairs, so nothing parses the prose.

// Targets, in the order the file lists them.
const (
	TargetMac     = "mac"
	TargetWindows = "windows"
)

var targets = []string{TargetMac, TargetWindows}

// Result is one program's outcome.
type Result struct {
	Name string
	Pass bool

	// NotRun is a program that could not be run at all — the guest agent
	// went away, the lock was held. That is not a glaze failure and must not
	// be recorded as one, and it is not a pass either: "cannot tell".
	NotRun bool

	// FirstFail is the first line the program printed that says FAIL, FAILED
	// or ERROR, or failing that the error that ended it. Empty on a pass.
	FirstFail string
}

// Section is one run, as recorded.
type Section struct {
	Target   string
	When     time.Time
	Elapsed  time.Duration
	Platform string
	Tree     Tree
	GoWork   string
	Deps     []Dep

	// BuildError is set when the examples did not compile, in which case
	// nothing ran and Results is empty.
	BuildError string
	Results    []Result
	Log        string
}

// Verdict is YES, NO, or CANNOT TELL, and the names behind the last two.
func (s Section) Verdict() string {
	if s.BuildError != "" {
		return "NO: the examples did not build"
	}
	var failed, notRun []string
	for _, r := range s.Results {
		switch {
		case r.NotRun:
			notRun = append(notRun, r.Name)
		case !r.Pass:
			failed = append(failed, r.Name)
		}
	}
	switch {
	case len(failed) > 0:
		return "NO: failed: " + strings.Join(failed, " ")
	case len(notRun) > 0:
		return "CANNOT TELL: did not run: " + strings.Join(notRun, " ")
	case len(s.Results) == 0:
		return "CANNOT TELL: nothing ran"
	default:
		return fmt.Sprintf("YES: all %d passed", len(s.Results))
	}
}

func title(target string) string {
	if target == TargetMac {
		return "On the Mac"
	}
	return "On Windows"
}

func openMarker(target string) string  { return "<!-- glaze-status:" + target }
func closeMarker(target string) string { return "<!-- /glaze-status:" + target + " -->" }

// markdown renders one section, markers included.
func (s Section) markdown() string {
	var b strings.Builder
	fields := []string{
		"commit=" + s.Tree.Commit,
		fmt.Sprintf("examples-dirty=%t", s.Tree.ExamplesDirty),
	}
	for _, d := range s.Deps {
		fields = append(fields, filepath.Base(d.Path)+"="+d.Key())
	}
	fmt.Fprintf(&b, "%s %s -->\n", openMarker(s.Target), strings.Join(fields, " "))
	fmt.Fprintf(&b, "## %s — %s\n\n", title(s.Target), s.Verdict())

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
	if s.GoWork != "" {
		fmt.Fprintf(&b, "- workspace: `%s` (go.work — `mise run upstream:unlink` removes it)\n", s.GoWork)
	}
	fmt.Fprintf(&b, "- full log: `%s`\n\n", s.Log)

	if s.BuildError != "" {
		fmt.Fprintf(&b, "The examples did not build, so nothing ran:\n\n```\n%s\n```\n", s.BuildError)
	} else {
		b.WriteString("| program | result | first failure |\n|---|---|---|\n")
		for _, r := range s.Results {
			res, line := "PASS", ""
			switch {
			case r.NotRun:
				res, line = "NOT RUN", cell(r.FirstFail)
			case !r.Pass:
				res, line = "**FAIL**", cell(r.FirstFail)
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Name, res, line)
		}
	}
	b.WriteString(closeMarker(s.Target) + "\n")
	return b.String()
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

const header = `# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by ` + "`irgo-winvm glaze-check`" + ` (` + "`mise run glaze:mac`" + ` and
` + "`mise run glaze:windows`" + `) and read back by ` + "`irgo-winvm glaze-status`" + `,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. What each program checks is in
[CONTRIBUTING.md](CONTRIBUTING.md#does-glaze-work).

`

// placeholder is a section nobody has run yet.
func placeholder(target string) string {
	return fmt.Sprintf("%s -->\n## %s — not recorded yet\n\nRun `irgo-winvm glaze-check%s`.\n%s\n",
		openMarker(target), title(target), map[string]string{TargetMac: "", TargetWindows: " -windows"}[target], closeMarker(target))
}

// sections splits an existing file into its recorded sections, by target.
// A file that is missing or has no markers yields none, and the missing
// sections are then written as placeholders.
func sections(body string) map[string]string {
	out := map[string]string{}
	for _, t := range targets {
		i := strings.Index(body, openMarker(t)+" ")
		if i < 0 {
			continue
		}
		end := closeMarker(t)
		j := strings.Index(body[i:], end)
		if j < 0 {
			continue
		}
		out[t] = body[i:i+j+len(end)] + "\n"
	}
	return out
}

// Record writes s into the status file under root, keeping the other target's
// section as it was.
//
// Written whole to a temporary file and renamed, and the close checked: this
// is the file both an owner and an agent will trust, and a half-written one
// would be believed.
func Record(root string, s Section) (string, error) {
	path := filepath.Join(root, StatusFile)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	have := sections(string(old))
	have[s.Target] = s.markdown()

	var b strings.Builder
	b.WriteString(header)
	for i, t := range targets {
		if i > 0 {
			b.WriteString("\n")
		}
		sec, ok := have[t]
		if !ok {
			sec = placeholder(t)
		}
		b.WriteString(sec)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".glaze-status-*")
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

// Read returns the status file under root.
func Read(root string) (string, error) {
	path := filepath.Join(root, StatusFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no glaze check has been recorded: %s does not exist. Run irgo-winvm glaze-check", path)
	}
	return string(b), err
}

// markerLine is a section's opening marker, holding its key=value pairs.
var markerLine = regexp.MustCompile(`<!-- glaze-status:([a-z]+) ([^>]*?) -->`)

// Freshness says, for each recorded section, whether it still describes the
// tree: yes, no, or cannot tell.
//
// "Still describes" means the two inputs that decide the result are unchanged —
// examples/ (compared by git against the recorded commit, working tree
// included) and the glaze and native the examples resolve to now. It does not
// compare the whole commit, because committing the status file is itself a new
// commit, and a check that called every verdict stale the moment it was
// committed would be one nobody reads. It also does not see a change to the
// VM, or to app-create itself — say so rather than claim more.
func Freshness(root, body string) []string {
	var lines []string
	deps, _, depErr := ReadDeps(root)
	now := map[string]string{}
	for _, d := range deps {
		now[filepath.Base(d.Path)] = d.Key()
	}
	for _, m := range markerLine.FindAllStringSubmatch(body, -1) {
		target, rec := m[1], map[string]string{}
		for _, f := range strings.Fields(m[2]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				rec[k] = v
			}
		}
		lines = append(lines, target+": "+fresh(root, rec, now, depErr))
	}
	if len(lines) == 0 {
		lines = append(lines, "nothing recorded yet: run irgo-winvm glaze-check")
	}
	return lines
}

func fresh(root string, rec, now map[string]string, depErr error) string {
	commit := rec["commit"]
	if commit == "" {
		return "CANNOT TELL — the section names no commit"
	}
	if rec["examples-dirty"] == "true" {
		return "CANNOT TELL — it was recorded with uncommitted changes in examples/, which git cannot compare against"
	}
	var why []string
	c := exec.Command("git", "diff", "--quiet", commit, "--", "examples")
	c.Dir = root
	err := c.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		why = append(why, "examples/ has changed since "+short(commit))
	default:
		return fmt.Sprintf("CANNOT TELL — git could not compare examples/ with %s: %v", short(commit), err)
	}
	if depErr != nil {
		return fmt.Sprintf("CANNOT TELL — could not ask go what examples/ builds against now: %v", depErr)
	}
	for _, lib := range []string{"glaze", "native"} {
		if rec[lib] != now[lib] {
			why = append(why, fmt.Sprintf("%s was %s and is now %s", lib, rec[lib], now[lib]))
		}
	}
	if len(why) > 0 {
		return "STALE — " + strings.Join(why, "; ")
	}
	return "current — examples/, glaze and native are what they were when it ran (the VM and the tool itself are not compared)"
}
