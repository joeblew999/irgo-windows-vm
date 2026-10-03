package docsite

// The checks `docsite check` runs over a built site.
//
// They read the output, not the renderers, because the failures worth catching
// are the ones where each piece is correct and the whole is not: a page that
// renders as HTML and never reaches the corpus, an anchor naming a heading
// that was renamed, a picture deleted from under its caption. None of them is
// visible on a page that renders, and each has happened.
//
// Every check also counts what it looked at, and a check that found nothing to
// look at is itself a problem where that can only mean its pattern has stopped
// matching.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// Report is what Check found.
type Report struct {
	// Problems fail the check, each naming the file and what is wrong.
	Problems []string

	// Notes say what each check covered.
	Notes []string
}

func (r *Report) fail(format string, a ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, a...))
}
func (r *Report) note(format string, a ...any) { r.Notes = append(r.Notes, fmt.Sprintf(format, a...)) }

// Check verifies the site Build wrote into s.Out.
func Check(s *Site) (*Report, error) {
	o, err := readOutput(s.Out)
	if err != nil {
		return nil, err
	}
	r := &Report{}
	checkEveryPageReachesEveryRendering(s, o, r)
	checkSitemap(s, o, r)
	checkAnchors(o, r)
	checkLocalLinks(o, r)
	checkImages(o, r)
	checkScreensReferenced(s, o, r)
	checkHeader(o, r)
	checkIDs(o, r)
	checkTables(o, r)
	checkFooters(s, o, r)
	if err := checkHeadingIDsStable(s, r); err != nil {
		return nil, err
	}
	if err := checkLinksFromSource(s, o, r); err != nil {
		return nil, err
	}
	return r, nil
}

// output is the built site, read once.
type output struct {
	dir   string
	html  map[string]string          // top-level .html files
	md    map[string]string          // top-level .md files
	ids   map[string]map[string]bool // html file -> its ids
	files map[string]bool            // every file, slash-separated, from dir
}

var htmlID = regexp.MustCompile(`id="([^"]+)"`)

