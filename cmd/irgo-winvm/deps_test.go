package main

// What reaches the binary a user downloads.
//
// This repository exists to find bugs in glaze and native by running them on
// real Windows. The tool that does the running must not link them: it would
// mean the thing under test is part of the instrument, and a glaze bug could
// break the tool that was supposed to report it.
//
// Nothing enforced that. The module split is what keeps them out — probe,
// glaze-probes and examples are separate modules for this reason — and a split
// is one `import` away from being undone, in a change that builds and passes
// every other check.

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// modulePath reduces a package path to the module that provides it:
// github.com/pierrec/lz4/v4/internal/lz4block -> github.com/pierrec/lz4.
//
// The licence table in CONTRIBUTING.md is written in modules, so this counts the
// same unit rather than a number nothing else uses.
var modulePath = regexp.MustCompile(`^(github\.com/[^/]+/[^/]+|golang\.org/x/[^/]+)`)

// forbidden are the libraries under test. They belong in the guest programs,
// which are separate modules, and nowhere near the host tool.
var forbidden = []string{
	"github.com/crgimenes/glaze",
	"github.com/crgimenes/native",
}

// TestShippedBinaryLinksNothingUnderTest is the guard that makes the module
// split mean something.
//
// Negative control, run by hand: add github.com/google/uuid to forbidden — a
// module the binary really does link — and this fails naming the package. That
// exercises the detector without fetching glaze, which the root module does not
// require and should never be made to.
//
// Importing glaze for real would fail earlier, at go.mod, which is the outer
// guard this one sits behind: it catches the case where somebody adds the
// require line to make the import work.
func TestShippedBinaryLinksNothingUnderTest(t *testing.T) {
	// Run from the repository root: the test's own directory is cmd/irgo-winvm,
	// where the pattern ./cmd/irgo-winvm resolves to nothing. The first version
	// of this failed that way and looked like a real finding.
	list := exec.Command("go", "list", "-deps", "./cmd/irgo-winvm")
	list.Dir = repoRoot(t)
	var stderr strings.Builder
	list.Stderr = &stderr
	out, err := list.Output()
	if err != nil {
		t.Fatalf("listing the binary's dependencies: %v: %s", err, stderr.String())
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) == 0 {
		t.Fatal("go list returned nothing; this test would pass vacuously")
	}

	mods := map[string]bool{}
	for _, p := range pkgs {
		for _, bad := range forbidden {
			if strings.HasPrefix(p, bad) {
				t.Errorf("%s reaches the published binary — it is one of the libraries "+
					"this repository exists to test, and the tool must not link it", p)
			}
		}
		if strings.Contains(p, "joeblew999") {
			continue
		}
		if m := modulePath.FindString(p); m != "" {
			mods[m] = true
		}
	}

	// Reported, not asserted against a hardcoded number. A count in a test is a
	// second copy of the licence table, and it would be updated by whoever added
	// the dependency — which is the person least likely to re-check the licence.
	var names []string
	for m := range mods {
		names = append(names, m)
	}
	sort.Strings(names)
	t.Logf("%d third-party modules reach the binary: %s", len(names), strings.Join(names, " "))
	t.Log("if that number changed, CONTRIBUTING.md's licence table needs re-checking")
}
