package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These build the real site into a temporary directory and assert on the
// output, rather than testing the renderers in isolation.
//
// That is the point: the failures worth catching are ones where each piece is
// correct and the whole is not — a page that renders as HTML and never reaches
// the corpus, or an anchor that names a heading which was renamed. Neither is
// visible from a unit test of the function that produced it, and neither is
// visible from looking at a page that renders.
//
// No network: the site is built from the tree and from the binary this
// repository compiles.

// buildToTemp runs the real build and returns the output directory.
func buildToTemp(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	if err := build("..", out, "https://github.com/joeblew999/irgo-windows-vm", "https://example.test/docs/"); err != nil {
		t.Fatalf("build: %v", err)
	}
	return out
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Base(path), err)
	}
	return string(b)
}

// TestEveryPageReachesBothRenderings is the divergence gate.
//
// The site and the corpus are produced by one pass over one list, so they
// cannot disagree without somebody editing that loop. This is what notices when
// they do — in both directions, because half a check leaves half the drift
// invisible.
//
// It asserts against `pages` rather than against a list written here. A list in
// a test is a second list, and this whole exercise is about not having one.
//
// Negative controls, run by hand when this was written:
//
//   - skipping a page in the corpus append (`continue` before the append in
//     build) fails this with that page named;
//   - skipping the HTML write for a page fails it too, on the file check.
func TestEveryPageReachesBothRenderings(t *testing.T) {
	out := buildToTemp(t)
	corpus := read(t, filepath.Join(out, corpusFull))
	index := read(t, filepath.Join(out, corpusIndex))

	if len(pages) == 0 {
		t.Fatal("no pages declared; this test would pass vacuously")
	}

	for _, p := range pages {
		t.Run(p.Out, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(out, p.Out)); err != nil {
				t.Errorf("%s is in pages but the site did not publish it: %v", p.Out, err)
			}
			// The corpus delimiter names the page's URL, not its title: two
			// pages could share a title, they cannot share an output file.
			if !strings.Contains(corpus, "Source: https://example.test/docs/"+p.Out) {
				t.Errorf("%s is in pages but %s does not contain it", p.Out, corpusFull)
			}
			if !strings.Contains(index, p.Out) {
				t.Errorf("%s is in pages but %s does not link it", p.Out, corpusIndex)
			}
		})
	}
}

// TestCorpusKeepsTheSiteOrder checks the reading order, which is the only thing
// making a concatenated file navigable by a human.
func TestCorpusKeepsTheSiteOrder(t *testing.T) {
	corpus := read(t, filepath.Join(buildToTemp(t), corpusFull))
	at := -1
	for _, p := range pages {
		i := strings.Index(corpus, "Source: https://example.test/docs/"+p.Out)
		if i < 0 {
			t.Fatalf("%s missing from the corpus", p.Out)
		}
		if i < at {
			t.Errorf("%s appears out of order in the corpus", p.Out)
		}
		at = i
	}
}

// TestCorpusCarriesTheCapturedReference is specifically about the one page with
// no source file.
//
// It would be easy to make the corpus from the markdown files on disk, which
// would be simpler, would build, and would silently omit the entire command
// reference — the one page that cannot be recovered by reading the repository.
func TestCorpusCarriesTheCapturedReference(t *testing.T) {
	corpus := read(t, filepath.Join(buildToTemp(t), corpusFull))
	for _, want := range []string{
		"Usage of iso-create:",    // captured from the binary's stderr
		"Usage of app-create:",    // ...for every command that has flags
		"download from Microsoft", // the usage string iso-create computes at runtime
	} {
		if !strings.Contains(corpus, want) {
			t.Errorf("the corpus does not contain %q; the reference was not captured into it", want)
		}
	}
}

// anchorLink matches an href with a fragment, with or without a page in front:
// `#frag` and `page.html#frag`.
var anchorLink = regexp.MustCompile(`href="([a-z0-9._-]*)#([^"]+)"`)

var htmlID = regexp.MustCompile(`id="([^"]+)"`)

// TestEveryAnchorResolves checks fragment links, which nothing checked before.
//
// The CI link check matches `href="[^"#:]*"` — a pattern that excludes every
// href containing a `#`. So `agents.html#utm` and the ten links in UPSTREAM's
// status table have never been verified by anything; they were correct by hand.
//
// Heading text is load-bearing once anything links to it, and renaming a
// heading breaks those links silently — the page still renders, the link still
// looks like a link, and it lands nowhere.
//
// The IDs are read out of the rendered HTML rather than recomputed from the
// heading text. That is deliberate: goldmark deletes punctuation without
// leaving a separator, so `errors.ErrUnsupported` becomes
// `errorserrunsupported`, an em-dash between spaces leaves a double hyphen, and
// collisions get `-1` appended — reference.html already relies on that, with
// `#commands` for the H1 and `#commands-1` for the subcommand. A slug function
// written here would have to reproduce all three or disagree with the published
// HTML, and disagree silently.
//
// Negative control, run by hand: renaming "## UTM" in UPSTREAM.md fails this,
// naming agents.html as the file whose link broke.
func TestEveryAnchorResolves(t *testing.T) {
	out := buildToTemp(t)
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]map[string]bool{}
	var htmlFiles []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		htmlFiles = append(htmlFiles, e.Name())
		body := read(t, filepath.Join(out, e.Name()))
		set := map[string]bool{}
		for _, m := range htmlID.FindAllStringSubmatch(body, -1) {
			set[m[1]] = true
		}
		ids[e.Name()] = set
	}
	if len(htmlFiles) == 0 {
		t.Fatal("no HTML was produced; this test would pass vacuously")
	}

	checked := 0
	for _, name := range htmlFiles {
		body := read(t, filepath.Join(out, name))
		for _, m := range anchorLink.FindAllStringSubmatch(body, -1) {
			page, frag := m[1], m[2]
			if page == "" {
				page = name // a bare #fragment means this same page
			}
			checked++
			target, ok := ids[page]
			if !ok {
				t.Errorf("%s links to %s#%s, and %s is not a page this site publishes", name, page, frag, page)
				continue
			}
			if !target[frag] {
				t.Errorf("%s links to %s#%s, and %s has no heading with that id", name, page, frag, page)
			}
		}
	}
	if checked == 0 {
		t.Error("no fragment links were found at all; the pattern has stopped matching and this test is asleep")
	}
	t.Logf("%d fragment links checked across %d pages", checked, len(htmlFiles))
}
