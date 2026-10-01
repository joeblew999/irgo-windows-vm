package docsite

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed page.tmpl
var pageTmpl string

//go:embed style.css
var styleCSS []byte

// screensOut is where the screenshots directory is published.
const screensOut = "screens"

type nav struct {
	Title, Href, Blurb string
	Current            bool

	// Section marks the header entry the current page is under, highlighted
	// like the current page but not it (aria-current="true").
	Section bool
}

type pageData struct {
	Title, Blurb string
	Body         template.HTML
	Nav          []nav

	// All is every page but the home page, for the footer: the one place a
	// page under a header entry is linked from every page.
	All []nav

	// TOC is the page's own sections, from the same parse as Body. Empty on a
	// page too short to need one, and the template then draws no sidebar.
	TOC template.HTML

	Repo, RepoLabel, Branch string
	License                 string
	Releases                bool

	// Build is the commit and time this page was generated, so a cached copy
	// can be told from a current one.
	Build string

	// Home marks the page the wordmark links to.
	Home bool
	Site string

	// Source is the markdown file this page was rendered from, empty for a
	// generated page, which gets Footer instead: telling the reader of a
	// generated page to edit a markdown file sends them to a file that does
	// not exist.
	Source string
	Footer template.HTML

	// Foot is a hook's HTML for the end of <body>.
	Foot template.HTML
}

// Options are the per-run settings that are not part of the config.
type Options struct {
	// SHA is the commit in the build stamp. Empty: read from git.
	SHA string

	// Time is the build stamp's time. Zero: SOURCE_DATE_EPOCH, else now.
	Time time.Time

	// Log receives a line per file written. Nil: discarded.
	Log io.Writer
}

