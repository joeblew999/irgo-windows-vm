package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The documentation and the binary have to name the same commands, and this
// checks it in both directions.
//
// It exists because they did not. RESULTS.md told readers to run
// `irgo-winvm build-iso` and `irgo-winvm setup` — neither of which this binary
// has ever had — on a site that rendered perfectly and a build that stayed
// green. Nothing could have caught that, because nothing was looking.
//
// A test rather than a shell step in the workflow: it runs under `go test`, so
// `mise run go:check` and CI and a developer on a laptop all get the same
// answer, and it can be given a negative control.

// docCommand matches a command named after the tool in prose: `irgo-winvm foo`.
var docCommand = regexp.MustCompile(`irgo-winvm ([a-z][a-z-]*)`)

// notCommands are words that follow "irgo-winvm" without naming a command.
//
// Kept as small as possible and each one justified, because every entry here is
// a hole in the check.
var notCommands = map[string]bool{
	// `go list -deps ./cmd/irgo-winvm` — a package path, not an invocation.
	"cmd": true,
	// Release artefacts: irgo-winvm-darwin-arm64. The regexp stops at the
	// hyphen, so these arrive as "darwin".
	"darwin": true,
}

// repoRoot is two directories up from cmd/irgo-winvm.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot find the repository root from the test's directory: %v", err)
	}
	return root
}

// markdownFiles returns every .md file at the repository root.
func markdownFiles(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, rErr := os.ReadFile(filepath.Join(root, e.Name()))
		if rErr != nil {
			t.Fatal(rErr)
		}
		out[e.Name()] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no markdown found at the repository root; this test would pass vacuously")
	}
	return out
}

// TestDocsNameOnlyRealCommands is direction one: everything the documentation
// tells a reader to run must exist.
//
// Negative control, run by hand: renaming `iso-create` to `iso-make` in
// README.md fails this and names the file.
func TestDocsNameOnlyRealCommands(t *testing.T) {
	for name, body := range markdownFiles(t) {
		for _, m := range docCommand.FindAllStringSubmatch(body, -1) {
			word := m[1]
			if notCommands[word] {
				continue
			}
			if _, ok := find(word); !ok {
				t.Errorf("%s names `irgo-winvm %s`, which is not a command", name, word)
			}
		}
	}
}

// TestEveryCommandIsDocumented is direction two, and the half that is easy to
// leave out.
//
// Without it, a command that quietly loses its documentation stays invisible —
// the check would pass while the site said nothing about it at all. Half the
// drift, undetected.
//
// Negative control, run by hand: deleting every mention of `vm-screen` from the
// markdown fails this.
func TestEveryCommandIsDocumented(t *testing.T) {
	files := markdownFiles(t)
	for _, c := range commands {
		documented := false
		for _, body := range files {
			// Either as an invocation or named in prose — README lists the
			// three steps in a table without the binary's name in front.
			if strings.Contains(body, "irgo-winvm "+c.Name) || strings.Contains(body, "`"+c.Name+"`") {
				documented = true
				break
			}
		}
		if !documented {
			t.Errorf("%s is a command and no markdown file mentions it", c.Name)
		}
	}
}
