package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

func renderString(t *testing.T, src string) renderedPage {
	t.Helper()
	out, err := renderMarkdown(newMarkdown(), []byte(src))
	if err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	return out
}

// TestHeadingIDsAreWhatPlainGoldmarkWouldGive is the guarantee the extensions
// were added under: nothing they do may change a heading's ID.
//
// Other pages link to these IDs, and so does anything outside the site that
// linked to a section before the redesign. TestEveryAnchorResolves checks the
// links the site itself contains; this checks the IDs did not move at all, by
// rendering every source page twice — once with the site's full pipeline, once
// with the bare GFM + auto-ID configuration the site used before — and
// requiring the same heading IDs in the same order.
//
// Negative control, run by hand: making splitDatedHeading overwrite every h2
// and h3's id attribute fails this on every source page, naming the first ID
// that moved.
func TestHeadingIDsAreWhatPlainGoldmarkWouldGive(t *testing.T) {
	plain := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	headingID := regexp.MustCompile(`<h[1-6] id="([^"]+)"`)

	checked := 0
	for _, p := range pages {
		if p.Src == "" {
			continue // generated pages need the binary; their IDs come from the same parser
		}
		raw, err := os.ReadFile(filepath.Join("..", p.Src))
		if err != nil {
			t.Fatal(err)
		}
		body := rewriteLinks(raw, repo, p.Src)

		var before bytes.Buffer
		if err := plain.Convert(body, &before); err != nil {
			t.Fatal(err)
		}
		after, err := renderMarkdown(newMarkdown(), body)
		if err != nil {
			t.Fatal(err)
		}

		want := headingID.FindAllStringSubmatch(before.String(), -1)
		got := headingID.FindAllStringSubmatch(string(after.Body), -1)
		if len(want) == 0 {
			t.Errorf("%s: no headings found; this comparison would pass vacuously", p.Src)
		}
		if len(got) != len(want) {
			t.Errorf("%s: %d headings with ids, was %d", p.Src, len(got), len(want))
			continue
		}
		for i := range want {
			checked++
			if got[i][1] != want[i][1] {
				t.Errorf("%s: heading %d's id moved from %q to %q — every link to it is now broken",
					p.Src, i, want[i][1], got[i][1])
				break
			}
		}
	}
	t.Logf("%d heading ids compared", checked)
}

// TestDatedHeadingSplit covers what headingMetaTransformer does and, as much,
// what it must leave alone.
//
// Negative control, run by hand: returning early from splitDatedHeading fails
// the two "split" cases; dropping the " — " from datedTail fails them too,
// with the separator leaking into the label.
func TestDatedHeadingSplit(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		meta     string // the label's visible text, or "" for no label
	}{
		{"date after the dash is split", "## A self-built ISO installs Windows — verified 12 Aug 2026", "verified 12 Aug 2026"},
		{"everything after the last dash is the label", "## Suspend and resume — 400 ms, verified 12 Aug 2026", "400 ms, verified 12 Aug 2026"},
		{"a dash with no date is left alone", "## macOS — verified", ""},
		{"a date with no separator is left alone", "## Measured 12 Aug 2026", ""},
		{"a date in a code span is left alone", "## Run `date — 12 Aug 2026`", ""},
		{"the page title is left alone", "# Results — 12 Aug 2026", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := string(renderString(t, tc.in+"\n").Body)
			m := regexp.MustCompile(`<span class="heading-meta">(.*?)</span></h|<span class="heading-meta">(.*)</span> <a`).FindStringSubmatch(body)
			if tc.meta == "" {
				if strings.Contains(body, "heading-meta") {
					t.Errorf("labelled a heading that should have been left alone:\n%s", body)
				}
				return
			}
			if m == nil {
				t.Fatalf("no label:\n%s", body)
			}
			visible := regexp.MustCompile(`<span class="visually-hidden">.*?</span>`).ReplaceAllString(m[1]+m[2], "")
			if visible != tc.meta {
				t.Errorf("label reads %q, want %q", visible, tc.meta)
			}
			// The separator is still in the text, for screen readers and
			// anyone copying the heading.
			if !strings.Contains(body, "— ") {
				t.Errorf("the separator was dropped from the heading's text:\n%s", body)
			}
		})
	}
}