// Build renders the site into s.Out, replacing whatever was there.
func Build(s *Site, opt Options) error {
	logw := opt.Log
	if logw == nil {
		logw = io.Discard
	}
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(logw, format, a...) } // progress only
	if opt.SHA == "" {
		opt.SHA = commitSHA(s.Root)
	}
	stamp := buildStamp{SHA: opt.SHA, Time: stampTime(opt.Time)}

	// Every page's markdown first: hooks run concurrently, and the nav needs
	// every title before any page is rendered.
	raw, foot, err := gather(s)
	if err != nil {
		return err
	}
	s.fillDefaults(raw)

	// Rebuilt from scratch every time: a deleted page left in place stays
	// published, saying something the repository no longer does.
	if err := os.RemoveAll(s.Out); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Out, 0o755); err != nil {
		return err
	}
	screens, err := copyScreens(s)
	if err != nil {
		return err
	}
	tmpl, err := template.New("page").Parse(pageTmpl)
	if err != nil {
		return err
	}
	md := newMarkdown()
	license := ""
	if _, err := os.Stat(filepath.Join(s.Root, "LICENSE")); err == nil {
		license = s.License
	}
	repoLabel := "Source"
	if strings.HasPrefix(s.Repo, "https://github.com/") {
		repoLabel = "GitHub"
	}
	w := newLinkRewriter(s)

	var corpus []corpusEntry
	for _, p := range s.Pages {
		// The rewritten markdown, named rather than passed inline, because the
		// corpus is built from exactly what the HTML is built from: one pass
		// producing both renderings, so neither can skip a page.
		body := w.rewrite(raw[p.Out], p.Src)
		html, err := renderMarkdown(md, body)
		if err != nil {
			return fmt.Errorf("converting %s: %w", p.Out, err)
		}
		corpus = append(corpus, corpusEntry{Title: p.Title, Out: p.Out, Blurb: p.Blurb, Markdown: body, Generated: p.Src == ""})

		var navs, all []nav
		for _, q := range s.Pages {
			if q.Home() {
				continue // the wordmark is its link
			}
			all = append(all, nav{Title: q.Title, Href: q.Out, Blurb: q.Blurb, Current: q.Out == p.Out})
			if !q.InHeader() {
				continue // under another entry: in the footer, not the header
			}
			navs = append(navs, nav{Title: q.Nav, Href: q.Out, Blurb: q.Blurb, Current: q.Out == p.Out, Section: q.Out == p.Parent})
		}
		footer := p.Footer
		if p.Src == "" && footer == "" {
			footer = "Generated at build time; nothing on it is typed by hand. To correct this page, change what generates it: no markdown file feeds it."
		}

		var rendered bytes.Buffer
		data := pageData{
			Title: p.Title, Blurb: p.Blurb, Body: html.Body, TOC: html.TOC, Nav: navs, All: all,
			Repo: s.Repo, RepoLabel: repoLabel, Branch: s.Branch, License: license, Releases: repoLabel == "GitHub",
			Source: p.Src, Build: stamp.line(), Home: p.Home(), Site: s.Name,
			// Both from the project itself, trusted like the template.
			Footer: template.HTML(footer),
			Foot:   template.HTML(foot[p.Out]),
		}
		if err := tmpl.Execute(&rendered, data); err != nil {
			return fmt.Errorf("rendering %s: %w", p.Out, err)
		}
		if err := os.WriteFile(filepath.Join(s.Out, p.Out), rendered.Bytes(), 0o644); err != nil {
			return err
		}
		from := p.Src
		if from == "" {
			from = "(generated)"
		}
		logf("  %-18s <- %s\n", p.Out, from)
	}

	if err := os.WriteFile(filepath.Join(s.Out, "style.css"), styleCSS, 0o644); err != nil {
		return err
	}
	syntax, err := syntaxCSS()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.Out, syntaxFile), syntax, 0o644); err != nil {
		return err
	}

	// The same pages again as plain markdown, one file each: a fetcher that
	// fails on a rendered page usually succeeds on plain text.
	for _, e := range corpus {
		if err := os.WriteFile(filepath.Join(s.Out, markdownName(e.Out)), e.Markdown, 0o644); err != nil {
			return err
		}
	}
	logf("  %-18s <- %d pages, plain markdown\n", "*.md", len(corpus))

	summary := summaryFrom(indexPage(corpus).Markdown)
	files := []struct {
		name string
		body []byte
	}{
		{corpusFull, renderCorpusFull(corpus, s.Base, stamp, s.LLMs.FullNote)},
		{corpusIndex, renderCorpusIndex(corpus, s.Base, summary, stamp, s.LLMs)},
		{robotsFile, renderRobots(s.Base, s.Name, corpus, s.LLMs.RobotsNote)},
	}
	// A sitemap's URLs must be absolute; without a base there is nothing
	// honest to write.
	if s.Base != "" {
		files = append(files, struct {
			name string
			body []byte
		}{sitemapFile, renderSitemap(corpus, s.Base)})
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(s.Out, f.name), f.body, 0o644); err != nil {
			return err
		}
	}
	logf("  %-18s <- %d pages, one file\n", corpusFull, len(corpus))
	for _, f := range files[1:] {
		logf("  %-18s <- the same list\n", f.name)
	}

	// Tells GitHub Pages not to run Jekyll over the output, which would drop
	// any file whose name starts with an underscore: a silent 404.
	if err := os.WriteFile(filepath.Join(s.Out, ".nojekyll"), nil, 0o644); err != nil {
		return err
	}
	logf("  %-18s (%d screenshots)\n", screensOut+"/", len(screens))
	return nil
}

// stampTime is the build time: t, or SOURCE_DATE_EPOCH for a reproducible
// build, or now.
func stampTime(t time.Time) string {
	if t.IsZero() {
		if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				t = time.Unix(n, 0)
			}
		}
	}
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339)
}

// gather reads every page's markdown, from its file or its hook, and every
// page's foot HTML. A missing source or a failed hook is fatal: a page that
// quietly disappears, or renders empty, still looks right.
func gather(s *Site) (raw, foot map[string][]byte, err error) {
	raw, foot = map[string][]byte{}, map[string][]byte{}
	var jobs []hookJob
	for _, p := range s.Pages {
		if p.Src != "" {
			b, err := os.ReadFile(filepath.Join(s.Root, p.Src))
			if err != nil {
				return nil, nil, fmt.Errorf("reading %s: %w", p.Src, err)
			}
			raw[p.Out] = b
		} else {
			jobs = append(jobs, hookJob{page: p.Out, hook: *p.Generate, dst: raw})
		}
		if p.Foot != nil {
			jobs = append(jobs, hookJob{page: p.Out, hook: *p.Foot, dst: foot})
		}
	}
	if err := runHooks(s, jobs); err != nil {
		return nil, nil, err
	}
	return raw, foot, nil
}

