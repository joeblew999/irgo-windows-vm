package docsite

// The documentation as one file, for anything that would rather not make a
// request per page.
//
// A site that takes a dozen fetches to read is a dozen chances to be
// rate-limited: an agent asked to read one got the index and no further. As
// markdown, without the page template around it, the corpus is about a third
// smaller than the HTML and fits in any context window.
//
// Two files, following the convention at https://llmstxt.org:
//
//   llms.txt       an index: what this is, then a link and a line per page
//   llms-full.txt  every page, concatenated, in the site's order
//
// Neither is authored. Both are rendered from the same pass over the pages
// that writes the HTML; see Build. Nothing here holds a second list, and nothing
// here restates a page description: those come from the Blurb that is already
// the page's meta description.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// corpusEntry is one page as the corpus sees it: what the HTML rendering was
// given, kept as it was given.
type corpusEntry struct {
	Title, Out, Blurb string
	Markdown          []byte

	// Generated marks a page with no source file.
	Generated bool
}

const (
	corpusIndex = "llms.txt"
	corpusFull  = "llms-full.txt"
	sitemapFile = "sitemap.xml"
	robotsFile  = "robots.txt"
)

// indexPage is the corpus entry for the site's front page: its title is the
// corpus H1, and its first sentence the summary line.
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

// markdownName is a page's plain-text form: the same name with the extension
// swapped.
//
// A rule rather than a list, so a generated page gets one too: the page that
// cannot be fetched from the repository at all is the one most worth serving
// as plain text.
func markdownName(out string) string {
	return strings.TrimSuffix(out, filepath.Ext(out)) + ".md"
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
// links have already been rewritten from `USING.md` to `using.html`. That is
// deliberate: this file is served from the site root, so those relative links
// resolve against its own URL. The raw markdown would carry `.md` targets that
// point at nothing from a published text file.
func renderCorpusFull(entries []corpusEntry, base string, stamp buildStamp, note string) []byte {
	var b bytes.Buffer

	fmt.Fprintf(&b, "# %s — complete documentation\n\n", indexPage(entries).Title)
	if l := stamp.line(); l != "" {
		fmt.Fprintf(&b, "%s\n\n", l)
	}
	fmt.Fprintf(&b, "Every page of %s, concatenated in reading order.\n", siteOrThis(base))
	if note == "" {
		note = defaultFullNote(entries)
	}
	b.WriteString(paragraph(note))
	b.WriteString("Pages, in order:\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s — %s\n", e.Title, e.Blurb)
	}

	for _, e := range entries {
		b.WriteString("\n\n---\n\n")
		// The delimiter the divergence check matches. Kept on one line and
		// machine-shaped on purpose: a reader can skim it, and a check can find
		// it exactly.
		fmt.Fprintf(&b, "# %s\n\nSource: %s%s\n\n", e.Title, base, e.Out)
		b.Write(bytes.TrimRight(e.Markdown, "\n"))
		b.WriteString("\n")
	}
	return b.Bytes()
}

// siteOrThis names the site in prose, when it has a URL.
func siteOrThis(base string) string {
	if base == "" {
		return "this site"
	}
	return base
}

// paragraph is configured prose as a paragraph: ending in one newline and a
// blank line, however it was written in the config.
func paragraph(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n\n"
}

// generatedTitles names the pages that have no source file.
func generatedTitles(entries []corpusEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Generated {
			out = append(out, e.Title)
		}
	}
	return out
}

// defaultFullNote says where the pages come from, naming the generated ones.
func defaultFullNote(entries []corpusEntry) string {
	gen := generatedTitles(entries)
	if len(gen) == 0 {
		return "Generated from the same source as the site: the markdown in the repository."
	}
	return "Generated from the same source as the site. Every page comes from markdown in\n" +
		"the repository except " + strings.Join(gen, ", ") + ", generated at build time;\n" +
		"no markdown file feeds those."
}

