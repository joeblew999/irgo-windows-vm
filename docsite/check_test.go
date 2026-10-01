package docsite

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These build small projects in temporary directories and run the real Build
// and Check over them: the failures worth catching are where each piece is
// right and the whole is not, which a unit test of one renderer cannot see.

// TestMain makes the test binary its own hook: run with DOCSITE_TEST_HOOK set,
// it prints the page docsite asked for (DOCSITE_PAGE) and exits.
func TestMain(m *testing.M) {
	if os.Getenv("DOCSITE_TEST_HOOK") == "1" {
		switch os.Getenv("DOCSITE_PAGE") {
		case "gen.html":
			fmt.Print("# Generated\n\nMade by a hook.\n\n## One\n\n## Two\n\n## Three\n\nSee [A](a.html#a-section).\n")
		case "a.html":
			fmt.Print("<script>/* foot */</script>\n")
		case "fail.html":
			fmt.Fprintln(os.Stderr, "the hook's own complaint")
			os.Exit(3)
		case "empty.html":
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// project writes files into a new directory and returns it.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// minimal is the reuse case: a README and two pages, no config.
func minimal() map[string]string {
	return map[string]string{
		"README.md": "# Widget\n\nWidget turns sprockets into widgets. It is small.\n\nRead [A](docs/a.md) and [B's section](docs/b.md#b-section).\n",
		"docs/a.md": "# Alpha guide\n\nHow to use alpha.\n\n## A section\n\n| x | y |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n\nBack [home](../README.md).\n",
		"docs/b.md": "# Beta guide\n\nAbout beta.\n\n## B section\n\n> [!NOTE]\n> A note.\n\nSee [alpha](a.md#a-section).\n",
	}
}

// buildAndCheck loads dir (with its docsite.toml, if any), builds and checks.
func buildAndCheck(t *testing.T, dir string) (*Site, *Report) {
	t.Helper()
	s, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.Out = filepath.Join(t.TempDir(), "out")
	if err := Build(s, Options{SHA: "testsha"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	r, err := Check(s)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return s, r
}

func wantProblem(t *testing.T, r *Report, substr string) {
	t.Helper()
	for _, p := range r.Problems {
		if strings.Contains(p, substr) {
			return
		}
	}
	t.Errorf("no problem mentioning %q; problems: %q", substr, r.Problems)
}

// TestMinimalProjectNeedsNoConfig: README.md and docs/*.md, discovered, with
// titles and nav from their H1s, build and pass every check.
func TestMinimalProjectNeedsNoConfig(t *testing.T) {
	s, r := buildAndCheck(t, project(t, minimal()))
	if len(r.Problems) > 0 {
		t.Fatalf("a correct project failed the check: %q", r.Problems)
	}
	var got []string
	for _, p := range s.Pages {
		got = append(got, p.Src+"="+p.Out+"="+p.Title+"="+p.Nav)
	}
	want := []string{"README.md=index.html=Widget=", "docs/a.md=a.html=Alpha guide=Alpha guide", "docs/b.md=b.html=Beta guide=Beta guide"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("pages:\n  got  %q\n  want %q", got, want)
	}
	if s.Name != "Widget" {
		t.Errorf("site name %q, want the home page's title", s.Name)
	}
	if s.Pages[1].Blurb != "How to use alpha." {
		t.Errorf("blurb %q, want the first sentence", s.Pages[1].Blurb)
	}
	index := read(t, filepath.Join(s.Out, "index.html"))
	for _, want := range []string{`href="a.html" title="How to use alpha.">Alpha guide</a>`, `<title>Widget</title>`, "Generated from README.md in the repository"} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html does not contain %q", want)
		}
	}
	// No repository and no base URL: nothing may point at a guess.
	if strings.Contains(index, "github.com") {
		t.Error("index.html links GitHub though the project has no repository URL")
	}
	if _, err := os.Stat(filepath.Join(s.Out, sitemapFile)); err == nil {
		t.Error("a sitemap was written without a base URL; its URLs must be absolute")
	}
	llms := read(t, filepath.Join(s.Out, corpusIndex))
	if !strings.Contains(llms, "> Widget turns sprockets into widgets. It is small.") {
		t.Errorf("llms.txt has no summary from the README:\n%s", llms)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The negative controls the plan asks for, as tests: each breaks one thing in
// the minimal project and the check must name it.

func TestCheckCatchesABrokenLink(t *testing.T) {
	files := minimal()
	files["docs/b.md"] += "\nAnd [gone](missing.md) and [also gone](c.html).\n"
	_, r := buildAndCheck(t, project(t, files))
	wantProblem(t, r, "b.html links to missing.md")
	wantProblem(t, r, "b.html links to c.html")
}

func TestCheckCatchesABrokenAnchor(t *testing.T) {
	files := minimal()
	files["docs/b.md"] += "\nSee [nowhere](a.md#no-such-section).\n"
	_, r := buildAndCheck(t, project(t, files))
	wantProblem(t, r, "b.html links to a.html#no-such-section")
}

func TestCheckCatchesAnUnexplainedScreenshotAndAMissingImage(t *testing.T) {
	files := minimal()
	files["docs/screens/shown.png"] = "png"
	files["docs/screens/vm/orphan.png"] = "png"
	files["docs/a.md"] += "\n![shown](screens/shown.png) ![gone](screens/gone.png)\n"
	_, r := buildAndCheck(t, project(t, files))
	wantProblem(t, r, "screens/vm/orphan.png is published and no page mentions it")
	wantProblem(t, r, "a.html shows an image at screens/gone.png")
	wantProblem(t, r, "a.md shows an image at screens/gone.png")
	for _, p := range r.Problems {
		if strings.Contains(p, "shown.png") {
			t.Errorf("a referenced, published screenshot was reported: %s", p)
		}
	}
}

// TestCheckLinksFromSource: links into the site by its URL, in files that are
// not pages, must resolve, including a prefix configured for code that builds
// the URL from a constant.
func TestCheckLinksFromSource(t *testing.T) {
	files := minimal()
	files["docsite.toml"] = `base = "https://example.test/widget/"
[check]
sources = ["src/**/*.{go,txt}", "README.md"]
prefixes = ['SiteURL\s*\+\s*"']
`
	files["src/deep/x.go"] = `package x
const a = "https://example.test/widget/a.html#a-section"
const b = "https://example.test/widget/a.html#renamed"
var c = SiteURL + "gone.html"
`
	files["src/y.txt"] = "https://example.test/widget/b.html#b-section\n"
	files["other/z.go"] = `"https://example.test/widget/never-scanned.html"`
	s, r := buildAndCheck(t, project(t, files))
	wantProblem(t, r, "src/deep/x.go links to a.html#renamed")
	wantProblem(t, r, "src/deep/x.go links to gone.html")
	if len(r.Problems) != 2 {
		t.Errorf("want exactly the two broken links, got %q", r.Problems)
	}
	// With a base, the sitemap exists and is checked.
	if _, err := os.Stat(filepath.Join(s.Out, sitemapFile)); err != nil {
		t.Error("no sitemap despite a base URL")
	}
}

// TestHooks: a generated page and a foot from a command, run in the project
// with DOCSITE_PAGE set; the generated page is in every rendering, links
// like any other, and its footer does not claim a source file.
func TestHooks(t *testing.T) {
	t.Setenv("DOCSITE_TEST_HOOK", "1")
	bin := strconv.Quote(os.Args[0])
	files := minimal()
	files["docsite.toml"] = `name = "Widget docs"
base = "https://example.test/w/"
repo = "https://github.com/example/widget"

[[page]]
src = "README.md"

[[page]]
src = "docs/a.md"
nav = "Alpha"
foot = { run = [` + bin + `], format = "html" }

[[page]]
src = "docs/b.md"
parent = "a.html"
intent = true

[[page]]
out = "gen.html"
footer = "Captured from the widget binary."
generate = { run = [` + bin + `] }
`
	files["LICENSE"] = "MIT"
	s, r := buildAndCheck(t, project(t, files))
	if len(r.Problems) > 0 {
		t.Fatalf("problems: %q", r.Problems)
	}
	gen := read(t, filepath.Join(s.Out, "gen.html"))
	for _, want := range []string{"Made by a hook.", "Captured from the widget binary.", `<title>Generated — Widget docs</title>`} {
		if !strings.Contains(gen, want) {
			t.Errorf("gen.html does not contain %q", want)
		}
	}
	a := read(t, filepath.Join(s.Out, "a.html"))
	if !strings.Contains(a, "<script>/* foot */</script>\n\n</body>") {
		t.Error("a.html does not end with its foot hook's HTML")
	}
	if strings.Contains(read(t, filepath.Join(s.Out, "b.html")), "/* foot */") {
		t.Error("a foot hook leaked onto another page")
	}
	// b is under a: not in the header, and a's entry lights up on b.
	b := read(t, filepath.Join(s.Out, "b.html"))
	header := headerBlock.FindString(b)
	if strings.Contains(header, `href="b.html"`) || !strings.Contains(header, `href="a.html" title="How to use alpha." class="current" aria-current="true">Alpha</a>`) {
		t.Errorf("b.html's header:\n%s", header)
	}
	if !strings.Contains(a, `/blob/main/LICENSE">License</a>`) || !strings.Contains(a, `/releases">Releases</a>`) {
		t.Error("the footer lacks the licence or releases link for a GitHub repo with a LICENSE")
	}
	if got := strings.Join(sourcesOf(s, true), ","); got != "docs/b.md" {
		t.Errorf("intent pages: %s", got)
	}
}

func sourcesOf(s *Site, intent bool) []string {
	var out []string
	for _, p := range s.Pages {
		if p.Intent == intent {
			out = append(out, p.Src)
		}
	}
	return out
}

// TestCheckCatchesAFooterLie: a generated page whose footer sends the reader
// to a markdown file, which does not exist. The page's own text may say
// "generated from"; only the footer counts.
func TestCheckCatchesAFooterLie(t *testing.T) {
	t.Setenv("DOCSITE_TEST_HOOK", "1")
	files := minimal()
	files["docsite.toml"] = "[[page]]\nsrc = \"README.md\"\n[[page]]\nsrc = \"docs/a.md\"\n" +
		"[[page]]\nout = \"gen.html\"\nfooter = \"Generated from docs/gen.md in the repository.\"\ngenerate = { run = [" + strconv.Quote(os.Args[0]) + "] }\n"
	files["README.md"] = "# Widget\n\nGenerated from nothing, in its own words.\n"
	_, r := buildAndCheck(t, project(t, files))
	wantProblem(t, r, "gen.html is generated, but its footer claims it came from a file")
	if len(r.Problems) != 1 {
		t.Errorf("want only the footer problem, got %q", r.Problems)
	}
}

// TestHookFailures: a hook that fails or prints nothing fails the build,
// naming the page and the hook's own message.
func TestHookFailures(t *testing.T) {
	t.Setenv("DOCSITE_TEST_HOOK", "1")
	bin := strconv.Quote(os.Args[0])
	for page, want := range map[string]string{
		"fail.html":  "the hook's own complaint",
		"empty.html": "produced nothing",
	} {
		files := minimal()
		files["docsite.toml"] = "[[page]]\nsrc = \"README.md\"\n\n[[page]]\nout = \"" + page + "\"\ngenerate = { run = [" + bin + "] }\n"
		s, err := Load("", project(t, files))
		if err != nil {
			t.Fatal(err)
		}
		s.Out = t.TempDir()
		err = Build(s, Options{})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), page) {
			t.Errorf("%s: got %v, want an error naming the page and %q", page, err, want)
		}
	}
}

// TestOpenAPIHook renders an OpenAPI file as a page whose links to its own
// sections resolve.
func TestOpenAPIHook(t *testing.T) {
	files := minimal()
	spec, err := os.ReadFile(filepath.Join("testdata", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	files["api/openapi.json"] = string(spec)
	files["README.md"] = "# Pets\n\nThe [API](api.html#get-petsid).\n"
	files["docsite.toml"] = "[[page]]\nsrc = \"README.md\"\n\n[[page]]\nout = \"api.html\"\ngenerate = { file = \"api/openapi.json\", format = \"openapi\" }\n"
	s, r := buildAndCheck(t, project(t, files))
	if len(r.Problems) > 0 {
		t.Fatalf("problems: %q", r.Problems)
	}
	md := read(t, filepath.Join(s.Out, "api.md"))
	for _, want := range []string{
		"# Pet store",
		"| [`GET /pets/{id}`](#get-petsid) | fetch one pet | `bearer` |",
		"- **`id`** (path, string, required): the pet's id",
		"- **Answers:** 200 OK (`application/json`, `Pet`); 404 no such pet",
		"| `name` | string | yes | what it answers to |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("api.md does not contain %q:\n%s", want, md)
		}
	}
	if s.Pages[1].Title != "Pet store" {
		t.Errorf("title %q, want the document's", s.Pages[1].Title)
	}
}

// TestConfigErrors: a config that cannot mean what it says is refused, naming
// the problem, rather than building something else.
func TestConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ toml, want string }{
		"unknown key":       {"titel = \"x\"\n", "unknown keys: titel"},
		"src and generate":  {"[[page]]\nsrc = \"README.md\"\ngenerate = { run = [\"x\"] }\n", "both src and generate"},
		"neither":           {"[[page]]\ntitle = \"x\"\n", "needs src or generate"},
		"parent not header": {"[[page]]\nsrc = \"README.md\"\n[[page]]\nsrc = \"docs/a.md\"\nparent = \"index.html\"\n", "not a page with a header entry"},
		"duplicate out":     {"[[page]]\nsrc = \"docs/a.md\"\n[[page]]\nsrc = \"docs/b.md\"\nout = \"a.html\"\n", "both published as a.html"},
		"bad format":        {"[[page]]\nout = \"g.html\"\ngenerate = { run = [\"x\"], format = \"yaml\" }\n", `format "yaml"`},
		"out with a dir":    {"[[page]]\nsrc = \"docs/a.md\"\nout = \"sub/a.html\"\n", "no directory"},
	} {
		t.Run(name, func(t *testing.T) {
			files := minimal()
			files["docsite.toml"] = tc.toml
			_, err := Load("", project(t, files))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestGlob(t *testing.T) {
	dir := project(t, map[string]string{
		"README.md": "", "cmd/a.go": "", "cmd/x/b.go": "", "cmd/x/c.md": "", "cmd/x/d.txt": "", "docs/e.md": "",
	})
	got, err := globFiles(dir, []string{"cmd/**/*.{go,md}", "README.md", "missing/**/*.go", "nothing.md"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "README.md cmd/a.go cmd/x/b.go cmd/x/c.md"; strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPagesURL(t *testing.T) {
	for repo, want := range map[string]string{
		"https://github.com/JoeBlew999/irgo-windows-vm": "https://joeblew999.github.io/irgo-windows-vm/",
		"https://gitlab.com/a/b":                        "",
		"":                                              "",
	} {
		if got := pagesURL(repo); got != want {
			t.Errorf("pagesURL(%q) = %q, want %q", repo, got, want)
		}
	}
}