// anyLink matches every markdown link target. Which ones to rewrite is decided
// in code, not by the pattern; see hasScheme.
var anyLink = regexp.MustCompile(`\]\(([^)#]*?)(#[^)]*)?\)`)

// hasScheme reports whether a link target is absolute: https:, mailto:, and so
// on. RFC 3986: a letter, then letters, digits, +, - or ., up to a colon.
//
// The first version excluded ":" only as a target's first character, so every
// "https://..." was rewritten as a repository path and every outbound link on
// the site was broken, invisibly to a local link check.
func hasScheme(target string) bool { return schemeRE.MatchString(target) }

var schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)

type linkRewriter struct {
	pages   map[string]string // source file -> page
	outs    map[string]bool   // names the build writes
	screens string
	blob    string
}

func newLinkRewriter(s *Site) *linkRewriter {
	w := &linkRewriter{pages: map[string]string{}, outs: map[string]bool{}, screens: s.Screens}
	for _, p := range s.Pages {
		if p.Src != "" {
			w.pages[p.Src] = p.Out
		}
		w.outs[p.Out] = true
	}
	// Files the build writes beside the pages, which a page may link.
	w.outs[corpusIndex], w.outs[corpusFull] = true, true
	if s.Repo != "" {
		w.blob = s.Repo + "/blob/" + s.Branch + "/"
	}
	return w
}

// rewrite points every in-repository link at wherever that thing is once the
// site is built:
//
//   - a file that becomes a page  -> that page          (docs/USING.md -> using.html)
//   - a published screenshot      -> the published copy (docs/screens/x.png -> screens/x.png)
//   - anything else in the repo   -> the repository     (LICENSE -> <repo>/blob/main/LICENSE)
//
// Targets are relative to src, the file they are written in, exactly as
// GitHub reads them, so a link works in both places. A target that is already
// a published name (mcp.html) is left alone, and so is every link when the
// project has no repository URL: the check then reports it, rather than the
// site pointing it at a guess.
func (w *linkRewriter) rewrite(raw []byte, src string) []byte {
	return anyLink.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := anyLink.FindSubmatch(m)
		target, anchor := string(sub[1]), string(sub[2])

		// Left exactly as written: anything absolute, a root-relative path, and
		// a bare anchor like (#evidence).
		if target == "" || hasScheme(target) || strings.HasPrefix(target, "/") || w.outs[target] {
			return m
		}
		target = path.Join(path.Dir(src), target)
		if out, ok := w.pages[target]; ok {
			return []byte("](" + out + anchor + ")")
		}
		if w.screens != "" {
			if rest, ok := strings.CutPrefix(target, w.screens+"/"); ok {
				return []byte("](" + screensOut + "/" + rest + anchor + ")")
			}
		}
		if w.blob == "" {
			return m
		}
		return []byte("](" + w.blob + target + anchor + ")")
	})
}

// imageExt is what copyScreens publishes.
var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true}

// copyScreens publishes the screenshots directory and returns the files'
// names. Walked, not listed, so subdirectories are kept: a flat ReadDir
// silently dropped every shot the moment they were sorted into folders.
func copyScreens(s *Site) ([]string, error) {
	if s.Screens == "" {
		return nil, nil
	}
	srcDir := filepath.Join(s.Root, filepath.FromSlash(s.Screens))
	dstDir := filepath.Join(s.Out, screensOut)
	var names []string
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if d.IsDir() || !imageExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := copyFile(p, dst); err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		_ = f.Close() // already failing
		return err
	}
	// Checked, because this is a write: a short copy is a truncated image that
	// renders as a broken one rather than as an error.
	return f.Close()
}
