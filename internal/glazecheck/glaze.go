package glazecheck

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Glaze is the glaze and native conformance suite, examples/conformance,
// recorded in docs/GLAZE-STATUS.md with a section for the Mac and one for
// Windows.
var Glaze = Suite{
	Name:       "glaze",
	Command:    "glaze-check",
	Dir:        SuiteDir,
	Package:    SuitePackage,
	Exe:        "conformance.test",
	Source:     "examples",
	StatusFile: StatusFile,
	ShotsDir:   ShotsDir,
	ShotsFlag:  ShotsFlag,
	Targets:    targets,
	Header:     header,
	Title:      title,
	Short:      titleShort,
	Placeholder: func(target string) string {
		return fmt.Sprintf("Run `irgo-winvm glaze-check%s`.", map[string]string{TargetMac: "", TargetWindows: " -windows"}[target])
	},
	Known:        func() []Known { return KnownUpstream },
	KnownVerdict: "KNOWN BUGS ONLY",
	KnownTail:    " fail, known upstream bugs listed in docs/reference/upstream.md; nothing else did",
	KnownLabel:   "known upstream",
	XPassTail:    " — if the fix is released, remove it from glazecheck.KnownUpstream and update docs/reference/upstream.md",
	Thing:        "conformance suite",
	Subject:      "glaze",
	ShotsBullet:  "- screenshots: %d of the %d tests that open a window took one — see [Screenshots](#screenshots)\n",
	GalleryIntro: "Every test that opens a window photographs it at the moment that shows what it checked — the page loaded, the tray up, " +
		"the menu installed, the dialog open — and the picture is recorded with the run it came from. A capture that failed says why " +
		"instead of showing a picture; a black or one-colour frame counts as failed. How each is taken is in " +
		"`examples/conformance/shots_test.go`.\n\n",
	Deps: ReadDeps,
	Marker: func(s Section) []string {
		fields := []string{
			"commit=" + s.Tree.Commit,
			fmt.Sprintf("examples-dirty=%t", s.Tree.ExamplesDirty),
		}
		for _, d := range s.Deps {
			fields = append(fields, filepath.Base(d.Path)+"="+d.Key())
		}
		return fields
	},
	Fresh: func(root string) func(map[string]string) string {
		deps, _, depErr := ReadDeps(root)
		now := map[string]string{}
		for _, d := range deps {
			now[filepath.Base(d.Path)] = d.Key()
		}
		return func(rec map[string]string) string { return fresh(root, rec, now, depErr) }
	},
}

func title(target string) string {
	if target == TargetMac {
		return "On the Mac"
	}
	return "On Windows"
}

func titleShort(target string) string {
	if target == TargetMac {
		return "Mac"
	}
	return "Windows"
}

// The header opens with the front matter every page in docs/ has
// (docs/writing.md): the file is a page under Reference.
const header = `---
title: Glaze status
nav_order: 3
parent: Reference
---

# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by ` + "`irgo-winvm glaze-check`" + ` (` + "`mise run glaze:mac`" + ` and
` + "`mise run glaze:windows`" + `) and read back by ` + "`irgo-winvm glaze-status`" + `,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. Every row is one test of
` + "`examples/conformance`" + `, from its test2json events; what each checks is in its
comment, and how the suite runs is in [Testing](guides/testing.md#does-glaze-work).

`

// fresh is whether one glaze section still describes the tree.
//
// "Still describes" means the two inputs that decide the result are unchanged —
// examples/ (compared by git against the recorded commit, working tree
// included) and the glaze and native the examples resolve to now. It does not
// compare the whole commit, because committing the status file is itself a new
// commit, and a check that called every verdict stale the moment it was
// committed would be one nobody reads. It also does not see a change to the
// VM, or to app-create itself — say so rather than claim more.
func fresh(root string, rec, now map[string]string, depErr error) string {
	commit := rec["commit"]
	if commit == "" {
		return "CANNOT TELL — the section names no commit"
	}
	if rec["examples-dirty"] == "true" {
		return "CANNOT TELL — it was recorded with uncommitted changes in examples/, which git cannot compare against"
	}
	var why []string
	changed, err := sourceChanged(root, commit, "examples")
	if err != nil {
		return "CANNOT TELL — " + err.Error()
	}
	if changed {
		why = append(why, "examples/ has changed since "+short(commit))
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
