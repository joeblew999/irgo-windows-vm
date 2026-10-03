package main

// The shipped binary must not link glaze or native, the libraries under test:
// a glaze bug could then break the tool meant to report it. The module split
// keeps them out, and this test keeps the split from being undone by one
// import.

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// modulePath reduces a package path to the module that provides it:
// github.com/pierrec/lz4/v4/internal/lz4block -> github.com/pierrec/lz4, the
// unit docs/contributing.md's licence table is written in.
var modulePath = regexp.MustCompile(`^(github\.com/[^/]+/[^/]+|golang\.org/x/[^/]+)`)

// forbidden are the libraries under test. They belong in the guest programs,
// which are separate modules, and nowhere near the host tool.
var forbidden = []string{
	"github.com/crgimenes/glaze",
	"github.com/crgimenes/native",
}

// TestShippedBinaryLinksNothingUnderTest catches a require line added to
// go.mod to make a glaze import build.
//
// Negative control: add github.com/google/uuid, which the binary does link, to
// forbidden, and this fails naming the package.
func TestShippedBinaryLinksNothingUnderTest(t *testing.T) {
	// From the repository root: in cmd/irgo-winvm the pattern matches nothing.
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

	// Logged, not asserted: a hard-coded count would be a second copy of the
	// licence table, bumped by whoever added the dependency without a re-check.
	var names []string
	for m := range mods {
		names = append(names, m)
	}
	sort.Strings(names)
	t.Logf("%d third-party modules reach the binary: %s", len(names), strings.Join(names, " "))
	t.Log("if that number changed, docs/contributing.md's licence table needs re-checking")
}
