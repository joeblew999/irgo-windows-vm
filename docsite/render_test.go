package docsite

import (
	"regexp"
	"strings"
	"testing"
)

func renderString(t *testing.T, src string) renderedPage {
	t.Helper()
	out, err := renderMarkdown(newMarkdown(), []byte(src))
	if err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	return out
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
// Negative control, run by hand: removing renderCodeBlock's "<pre><code>"
// write fails the plain case; dropping chromahtml.WithClasses fails the "no
// inline style" check.
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

// TestCodeBlockUnknownLanguage: a language chroma does not know keeps its
// label, escaped, and its text plain.
//
// Negative control, run by hand: writing lang without util.EscapeHTML fails
// the escaping check.
func TestCodeBlockUnknownLanguage(t *testing.T) {
	body := string(renderString(t, "```no-such-lang\"x\n<b>\n```\n").Body)
	want := `<div class="codeblock" data-lang="no-such-lang&quot;x"><pre><code>&lt;b&gt;` + "\n" + `</code></pre></div>`
	if !strings.Contains(body, want) {
		t.Errorf("got:\n%s\nwant it to contain:\n%s", body, want)
	}
}

// TestGitHubAlerts: a callout written for GitHub gets the classes the
// stylesheet colours, rather than rendering as a quote that opens with a
// literal "[!WARNING]".
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

// TestAnchorAttributesInAFixedOrder: the anchor extension sets the "#" link's
// attributes from a map, so without anchorAttributeOrder two builds of the
// same page differed in every heading.
//
// Negative control, run by hand: removing anchorAttributeOrder from
// decorations fails this (with 30 headings, a random order is all but certain
// to show).
func TestAnchorAttributesInAFixedOrder(t *testing.T) {
	var src strings.Builder
	for i := 0; i < 30; i++ {
		src.WriteString("## Heading\n\n")
	}
	body := string(renderString(t, src.String()).Body)
	want := `<a aria-hidden="true" class="anchor" tabindex="-1" href=`
	if n := strings.Count(body, want); n != 30 {
		t.Errorf("%d of 30 anchors have their attributes in order:\n%s", n, body)
	}
}