// TestTOCDropsTheDate: the sidebar entry is the title alone, still linking to
// the full heading's ID.
//
// Negative control, run by hand: removing the date replacement from cleanTOC
// fails this.
func TestTOCDropsTheDate(t *testing.T) {
	out := renderString(t, "# P\n\n## One — verified 12 Aug 2026\n\n## Two\n\n## Three\n")
	toc := string(out.TOC)
	if !strings.Contains(toc, `href="#one--verified-12-aug-2026">One</a>`) {
		t.Errorf("TOC entry should read just \"One\" and link to the full id:\n%s", toc)
	}
}

// TestTOCDoesNotDoubleEscape: "guest's output" was published in the sidebar
// as "guest&rsquo;s output", because the typographer's entity was copied into
// the title and then escaped again.
//
// Negative control, run by hand: removing the html.UnescapeString line from
// cleanTOC fails this with &amp;rsquo; in the output.
func TestTOCDoesNotDoubleEscape(t *testing.T) {
	toc := string(renderString(t, "# P\n\n## The guest's output\n\n## A & B\n\n## Three\n").TOC)
	if strings.Contains(toc, "&amp;rsquo;") || strings.Contains(toc, "&amp;amp;") {
		t.Errorf("an entity was escaped twice:\n%s", toc)
	}
	if !strings.Contains(toc, "The guest’s output") {
		t.Errorf("expected the typographic apostrophe as a character:\n%s", toc)
	}
	if !strings.Contains(toc, "A &amp; B") {
		t.Errorf("a literal ampersand should still be escaped once:\n%s", toc)
	}
}

// TestTOCOnlyWhenWorthIt: a page with fewer than minTOCEntries sections gets
// no sidebar, and the page title is never in it.
//
// Negative control, run by hand: setting minTOCEntries to 0 fails the first
// case; MinDepth(1) fails the second.
func TestTOCOnlyWhenWorthIt(t *testing.T) {
	if toc := renderString(t, "# P\n\n## One\n\n## Two\n").TOC; toc != "" {
		t.Errorf("two sections produced a table of contents:\n%s", toc)
	}
	toc := string(renderString(t, "# Title\n\n## One\n\n## Two\n\n### Two A\n").TOC)
	if toc == "" {
		t.Fatal("three sections produced no table of contents")
	}
	if strings.Contains(toc, "#title") {
		t.Errorf("the page's own h1 is in its table of contents:\n%s", toc)
	}
}

// TestCodeBlocks: a named language is highlighted with classes, never inline
// colours; an unnamed block stays plain text in a <pre>; both are wrapped so
// the page script can give them a copy button.
//
// Negative control, run by hand: removing codeWrapper's "<pre><code>" write
// fails the plain case; dropping chromahtml.WithClasses fails the "no inline
// style" check.
func TestCodeBlocks(t *testing.T) {
	body := string(renderString(t, "```sh\nmise run go:check  # a comment\n```\n\n```\nPASS: JS -> Go\n```\n").Body)

	if !strings.Contains(body, `<div class="codeblock" data-lang="sh"><pre class="chroma">`) {
		t.Errorf("the sh block is not highlighted inside a labelled wrapper:\n%s", body)
	}
	if strings.Contains(body, "style=") {
		t.Errorf("highlighting wrote inline styles, which cannot follow the colour scheme:\n%s", body)
	}
	if !strings.Contains(body, `<div class="codeblock"><pre><code>PASS: JS -&gt; Go`) {
		t.Errorf("the unlabelled block lost its <pre><code>, or was guessed at:\n%s", body)
	}
}

// TestGitHubAlerts: a callout written for GitHub gets the classes the
// stylesheet colours, rather than rendering as a quote that opens with a
// literal "[!WARNING]". No page uses one yet, which is exactly when a broken
// one would go unnoticed.
//
// Negative control, run by hand: removing &alerts.GhAlerts{} from newMarkdown
// fails this.
func TestGitHubAlerts(t *testing.T) {
	body := string(renderString(t, "> [!WARNING]\n> Never call `suspend --save-state`.\n").Body)
	if !strings.Contains(body, `markdown-alert markdown-alert-warning`) || strings.Contains(body, "[!WARNING]") {
		t.Errorf("the alert was not rendered as one:\n%s", body)
	}
}

