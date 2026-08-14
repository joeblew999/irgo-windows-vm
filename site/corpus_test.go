package main

import (
	"encoding/xml"
	"io/fs"
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
	if err := build("..", out, "https://github.com/joeblew999/irgo-windows-vm", "https://example.test/docs/", "testsha"); err != nil {
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
	sitemap := read(t, filepath.Join(out, sitemapFile))

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
			// A published page missing from the sitemap is invisible to
			// anything that discovers a site the standard way.
			if !strings.Contains(sitemap, "<loc>https://example.test/docs/"+p.Out+"</loc>") {
				t.Errorf("%s is in pages but %s does not list it", p.Out, sitemapFile)
			}
			// The plain-text form, for fetchers that fail on rendered pages.
			md := filepath.Join(out, markdownName(p.Out))
			fi, err := os.Stat(md)
			if err != nil {
				t.Errorf("%s is in pages but %s was not written: %v", p.Out, markdownName(p.Out), err)
				return
			}
			if fi.Size() == 0 {
				t.Errorf("%s is empty; an empty page is worse than a missing one because it still serves", markdownName(p.Out))
			}
			if !strings.Contains(sitemap, "<loc>https://example.test/docs/"+markdownName(p.Out)+"</loc>") {
				t.Errorf("%s is published but %s does not list it", markdownName(p.Out), sitemapFile)
			}
		})
	}
}

// TestSitemapListsTheCorpusToo covers the two files that are not pages.
//
// They are the reason the sitemap is worth generating at all: a crawler that
// reads a sitemap and has never heard of the llms.txt convention still finds
// them. Left out, the sitemap would list only what was already discoverable by
// following links.
func TestSitemapListsTheCorpusToo(t *testing.T) {
	sitemap := read(t, filepath.Join(buildToTemp(t), sitemapFile))
	for _, f := range []string{corpusIndex, corpusFull} {
		if !strings.Contains(sitemap, "<loc>https://example.test/docs/"+f+"</loc>") {
			t.Errorf("%s does not list %s", sitemapFile, f)
		}
	}
}

