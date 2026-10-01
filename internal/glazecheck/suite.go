package glazecheck

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Suite is one conformance suite: a go test package under examples/, where
// its record goes, and how its verdict is worded. The runner — build, run,
// test2json, record, screenshots, freshness — is the same for every suite;
// only what is listed here differs.
//
// There are two: Glaze (examples/conformance, docs/GLAZE-STATUS.md) and VM
// (examples/vmconformance, docs/VM-STATUS.md). The package is named for the
// first, which it was written for.
type Suite struct {
	// Name prefixes the run's log file (glaze-mac-<stamp>.log) and the status
	// file's markers (<!-- glaze-status:mac ... -->).
	Name string
	// Command is the command that runs it, named wherever the record says how
	// to make one.
	Command string
	// Dir is the package relative to the examples module, and Package its
	// import path, which test2json events name.
	Dir, Package string
	// Exe is the test binary's file name, without .exe.
	Exe string
	// Source is what decides whether a record still describes the tree,
	// relative to the checkout: git compares it with the recorded commit.
	Source string
	// StatusFile and ShotsDir are where the record and its pictures go,
	// relative to the checkout.
	StatusFile, ShotsDir string
	// ShotsFlag is the binary's flag naming the directory it writes pictures
	// into, or "" when it takes none.
	ShotsFlag string

	// Targets are the sections the file always has, in order, each a
	// placeholder until it is run. With Open, any other target recorded gets
	// a section too, after these, in name order; without it nothing else is
	// recorded.
	Targets []string
	Open    bool

	// Header opens the status file.
	Header string
	// Title is a section's heading, Short its column in the Screenshots
	// table, and Placeholder the body of a target in Targets not run yet.
	Title, Short, Placeholder func(target string) string

	// Known is the list of failures that are already understood (see Known),
	// read at each use so a test can replace it.
	Known func() []Known
	// KnownVerdict is the verdict when every failure is known, KnownTail what
	// follows the names in it, KnownLabel how the table marks one, and
	// XPassTail what an UNEXPECTED PASS tells the reader to do.
	KnownVerdict, KnownTail, KnownLabel, XPassTail string
	// Thing is the suite in "NO: the <thing> did not build", and Subject what
	// a run that never started says nothing about.
	Thing, Subject string
	// ShotsBullet is the section's screenshots line, given how many pictures
	// were taken of how many tried.
	ShotsBullet string
	// GalleryIntro opens the Screenshots section.
	GalleryIntro string

	// Deps reads what the suite builds against that the record names, or is
	// nil when that is nothing but this checkout.
	Deps func(root string) ([]Dep, string, error)
	// Marker is a section's key=value facts, in its opening marker, which
	// Fresh reads back.
	Marker func(Section) []string
	// Fresh returns, after any one-off work, the answer for each recorded
	// section's marker facts: whether it still describes the tree.
	Fresh func(root string) func(rec map[string]string) string
}

// orDefault is s, or Glaze when s is nil: a Section or Options built without
// naming a suite is glaze's, as every one was before there were two.
func (s *Suite) orDefault() *Suite {
	if s == nil {
		return &Glaze
	}
	return s
}

func (s *Suite) openMarker(target string) string  { return "<!-- " + s.Name + "-status:" + target }
func (s *Suite) closeMarker(target string) string { return "<!-- /" + s.Name + "-status:" + target + " -->" }

// markerRE matches a section's opening marker with its key=value pairs. A
// placeholder's marker has none, and is not matched.
func (s *Suite) markerRE() *regexp.Regexp {
	return regexp.MustCompile(`<!-- ` + regexp.QuoteMeta(s.Name) + `-status:([A-Za-z0-9._-]+) ([^>]*?) -->`)
}

// anyMarkerRE matches any section's opening marker, placeholders included.
func (s *Suite) anyMarkerRE() *regexp.Regexp {
	return regexp.MustCompile(`<!-- ` + regexp.QuoteMeta(s.Name) + `-status:([A-Za-z0-9._-]+) `)
}

func (s *Suite) fixed(target string) bool {
	for _, t := range s.Targets {
		if t == target {
			return true
		}
	}
	return false
}

// accepts reports whether target can have a section.
func (s *Suite) accepts(target string) bool {
	return s.fixed(target) || (s.Open && target != "" && !strings.ContainsAny(target, " >/\\"))
}

// order is the targets to write, given the ones recorded: Targets first, then
// any other recorded one in name order.
func (s *Suite) order(have map[string]string) []string {
	out := append([]string{}, s.Targets...)
	var extra []string
	for t := range have {
		if !s.fixed(t) && s.accepts(t) {
			extra = append(extra, t)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// shotsURL is ShotsDir as the status file, in docs/, refers to it.
func (s *Suite) shotsURL() string { return strings.TrimPrefix(s.ShotsDir, "docs/") }

// knownRef is the reference for test on target in s's known list. A Known
// with no Target holds on every target.
func (s *Suite) knownRef(target, test string) string {
	if s.Known == nil {
		return ""
	}
	for _, k := range s.Known() {
		if (k.Target == target || k.Target == "") && k.Test == test {
			return k.Ref
		}
	}
	return ""
}

// sourceChanged asks git whether dir differs from commit, working tree
// included: yes, no, or an error for cannot tell.
func sourceChanged(root, commit, dir string) (bool, error) {
	c := exec.Command("git", "diff", "--quiet", commit, "--", dir)
	c.Dir = root
	err := c.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return true, nil
	default:
		return false, fmt.Errorf("git could not compare %s/ with %s: %v", dir, short(commit), err)
	}
}

// exeName is the binary's file name for goos.
func (s *Suite) exeName(goos string) string {
	if goos == "windows" {
		return s.Exe + ".exe"
	}
	return s.Exe
}
