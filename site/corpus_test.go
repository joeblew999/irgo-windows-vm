package main

import (
	"os"
	"path/filepath"
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