// renderCorpusIndex is the short form: what this project is, then one line per
// page, then a pointer to the whole thing.
//
// The shape is the llms.txt convention — an H1, a blockquote summary, then
// sections of `- [Name](url): description`.
func renderCorpusIndex(entries []corpusEntry, base, summary string, stamp buildStamp, llms LLMsConfig) []byte {
	var b bytes.Buffer

	fmt.Fprintf(&b, "# %s\n\n", indexPage(entries).Title)
	if l := stamp.line(); l != "" {
		fmt.Fprintf(&b, "%s\n\n", l)
	}
	if summary != "" {
		fmt.Fprintf(&b, "> %s\n\n", summary)
	}
	about := llms.About
	if about == "" {
		about = "Generated from the repository's own markdown, so it cannot disagree with the\nsource."
	}
	b.WriteString(paragraph(about))

	b.WriteString("## Documentation\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- [%s](%s%s): %s\n", e.Title, base, e.Out, e.Blurb)
	}

	b.WriteString("\n## One page at a time, as markdown\n\n")
	b.WriteString("Every page above is also served as plain markdown, with the extension\n")
	b.WriteString("swapped — so the HTML page and its source are one character apart:\n\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- [%s](%s%s)\n", markdownName(e.Out), base, markdownName(e.Out))
	}

	b.WriteString("\n## Everything at once\n\n")
	fmt.Fprintf(&b, "- [%s](%s%s): every page above in one file, for reading in a single request\n",
		corpusFull, base, corpusFull)

	// Why these files rather than the repository's markdown: a generated page
	// has no source there. Asked how to read a site's docs, a capable model
	// recommended fetching the raw .md files, which silently drops every
	// generated page and looks complete.
	note := llms.IndexNote
	if note == "" {
		note = defaultIndexNote(entries)
	}
	b.WriteString("\n## How this is generated\n\n")
	b.WriteString(strings.TrimSpace(note) + "\n")
	return b.Bytes()
}

func defaultIndexNote(entries []corpusEntry) string {
	s := "Every page here is generated from markdown in the repository, so the source\n" +
		"is authoritative and this is never edited by hand."
	if gen := generatedTitles(entries); len(gen) > 0 {
		s += "\n\nExcept " + strings.Join(gen, ", ") + ": generated at build time, with no\n" +
			"source file. Fetching the repository's .md files gets you every other page\n" +
			"and silently omits those, which is the reason to prefer these files."
	}
	return s
}

// summaryFrom pulls the one-line description out of README's first paragraph of
// prose.
//
// Derived rather than written, so there is no second copy to go stale. It
// skips headings, HTML, badges, link lines, tables and lists, and takes the
// first line of prose: the line that already introduces the project to a
// human.
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
		// The plain-text form of the same page: a rendering nothing advertises
		// is a rendering nothing finds.
		fmt.Fprintf(&b, "  <url><loc>%s</loc></url>\n", escapeXML(base+markdownName(e.Out)))
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
func renderRobots(base, name string, entries []corpusEntry, note string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Documentation for %s. Everything here is public.\n", name)
	b.WriteString("#\n")
	b.WriteString("# Reading this with a machine? Two files are meant for you:\n")
	fmt.Fprintf(&b, "#   %s%s\n", base, corpusIndex)
	fmt.Fprintf(&b, "#     an index of every page, one line each\n")
	fmt.Fprintf(&b, "#   %s%s\n", base, corpusFull)
	fmt.Fprintf(&b, "#     the whole documentation in one request\n")
	if note == "" {
		if gen := generatedTitles(entries); len(gen) > 0 {
			note = "Prefer those over fetching the markdown from the repository: " + strings.Join(gen, ", ") +
				"\nhave no source file there, so the raw-markdown route silently omits them."
		}
	}
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("#\n")
		for _, l := range strings.Split(note, "\n") {
			b.WriteString(strings.TrimRight("# "+l, " ") + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	if base != "" {
		fmt.Fprintf(&b, "\nSitemap: %s%s\n", base, sitemapFile)
	}
	return b.Bytes()
}

// escapeXML is the small subset a <loc> needs. The URLs here are built from a
// config value and page names, so this is belt and braces, but a & in a base
// URL would otherwise produce a sitemap no parser accepts.
func escapeXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// buildStamp identifies what produced this output.
//
// Two agents were once served a page from a build several commits old, and
// nothing on it said which build it was, so a stale copy and a current one
// were indistinguishable from outside.
type buildStamp struct {
	SHA  string
	Time string
}

// line is the one-line form, used at the top of both corpus files and in
// every page footer.
func (b buildStamp) line() string {
	switch {
	case b.SHA == "" && b.Time == "":
		return ""
	case b.SHA == "":
		return "Built " + b.Time
	case b.Time == "":
		return "Built from " + b.SHA
	}
	return "Built from " + b.SHA + " at " + b.Time
}

// commitSHA reads the commit being built, or returns empty.
//
// Read from the checkout rather than passed in: rev-parse cannot disagree with
// the tree it is run in. Empty rather than fatal when that fails: a stamp is
// worth having and not worth refusing to build over.
func commitSHA(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