func readOutput(dir string) (*output, error) {
	o := &output{dir: dir, html: map[string]string{}, md: map[string]string{}, ids: map[string]map[string]bool{}, files: map[string]bool{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		o.files[rel] = true
		if strings.Contains(rel, "/") {
			return nil
		}
		switch filepath.Ext(rel) {
		case ".html", ".md":
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if filepath.Ext(rel) == ".md" {
				o.md[rel] = string(b)
				return nil
			}
			o.html[rel] = string(b)
			set := map[string]bool{}
			for _, m := range htmlID.FindAllStringSubmatch(string(b), -1) {
				set[m[1]] = true
			}
			o.ids[rel] = set
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(o.html) == 0 {
		return nil, fmt.Errorf("%s holds no HTML; build the site first", dir)
	}
	return o, nil
}

func sortedNames(m map[string]string) []string { return sortedKeys(m) }

// checkEveryPageReachesEveryRendering is the divergence gate: every page is in
// the HTML, its .md, llms-full.txt, llms.txt and the sitemap. They are written
// by one loop, so they cannot disagree unless somebody edits that loop; this
// notices when somebody does, in every direction.
func checkEveryPageReachesEveryRendering(s *Site, o *output, r *Report) {
	full, index, sitemap := o.read(corpusFull), o.read(corpusIndex), o.read(sitemapFile)
	for _, p := range s.Pages {
		if _, ok := o.html[p.Out]; !ok {
			r.fail("%s is a page but the build did not publish it", p.Out)
		}
		// The corpus delimiter names the page's URL, not its title: two pages
		// could share a title, they cannot share an output file.
		if !strings.Contains(full, "Source: "+s.Base+p.Out+"\n") {
			r.fail("%s is a page but %s does not contain it", p.Out, corpusFull)
		}
		if !strings.Contains(index, "]("+s.Base+p.Out+")") {
			r.fail("%s is a page but %s does not link it", p.Out, corpusIndex)
		}
		md := markdownName(p.Out)
		if strings.TrimSpace(o.md[md]) == "" {
			// An empty page is worse than a missing one: it still serves.
			r.fail("%s is a page but %s is missing or empty", p.Out, md)
		}
		if s.Base != "" {
			for _, u := range []string{p.Out, md} {
				if !strings.Contains(sitemap, "<loc>"+escapeXML(s.Base+u)+"</loc>") {
					r.fail("%s is published but %s does not list it", u, sitemapFile)
				}
			}
		}
	}
	// The reading order is what makes the concatenated file navigable.
	at := -1
	for _, p := range s.Pages {
		i := strings.Index(full, "Source: "+s.Base+p.Out+"\n")
		if i >= 0 && i < at {
			r.fail("%s appears out of order in %s", p.Out, corpusFull)
		}
		at = max(at, i)
	}
	r.note("%d pages in the HTML, the markdown copies, both corpus files and the sitemap", len(s.Pages))
}

func (o *output) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(o.dir, name))
	return string(b)
}

// checkSitemap: a sitemap no parser accepts is worse than none, and the
// failure is invisible from reading it.
func checkSitemap(s *Site, o *output, r *Report) {
	if s.Base == "" {
		r.note("no base URL, so no sitemap")
		return
	}
	var doc struct {
		XMLName xml.Name `xml:"urlset"`
		URLs    []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(o.read(sitemapFile)), &doc); err != nil {
		r.fail("%s does not parse: %v", sitemapFile, err)
		return
	}
	// Every page twice (HTML and markdown), plus the two corpus files.
	if want := len(s.Pages)*2 + 2; len(doc.URLs) != want {
		r.fail("%s has %d urls, want %d", sitemapFile, len(doc.URLs), want)
	}
	for _, u := range doc.URLs {
		if !strings.HasPrefix(u.Loc, "https://") && !strings.HasPrefix(u.Loc, "http://") {
			r.fail("%s lists a url that is not absolute: %q", sitemapFile, u.Loc)
		}
	}
}

// anchorLink matches an href with a fragment, with or without a page in front.
var anchorLink = regexp.MustCompile(`href="([a-z0-9._-]*)#([^"]+)"`)

// checkAnchors: every fragment link names an id on its page.
//
// The ids are read from the rendered HTML, not recomputed from heading text:
// goldmark drops punctuation without a separator, leaves a double hyphen for a
// spaced dash and numbers collisions, and a slug function here would have to
// agree on all three or disagree silently.
func checkAnchors(o *output, r *Report) {
	checked := 0
	for _, name := range sortedNames(o.html) {
		for _, m := range anchorLink.FindAllStringSubmatch(o.html[name], -1) {
			page, frag := m[1], m[2]
			if page == "" {
				page = name
			}
			checked++
			ids, ok := o.ids[page]
			if !ok {
				r.fail("%s links to %s#%s, and %s is not a page this site publishes", name, page, frag, page)
				continue
			}
			if !ids[frag] {
				r.fail("%s links to %s#%s, and %s has no heading with that id", name, page, frag, page)
			}
		}
	}
	r.note("%d fragment links resolve", checked)
}

var localRef = regexp.MustCompile(`(?:href|src)="([^"]*)"`)

// checkLocalLinks: every local href and src names a file the build wrote, and
// no absolute URL has been glued onto a repository path (".../blob/main/https://..."),
// which is a valid absolute link and so invisible to a local-file check.
func checkLocalLinks(o *output, r *Report) {
	checked := 0
	for _, name := range sortedNames(o.html) {
		for _, m := range localRef.FindAllStringSubmatch(o.html[name], -1) {
			ref := m[1]
			if i := strings.Index(ref, "/blob/"); i >= 0 {
				rest := ref[i+len("/blob/"):]
				if j := strings.Index(rest, "/"); j >= 0 && hasScheme(rest[j+1:]) {
					r.fail("%s links to %s: an absolute URL was rewritten as a repository path", name, ref)
					continue
				}
			}
			if ref == "" || hasScheme(ref) || strings.HasPrefix(ref, "//") {
				continue
			}
			ref, _, _ = strings.Cut(ref, "#")
			ref, _, _ = strings.Cut(ref, "?")
			if ref == "" {
				continue
			}
			checked++
			if !o.files[strings.TrimPrefix(ref, "./")] {
				r.fail("%s links to %s and the build published no such file", name, ref)
			}
		}
	}
	r.note("%d local links resolve", checked)
}

var (
	htmlImage = regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
	mdImage   = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
)

// checkImages: every picture on a page, in both renderings, exists. A link
// check that greps href never sees an img's src, and deleting a screenshot
// leaves a broken image with every other check green.
func checkImages(o *output, r *Report) {
	checked := 0
	scan := func(name, body string, re *regexp.Regexp) {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			ref := m[1]
			if hasScheme(ref) || strings.HasPrefix(ref, "//") {
				continue
			}
			ref, _, _ = strings.Cut(ref, "#")
			checked++
			if !o.files[ref] {
				r.fail("%s shows an image at %s and the build published no such file", name, ref)
			}
		}
	}
	for _, name := range sortedNames(o.html) {
		scan(name, o.html[name], htmlImage)
	}
	for _, name := range sortedNames(o.md) {
		scan(name, o.md[name], mdImage)
	}
	r.note("%d local images exist", checked)
}