// TestTypographerLeavesFlagsAlone: curly quotes are fine; turning `--flag` in
// prose into an en dash is not, on a site about a command-line tool.
//
// Negative control, run by hand: removing the EnDash: nil entry fails this.
func TestTypographerLeavesFlagsAlone(t *testing.T) {
	body := string(renderString(t, "Never pass --save-state to it. It \"works\".\n").Body)
	if !strings.Contains(body, "--save-state") {
		t.Errorf("a flag in prose was rewritten:\n%s", body)
	}
	if !strings.Contains(body, "&ldquo;works&rdquo;") {
		t.Errorf("quotes were not made typographic:\n%s", body)
	}
}

// TestSyntaxCSSFollowsTheColourScheme: each style inside its own
// prefers-color-scheme query, and no chroma rule outside both.
//
// An unscoped light style is not overridden by the dark one wherever the dark
// one is silent, and github-dark is silent on punctuation: every brace in a
// dark-mode JSON block came out near-black on near-black.
//
// Negative control, run by hand: removing the light block's @media wrapper
// fails this.
func TestSyntaxCSSFollowsTheColourScheme(t *testing.T) {
	css, err := syntaxCSS()
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	lightAt := strings.Index(s, "@media not all and (prefers-color-scheme: dark) {")
	darkAt := strings.Index(s, "@media (prefers-color-scheme: dark) {")
	if lightAt < 0 || darkAt < 0 || darkAt < lightAt {
		t.Fatalf("expected a light block then a dark block (light at %d, dark at %d)", lightAt, darkAt)
	}
	light, dark := s[lightAt:darkAt], s[darkAt:]
	if !strings.Contains(light, ".chroma") || !strings.Contains(dark, ".chroma") {
		t.Error("expected chroma rules inside both blocks")
	}
	if strings.Contains(s[:lightAt], ".chroma") {
		t.Error("chroma rules outside both colour-scheme blocks apply in both schemes")
	}
	if !strings.HasSuffix(strings.TrimSpace(light), "}") {
		t.Error("the light block is not closed before the dark one starts")
	}
}

var (
	anyTable     = regexp.MustCompile(`<table>`)
	wrappedTable = regexp.MustCompile(`<div class="table-scroll"[^>]*>\s*<table>`)
)

// TestEveryTableScrollsInItsOwnBox: on a phone a five-column table must
// scroll inside the article, never widen the page, and that needs the wrapper
// on every table rather than most.
//
// Negative control, run by hand: removing tableScroller from decorations fails
// this on every page with a table.
func TestEveryTableScrollsInItsOwnBox(t *testing.T) {
	out := buildToTemp(t)
	total := 0
	for _, p := range pages {
		body := read(t, filepath.Join(out, p.Out))
		n, w := len(anyTable.FindAllString(body, -1)), len(wrappedTable.FindAllString(body, -1))
		total += n
		if n != w {
			t.Errorf("%s: %d tables, %d of them in a scroll box", p.Out, n, w)
		}
	}
	if total == 0 {
		t.Fatal("no tables on any page; this test would pass vacuously")
	}
	t.Logf("%d tables, all wrapped", total)
}

// TestNoDuplicateIDs: an id that appears twice on a page makes every link to
// it land on whichever comes first. The template now adds a sidebar, a folded
// contents box and heading anchors, so it is the first time the page has had
// ids of its own that could collide with a heading's.
//
// Negative control, run by hand: adding id="status" to the template's <main>
// fails this on upstream.html and reference.html, which both have a "Status"
// heading. (id="results" did not: no page has a heading with that id, so the
// first attempt at this control stayed green.)
func TestNoDuplicateIDs(t *testing.T) {
	out := buildToTemp(t)
	for _, p := range pages {
		seen := map[string]int{}
		for _, m := range htmlID.FindAllStringSubmatch(read(t, filepath.Join(out, p.Out)), -1) {
			seen[m[1]]++
		}
		if len(seen) == 0 {
			t.Errorf("%s has no ids at all; this test is not looking at what it thinks", p.Out)
		}
		var dups []string
		for id, n := range seen {
			if n > 1 {
				dups = append(dups, id)
			}
		}
		sort.Strings(dups)
		for _, id := range dups {
			t.Errorf("%s: id %q appears %d times", p.Out, id, seen[id])
		}
	}
}
