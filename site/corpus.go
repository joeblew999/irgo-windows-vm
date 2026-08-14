package main

// The documentation as one file, for anything that would rather not make six
// requests.
//
// The site is correct and public and still takes six fetches to read, which is
// six chances to be rate-limited — and an agent asked to read these docs got the
// index and no further. As markdown, without the page template around it, the
// whole corpus is about a third smaller than the HTML and fits in any context
// window.
//
// Two files, following the convention at https://llmstxt.org:
//
//   llms.txt       an index: what this is, then a link and a line per page
//   llms-full.txt  every page, concatenated, in the site's order
//
// Neither is authored. Both are rendered from the same pass over `pages` that
// writes the HTML — see build. Nothing here holds a second list, and nothing
// here restates a page description: those come from the Blurb that is already
// the page's meta description.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// corpusEntry is one page as the corpus sees it: what the HTML rendering was
// given, kept as it was given.
type corpusEntry struct {
	Title, Out, Blurb string
	Markdown          []byte
}

const (
	corpusIndex = "llms.txt"
	corpusFull  = "llms-full.txt"
	sitemapFile = "sitemap.xml"
	robotsFile  = "robots.txt"
)

// indexPage is the corpus entry for the site's front page.
//
// Its title names the project and its markdown is the README, so both the
// corpus H1 and the summary line come from the list that is already there.
// Neither is declared a second time.
func indexPage(entries []corpusEntry) corpusEntry {
	for _, e := range entries {
		if e.Out == "index.html" {
			return e
		}
	}
	// Not fatal: a corpus without a front page is odd but still readable, and
	// the divergence gate is what fails when a page goes missing.
	if len(entries) > 0 {
		return entries[0]
	}
	return corpusEntry{Title: "documentation"}
}

// renderCorpusFull concatenates every page in order.
//
// Each page is introduced by a rule, its title, and the URL it is published at,
// so a reader landing mid-file knows which document they are in and can fetch
// the rendered version. That header is also what the divergence gate looks for,
// which is why it names the page's output file rather than its title alone —
// two pages could share a title, they cannot share a URL.
//
// The markdown is stored exactly as the HTML rendering received it, which means
// links have already been rewritten from `AGENTS.md` to `agents.html`. That is
// deliberate: this file is served from the site root, so those relative links
// resolve against its own URL. The raw markdown would carry `.md` targets that
// point at nothing from a published text file.
func renderCorpusFull(entries []corpusEntry, base string) []byte {
	var b bytes.Buffer

	fmt.Fprintf(&b, "# %s — complete documentation\n\n", indexPage(entries).Title)
	fmt.Fprintf(&b, "Every page of %s, concatenated in reading order.\n", base)
	b.WriteString("Generated from the same source as the site. Five of these pages come from\n")
	b.WriteString("markdown in the repository; the command reference has no source file and is\n")
	b.WriteString("captured from the compiled binary, so a wrong flag there is a bug in Go, not\n")
	b.WriteString("in any markdown.\n\n")
	b.WriteString("Pages, in order:\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s — %s\n", e.Title, e.Blurb)
	}

	for _, e := range entries {
		b.WriteString("\n\n---\n\n")
		// The delimiter the gate matches. Kept on one line and machine-shaped
		// on purpose: a reader can skim it, and a check can find it exactly.
		fmt.Fprintf(&b, "# %s\n\nSource: %s%s\n\n", e.Title, base, e.Out)
		b.Write(bytes.TrimRight(e.Markdown, "\n"))
		b.WriteString("\n")
	}
	return b.Bytes()
}