// checkScreensReferenced: every published screenshot is named by some page.
// The directory is copied wholesale, so an uncaptioned shot is published
// anyway, and a picture nobody explains is decoration that looks like
// evidence.
func checkScreensReferenced(s *Site, o *output, r *Report) {
	var shots []string
	for f := range o.files {
		if strings.HasPrefix(f, screensOut+"/") {
			shots = append(shots, f)
		}
	}
	if len(shots) == 0 {
		return
	}
	sort.Strings(shots)
	var all strings.Builder
	for _, b := range o.html {
		all.WriteString(b)
	}
	for _, b := range o.md {
		all.WriteString(b)
	}
	hay := all.String()
	orphans := 0
	for _, f := range shots {
		if !strings.Contains(hay, f) {
			r.fail("%s is published and no page mentions it: caption it, or take it out of %s", f, s.Screens)
			orphans++
		}
	}
	r.note("%d published screenshots, %d referenced", len(shots), len(shots)-orphans)
}

var (
	headerBlock = regexp.MustCompile(`(?s)<header\b.*?</header>`)
	headerLink  = regexp.MustCompile(`<a\b[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
)

// checkHeader: no two header links agree on both destination and text, which
// is how the wordmark and a nav entry for the home page once sat side by side.
func checkHeader(o *output, r *Report) {
	for _, name := range sortedNames(o.html) {
		header := headerBlock.FindString(o.html[name])
		if header == "" {
			r.fail("%s has no <header>", name)
			continue
		}
		seen := map[string]bool{}
		for _, m := range headerLink.FindAllStringSubmatch(header, -1) {
			key := m[1] + "\x00" + strings.TrimSpace(m[2])
			if seen[key] {
				r.fail("%s: the header links to %s as %q twice", name, m[1], strings.TrimSpace(m[2]))
			}
			seen[key] = true
		}
	}
}

// checkIDs: an id that appears twice makes every link to it land on whichever
// comes first.
func checkIDs(o *output, r *Report) {
	for _, name := range sortedNames(o.html) {
		seen := map[string]int{}
		for _, m := range htmlID.FindAllStringSubmatch(o.html[name], -1) {
			seen[m[1]]++
		}
		for _, id := range sortedKeys(seen) {
			if seen[id] > 1 {
				r.fail("%s: id %q appears %d times", name, id, seen[id])
			}
		}
	}
}

var (
	anyTable     = regexp.MustCompile(`<table>`)
	wrappedTable = regexp.MustCompile(`<div class="table-scroll"[^>]*>\s*<table>`)
)

// checkTables: on a phone a wide table must scroll inside the article, never
// widen the page, which needs the wrapper on every table.
func checkTables(o *output, r *Report) {
	for _, name := range sortedNames(o.html) {
		n, w := len(anyTable.FindAllString(o.html[name], -1)), len(wrappedTable.FindAllString(o.html[name], -1))
		if n != w {
			r.fail("%s: %d tables, %d of them in a scroll box", name, n, w)
		}
	}
}

var (
	tagRE       = regexp.MustCompile(`<[^>]*>`)
	footerBlock = regexp.MustCompile(`(?s)<footer\b.*?</footer>`)
)

// checkFooters: a page from a file names that file, and a generated page does
// not claim to come from one. Only the footer is read: a generated page may
// well say "generated from" in its own text.
func checkFooters(s *Site, o *output, r *Report) {
	for _, p := range s.Pages {
		body, ok := o.html[p.Out]
		if !ok {
			continue
		}
		text := strings.Join(strings.Fields(tagRE.ReplaceAllString(footerBlock.FindString(body), " ")), " ")
		if text == "" {
			r.fail("%s has no <footer>", p.Out)
			continue
		}
		if p.Src != "" {
			if !strings.Contains(text, "Generated from "+p.Src) {
				r.fail("%s comes from %s, and its footer does not name it", p.Out, p.Src)
			}
			continue
		}
		if strings.Contains(text, "Generated from ") {
			r.fail("%s is generated, but its footer claims it came from a file", p.Out)
		}
	}
}

// checkHeadingIDsStable: nothing the site adds to goldmark may move a heading
// id, because other pages and other sites link to them. Every source page is
// rendered twice, by the site and by bare GFM with auto ids, and the ids must
// match in order.
func checkHeadingIDsStable(s *Site, r *Report) error {
	plain := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	site := newMarkdown()
	w := newLinkRewriter(s)
	headingID := regexp.MustCompile(`<h[1-6] id="([^"]+)"`)
	compared := 0
	for _, p := range s.Pages {
		if p.Src == "" {
			continue
		}
		raw, err := readSource(s, p.Src)
		if err != nil {
			return err
		}
		body := w.rewrite(raw, p.Src)
		var before bytes.Buffer
		if err := plain.Convert(body, &before); err != nil {
			return err
		}
		after, err := renderMarkdown(site, body)
		if err != nil {
			return err
		}
		want := headingID.FindAllStringSubmatch(before.String(), -1)
		got := headingID.FindAllStringSubmatch(string(after.Body), -1)
		if len(got) != len(want) {
			r.fail("%s: %d headings with ids, plain goldmark gives %d", p.Src, len(got), len(want))
			continue
		}
		for i := range want {
			compared++
			if got[i][1] != want[i][1] {
				r.fail("%s: heading %d's id is %q, plain goldmark gives %q; every link to it would break", p.Src, i, got[i][1], want[i][1])
				break
			}
		}
	}
	r.note("%d heading ids are what plain goldmark gives", compared)
	return nil
}

