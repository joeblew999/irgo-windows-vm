package main

// These tests hold the three halves of issue intake to each other: the web
// forms in .github/ISSUE_TEMPLATE, the bodies `report -issue` prints for
// agents, and the labels in .github/labels.tsv. Each is edited by hand, and
// a field renamed in one place only would split issues into two shapes.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// formField is one field of an issue form, as far as these tests need it.
type formField struct {
	typ, label string
	required   bool
}

// issueForm is what readForm takes from an issue form.
type issueForm struct {
	labels []string
	fields []formField
}

// readForm reads an issue form without a YAML parser, which the root module
// does not otherwise need. It relies on the layout the forms keep and their
// header comment states: fields are "  - type:" items, a field's own label is
// at six spaces, and its "required:" at six spaces under "validations:".
// Checkbox options are deeper ("        - label:"), so they are not fields.
func readForm(t *testing.T, path string) issueForm {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f issueForm
	inValidations := false
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "labels:"):
			for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(l, -1) {
				f.labels = append(f.labels, m[1])
			}
		case strings.HasPrefix(l, "  - type: "):
			f.fields = append(f.fields, formField{typ: strings.TrimPrefix(l, "  - type: ")})
			inValidations = false
		case len(f.fields) == 0:
		case strings.HasPrefix(l, "      label: "):
			f.fields[len(f.fields)-1].label = strings.TrimPrefix(l, "      label: ")
		case strings.HasPrefix(l, "    validations:"):
			inValidations = true
		case inValidations && l == "      required: true":
			f.fields[len(f.fields)-1].required = true
		}
	}
	if len(f.fields) < 3 || len(f.labels) == 0 {
		t.Fatalf("%s: read %d fields and %d labels; the layout changed and these tests would pass vacuously", path, len(f.fields), len(f.labels))
	}
	return f
}

func formPath(t *testing.T, name string) string {
	return filepath.Join(repoRoot(t), ".github", "ISSUE_TEMPLATE", name)
}

// TestIssueBodiesMatchTheForms: the body `report -issue <kind>` prints has a
// "### <label>" heading for every field of its form, in the form's order, and
// no heading the form lacks. That is the shape GitHub gives a form's answers,
// so an agent's issue and a web issue read alike.
//
// Negative control: renaming the bug form's "Exact command" label, or
// swapping two of its fields, fails this; so does deleting the Checks section
// from issueKinds.
func TestIssueBodiesMatchTheForms(t *testing.T) {
	seen := map[string]bool{}
	for name, k := range issueKinds {
		seen[k.form] = true
		f := readForm(t, formPath(t, k.form))
		var want []string
		for _, fl := range f.fields {
			if fl.typ != "markdown" {
				want = append(want, fl.label)
			}
		}
		var got []string
		for _, l := range strings.Split(issueBody(name, k, "REPORT"), "\n") {
			if h, ok := strings.CutPrefix(l, "### "); ok {
				got = append(got, h)
			}
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("report -issue %s headings do not match %s:\n  body: %q\n  form: %q", name, k.form, got, want)
		}
		if k.withReport && !strings.Contains(issueBody(name, k, "REPORT"), "\nREPORT\n") {
			t.Errorf("report -issue %s does not carry the report", name)
		}
	}
	// Every form has a kind: a form an agent cannot follow is one only people
	// can file.
	entries, err := os.ReadDir(filepath.Join(repoRoot(t), ".github", "ISSUE_TEMPLATE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.yml" && !seen[e.Name()] {
			t.Errorf(".github/ISSUE_TEMPLATE/%s has no report -issue kind", e.Name())
		}
	}
}

// TestBugFormsRequireTheReport: a bug or upstream issue without the report
// cannot be triaged, so the form must not let it be submitted, and the body
// for agents must include it.
//
// Negative control: setting the bug form's report field to required: false
// fails this.
func TestBugFormsRequireTheReport(t *testing.T) {
	for _, name := range []string{"bug", "upstream"} {
		k := issueKinds[name]
		if !k.withReport {
			t.Errorf("report -issue %s does not include the report", name)
		}
		required := false
		for _, fl := range readForm(t, formPath(t, k.form)).fields {
			if fl.label == reportSection.heading {
				required = fl.required
			}
		}
		if !required {
			t.Errorf("%s does not require the %q field", k.form, reportSection.heading)
		}
	}
}

// labelsFile reads .github/labels.tsv: name, tab, colour, tab, description.
func labelsFile(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "labels.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	colour := regexp.MustCompile(`^[0-9a-f]{6}$`)
	out := map[string]bool{}
	for i, l := range strings.Split(string(b), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.Split(l, "\t")
		switch {
		case len(parts) != 3:
			t.Errorf("labels.tsv line %d: want name, colour and description separated by tabs: %q", i+1, l)
		case !colour.MatchString(parts[1]):
			t.Errorf("labels.tsv line %d: colour %q is not six lowercase hex digits", i+1, parts[1])
		case len(parts[2]) > 100:
			t.Errorf("labels.tsv line %d: GitHub refuses descriptions over 100 characters", i+1)
		default:
			out[parts[0]] = true
		}
	}
	if len(out) < 5 {
		t.Fatalf("read %d labels from labels.tsv; it moved or lost its tabs", len(out))
	}
	return out
}

// ghLabel matches the labels of a gh command line in the docs: --label a,b.
var ghLabel = regexp.MustCompile(`--label[ =]"?([a-z0-9 ,-]+[a-z0-9])"?`)

// TestEveryLabelNamedIsDefined: a label a form applies, `report -issue`
// suggests or the docs tell an agent to pass must be in labels.tsv, or the
// sync never creates it and gh refuses the issue.
//
// Negative control: removing needs-triage from labels.tsv fails this for all
// three forms; misspelling agent-filed in FOR-AGENTS.md fails it for the
// docs.
func TestEveryLabelNamedIsDefined(t *testing.T) {
	defined := labelsFile(t)
	named := map[string][]string{}
	for name, k := range issueKinds {
		for _, l := range readForm(t, formPath(t, k.form)).labels {
			named[l] = append(named[l], k.form)
		}
		for _, l := range k.labels {
			named[l] = append(named[l], "report -issue "+name)
		}
	}
	contributing, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "FOR-AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, m := range ghLabel.FindAllStringSubmatch(string(contributing), -1) {
		for _, l := range strings.Split(m[1], ",") {
			named[strings.TrimSpace(l)] = append(named[strings.TrimSpace(l)], "docs/FOR-AGENTS.md")
			found++
		}
	}
	if found == 0 {
		t.Error("docs/FOR-AGENTS.md shows no gh --label; the agent instructions moved and this half of the test is vacuous")
	}
	var missing []string
	for l, where := range named {
		if !defined[l] {
			missing = append(missing, l+" (named by "+strings.Join(where, ", ")+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("label %s is not in .github/labels.tsv", m)
	}
}