// renderCorpusIndex is the short form: what this project is, then one line per
// page, then a pointer to the whole thing.
//
// The shape is the llms.txt convention — an H1, a blockquote summary, then
// sections of `- [Name](url): description`.
func renderCorpusIndex(entries []corpusEntry, base, summary string) []byte {
	var b bytes.Buffer

	fmt.Fprintf(&b, "# %s\n\n", indexPage(entries).Title)
	if summary != "" {
		fmt.Fprintf(&b, "> %s\n\n", summary)
	}
	b.WriteString("Documentation for a tool that installs Windows 11 ARM64 on Apple Silicon\n")
	b.WriteString("unattended and runs Go binaries inside it. Generated from the repository's\n")
	b.WriteString("own markdown, so it cannot disagree with the source.\n\n")

	b.WriteString("## Documentation\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- [%s](%s%s): %s\n", e.Title, base, e.Out, e.Blurb)
	}

	b.WriteString("\n## Everything at once\n\n")
	fmt.Fprintf(&b, "- [%s](%s%s): every page above in one file, for reading in a single request\n",
		corpusFull, base, corpusFull)

	// Why this file rather than the repository's markdown.
	//
	// Not a general preference for the site over the source: it is that one
	// page has no source. Asked how an agent should read these docs, a capable
	// model recommended fetching the .md files from raw.githubusercontent.com —
	// which works for five of the six and silently drops the command reference,
	// producing documentation that looks complete with no flag reference in it.
	b.WriteString("\n## How this is generated\n\n")
	b.WriteString("Every page here is generated from markdown in the repository, so the source\n")
	b.WriteString("is authoritative and this is never edited by hand.\n\n")
	b.WriteString("One page is different, and it is the reason to prefer these files over\n")
	b.WriteString("fetching the raw markdown from GitHub: the command reference has **no source\n")
	b.WriteString("file**. It is captured from the compiled binary at build time — `irgo-winvm\n")
	b.WriteString("help` and `-h` for every command — so that no flag, default or usage string\n")
	b.WriteString("is ever transcribed. Fetching the repository's .md files gets you five of the\n")
	b.WriteString("six pages and silently omits it.\n")
	return b.Bytes()
}

// summaryFrom pulls the one-line description out of README's first paragraph of
// prose.
//
// Derived rather than written here, so there is no second copy to go stale. It
// skips the heading, the bare URL and the link line the README opens with, and
// takes the first real sentence — which is the sentence that already introduces
// the project to a human.
//
// Returns empty rather than guessing if the shape changes; the index is still
// useful without a summary line, and a wrong summary is worse than none.
func summaryFrom(readme []byte) string {
	for _, line := range strings.Split(string(readme), "\n") {
		s := strings.TrimSpace(line)
		switch {
		case s == "",
			strings.HasPrefix(s, "#"),
			strings.HasPrefix(s, "<"),
			strings.HasPrefix(s, "·"),
			strings.HasPrefix(s, "["),
			strings.HasPrefix(s, "!"),
			strings.HasPrefix(s, "|"),
			strings.HasPrefix(s, "-"):
			continue
		}
		return s
	}
	return ""
}

// renderSitemap lists every published URL, from the same entries as everything
// else.
//
// This is the standard machine-readable manifest and crawlers already look for
// it — the alternative considered was a bespoke JSON schema, which would have
// been a fourth rendering of one list that nothing consumes.
//
// The corpus files are listed alongside the pages deliberately. A crawler that
// reads a sitemap and does not know the llms.txt convention still finds them.
//
// No <lastmod>. It is optional, and the only source available here is the file
// mtime — which a fresh CI checkout sets to clone time, so every page would
// claim to have changed on every build. A field that is always wrong is worse
// than one that is absent.
func renderSitemap(entries []corpusEntry, base string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  <url><loc>%s</loc></url>\n", escapeXML(base+e.Out))
	}
	for _, f := range []string{corpusIndex, corpusFull} {
		fmt.Fprintf(&b, "  <url><loc>%s</loc></url>\n", escapeXML(base+f))
	}
	b.WriteString("</urlset>\n")
	return b.Bytes()
}

// renderRobots allows everything and says where the rest is.
//
// The comment naming the corpus is the point of the file as much as the
// Sitemap line is: asked how to read these docs, a capable model recommended
// scraping raw markdown from GitHub, because nothing published here told it
// there was a better route. robots.txt is one of the two places anything
// looking for that route will check.
func renderRobots(base string) []byte {
	var b bytes.Buffer
	b.WriteString("# Documentation for irgo-windows-vm. Everything here is public.\n")
	b.WriteString("#\n")
	b.WriteString("# Reading this with a machine? Two files are meant for you:\n")
	fmt.Fprintf(&b, "#   %s%s\n", base, corpusIndex)
	fmt.Fprintf(&b, "#     an index of every page, one line each\n")
	fmt.Fprintf(&b, "#   %s%s\n", base, corpusFull)
	fmt.Fprintf(&b, "#     the whole documentation in one request\n")
	b.WriteString("#\n")
	b.WriteString("# Prefer those over fetching the markdown from the repository: the command\n")
	b.WriteString("# reference is captured from the binary at build time and has no source file,\n")
	b.WriteString("# so the raw-markdown route silently omits it.\n\n")
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n\n")
	fmt.Fprintf(&b, "Sitemap: %s%s\n", base, sitemapFile)
	return b.Bytes()
}

// escapeXML is the small subset a <loc> needs. The URLs here are built from a
// flag and a set of filenames we choose, so this is belt and braces rather than
// untrusted input — but a & in a base URL would otherwise produce a sitemap
// that no parser accepts.
func escapeXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