// checkLinksFromSource: links into the site from files that are not pages
// (messages a binary prints, issue forms, absolute URLs in the markdown, which
// the renderer leaves alone) name a published page and, with a fragment, one
// of its headings. When a site's pages are renamed, these are the links that
// go on pointing at the old names and nothing says so.
func checkLinksFromSource(s *Site, o *output, r *Report) error {
	var prefixes []string
	if s.Base != "" {
		prefixes = append(prefixes, regexp.QuoteMeta(s.Base))
	}
	prefixes = append(prefixes, s.Check.Prefixes...)
	if len(prefixes) == 0 {
		r.note("no base URL and no prefixes, so no links into the site from source")
		return nil
	}
	re, err := regexp.Compile(`(?:` + strings.Join(prefixes, "|") + `)([a-z0-9][a-z0-9._-]*\.html)(?:#([A-Za-z0-9_-]+))?`)
	if err != nil {
		return fmt.Errorf("check.prefixes: %w", err)
	}
	files, err := globFiles(s.Root, s.Check.Sources)
	if err != nil {
		return err
	}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(f)))
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			page, frag := m[1], m[2]
			checked++
			ids, ok := o.ids[page]
			if !ok {
				r.fail("%s links to %s, which the site does not publish", f, page)
				continue
			}
			if frag != "" && !ids[frag] {
				r.fail("%s links to %s#%s, and %s has no heading with that id", f, page, frag, page)
			}
		}
	}
	r.note("%d links into the site from %d source files resolve", checked, len(files))
	return nil
}
