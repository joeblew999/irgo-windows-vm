package main

import (
	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The documentation and the binary have to name the same commands, and this
// checks it in both directions.
//
// It exists because they did not. docs/RESULTS.md told readers to run
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

// intentDocs name commands that do not exist yet, on purpose: a roadmap says
// what is next, and the threat model names what an attacker could call. Every
// other page states what is true now, so every other page is checked.
var intentDocs = map[string]bool{
	"docs/ROADMAP.md":      true,
	"docs/THREAT-MODEL.md": true,
}

// markdownFiles returns every .md file at the repository root and in docs/,
// keyed by path from the root, minus intentDocs.
func markdownFiles(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string]string{}
	for _, dir := range []string{".", "docs"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := filepath.ToSlash(filepath.Join(dir, e.Name()))
			if e.IsDir() || !strings.HasSuffix(name, ".md") || intentDocs[name] {
				continue
			}
			b, rErr := os.ReadFile(filepath.Join(root, name))
			if rErr != nil {
				t.Fatal(rErr)
			}
			out[name] = string(b)
		}
	}
	if len(out) < 5 {
		t.Fatalf("found %d markdown files; the docs moved and this test would pass vacuously", len(out))
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

// exitCodeDoc holds the exit-code table. README says what the project is for
// and nothing technical, so the contract lives with the rest of the CLI detail.
const exitCodeDoc = "docs/DEVELOPMENT.md"

// exitCodeHeading opens the section the table is read from. Only that section
// is read: the same file has other tables with bold numbers in them — the VM's
// `| CPUs | **4** |` row matched exitRow and documented code 4 on its own, so
// deleting the real **4** row left this test green.
const exitCodeHeading = "## What it exits with"

// exitCodeSection returns body from exitCodeHeading up to the next level-two
// heading, or "" if the heading is missing.
func exitCodeSection(body string) string {
	i := strings.Index(body, exitCodeHeading+"\n")
	if i < 0 {
		return ""
	}
	rest := body[i+len(exitCodeHeading)+1:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// exitRow matches a row of the exit-code table in the markdown: | **4** | ... |
var exitRow = regexp.MustCompile(`\|\s*\*\*(\d)\*\*\s*\|`)

// TestDocsNameEveryExitCode is the same rule as the command check, applied to
// the other contract this tool publishes.
//
// The codes are declared in package command, the CLI exits with them, the MCP
// server reports them to an agent, and docs/DEVELOPMENT.md explains them to a
// person. Three
// renderings of one list — and the markdown is the one that cannot be checked
// by compiling, so it is the one that goes stale.
//
// It is not hypothetical for this repository: docs/RESULTS.md once told readers to
// run two commands this binary has never had, on a site that rendered perfectly
// and a build that stayed green.
//
// Negative control, run by hand: adding a seventh code to command.Outcomes
// fails this until the table documents it; deleting the **4** row fails it too.
func TestDocsNameEveryExitCode(t *testing.T) {
	body, ok := markdownFiles(t)[exitCodeDoc]
	if !ok {
		t.Fatalf("%s not found", exitCodeDoc)
	}
	section := exitCodeSection(body)
	if section == "" {
		t.Fatalf("%s has no %q section", exitCodeDoc, exitCodeHeading)
	}
	documented := map[string]bool{}
	for _, m := range exitRow.FindAllStringSubmatch(section, -1) {
		documented[m[1]] = true
	}
	if len(documented) == 0 {
		t.Fatalf("%s documents no exit codes; this test would pass vacuously", exitCodeDoc)
	}

	declared := map[string]bool{}
	for _, o := range command.Outcomes {
		declared[strconv.Itoa(int(o.Code))] = true
		if !documented[strconv.Itoa(int(o.Code))] {
			t.Errorf("exit code %d (%s) is declared but %s does not document it — "+
				"a caller cannot act on a code nothing explains", o.Code, o.Name, exitCodeDoc)
		}
	}
	for code := range documented {
		if !declared[code] {
			t.Errorf("%s documents exit code %s, which package command does not declare", exitCodeDoc, code)
		}
	}
}