// TestSitemapIsValidXML — a sitemap no parser accepts is worse than none, and
// the failure is invisible from reading it.
func TestSitemapIsValidXML(t *testing.T) {
	raw := read(t, filepath.Join(buildToTemp(t), sitemapFile))
	var doc struct {
		XMLName xml.Name `xml:"urlset"`
		URLs    []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("sitemap does not parse: %v", err)
	}
	// Every page twice — HTML and markdown — plus the two corpus files.
	if want := len(pages)*2 + 2; len(doc.URLs) != want {
		t.Errorf("sitemap has %d urls, want %d (each page as HTML and markdown, plus the two corpus files)",
			len(doc.URLs), want)
	}
	for _, u := range doc.URLs {
		if !strings.HasPrefix(u.Loc, "https://") {
			t.Errorf("sitemap url is not absolute: %q", u.Loc)
		}
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

// collapse turns rendered HTML into one line of text, so an assertion about a
// sentence does not depend on where the template happened to wrap it.
func collapse(html string) string {
	text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(html, " ")
	return strings.Join(strings.Fields(text), " ")
}

// TestFooterNamesTheRealSource is the check that would have caught the footer
// telling five pages the truth and one page a lie.
//
// Every page used to carry "Generated from the markdown in the repository — if
// this page is wrong, the markdown is wrong". On the command reference that is
// false: it has no source file, it is captured from the binary, and the same
// page's own body says so a few paragraphs above the footer. A reader who found
// a wrong default was being sent to edit a file that does not exist.
//
// Nothing caught it because the page rendered, the sentence read well, and it
// was wrong on exactly one page out of six.
//
// Negative control, run by hand: removing the {{if .Source}} branch so every
// page gets the file-backed sentence fails this on reference.html; making every
// page get the binary sentence fails it on the other five.
func TestFooterNamesTheRealSource(t *testing.T) {
	out := buildToTemp(t)
	const capturedClaim = "Captured from the compiled binary at build time"

	for _, p := range pages {
		t.Run(p.Out, func(t *testing.T) {
			footer := collapse(read(t, filepath.Join(out, p.Out)))

			if p.Src == "" {
				if !strings.Contains(footer, capturedClaim) {
					t.Errorf("%s has no source file, but its footer does not say it was captured from the binary", p.Out)
				}
				// The specific lie: telling a reader of a generated page that
				// markdown is what to fix.
				if strings.Contains(footer, "Generated from") {
					t.Errorf("%s is generated from the binary, but its footer claims it came from a markdown file", p.Out)
				}
				return
			}

			if !strings.Contains(footer, "Generated from "+p.Src) {
				t.Errorf("%s comes from %s, and its footer does not name it", p.Out, p.Src)
			}
			if strings.Contains(footer, capturedClaim) {
				t.Errorf("%s comes from %s, but its footer claims it was captured from the binary", p.Out, p.Src)
			}
		})
	}
}

// TestEveryPublishedScreenshotIsExplained closes the class of failure that
// booting.png was one instance of.
//
// docs/screens is copied wholesale into the site, so anything in it is
// published whether or not a page says what it shows. booting.png was published
// that way for days, captioned by nothing — and deleting it fixed one file, not
// the reason it happened.
//
// It recurs on its own: `vm:shots` publishes the newest shot of every stage, and
// the boot wait is photographed every few seconds, so a slower boot produces
// booting-3, booting-4 and so on. A run during this work produced booting-3
// immediately. The README captions two.
//
// So the rule is that a screenshot in the repository has to be referenced by
// something the site publishes. An unexplained picture in documentation is not
// evidence, it is decoration that looks like evidence.
//
// Negative control, run by hand: copying any curated shot to
// docs/screens/vm/unexplained.png fails this and names it.
func TestEveryPublishedScreenshotIsExplained(t *testing.T) {
	out := buildToTemp(t)

	// Read what the build published, not the source directory, so this measures
	// what a reader can actually reach.
	var shots []string
	err := filepath.WalkDir(filepath.Join(out, "screens"), func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".png") {
			rel, rErr := filepath.Rel(out, p)
			if rErr != nil {
				return rErr
			}
			shots = append(shots, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the published screenshots: %v", err)
	}
	if len(shots) == 0 {
		t.Fatal("no screenshots were published; this test would pass vacuously")
	}

	// One haystack: every page, in both renderings.
	var all strings.Builder
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".html") || strings.HasSuffix(e.Name(), ".md") {
			all.WriteString(read(t, filepath.Join(out, e.Name())))
		}
	}
	haystack := all.String()

	orphans := 0
	for _, s := range shots {
		if !strings.Contains(haystack, s) {
			t.Errorf("%s is published and no page mentions it — caption it, or take it out of docs/screens", s)
			orphans++
		}
	}
	// Counted, because the first version of this line said "all referenced"
	// unconditionally and printed it underneath its own failure.
	t.Logf("%d published screenshots, %d referenced", len(shots), len(shots)-orphans)
}

// Pictures, in both renderings. HTML is <img src="...">; the markdown the site
// serves beside it keeps the ![alt](target) form, and the `!` is what separates
// an image from an ordinary link.
var (
	htmlImage = regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
	mdImage   = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
)

// TestEveryPictureOnAPageExists is the other direction, and it was open.
//
// TestEveryPublishedScreenshotIsExplained stops a picture being published with
// nothing to explain it. Nothing stopped the reverse: a caption naming a file
// that is not there. Deleting a screenshot leaves a broken image on the page and
// every check green — the site's link check greps `href="..."` and an image is
// `src="..."`, so no image on this site has ever been verified to resolve, in
// either rendering.
//
// That is not hypothetical for this repository. Screenshots are published by
// `mise run vm:shots`, which copies the newest shot of each stage; a stage that
// stops happening keeps its last picture, but a stage renamed in the Go code
// lands under the new name and the README goes on pointing at the old one.
//
// Checked against the published tree rather than docs/screens, so it measures
// what a reader's browser would request, and it covers the markdown rendering
// too — those pages are served, and a broken image is just as broken in them.
//
// Negative control, run by hand: `rm docs/screens/vm/ready.png` fails this and
// names screens/vm/ready.png, in both index.html and index.md.
func TestEveryPictureOnAPageExists(t *testing.T) {
	out := buildToTemp(t)
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var re *regexp.Regexp
		switch {
		case strings.HasSuffix(e.Name(), ".html"):
			re = htmlImage
		case strings.HasSuffix(e.Name(), ".md"):
			re = mdImage
		default:
			continue
		}
		body := read(t, filepath.Join(out, e.Name()))
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			ref := m[1]
			// Anything the site does not serve itself is somebody else's to
			// keep working; this is about files that should be in the output.
			if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") ||
				strings.HasPrefix(ref, "data:") {
				continue
			}
			ref, _, _ = strings.Cut(ref, "#")
			checked++
			if _, sErr := os.Stat(filepath.Join(out, filepath.FromSlash(ref))); sErr != nil {
				t.Errorf("%s shows an image at %s and the build published no such file", e.Name(), ref)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no local images were found on any page; this test would pass vacuously")
	}
	t.Logf("%d image references checked across both renderings", checked)
}
