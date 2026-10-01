// Package docsite renders a project's markdown (README.md and a docs folder)
// into a static site, and checks it.
//
// It generates; it does not author. Every sentence on the site comes from a
// markdown file that already exists, or from a hook the project runs to
// generate a page (a command reference captured from a binary, an API
// rendered from an OpenAPI document). If the site is wrong, the markdown or
// the hook is wrong; fix it there.
//
// A config file is optional. Without one, README.md becomes the home page,
// every docs/*.md a page, and the navigation is built from their H1s.
package docsite

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the optional docsite.toml. Every field may be left out. Paths are
// relative to Root, and Root is relative to the config file's directory.
type Config struct {
	// Name is the wordmark and the tail of every page's <title>. Default: the
	// home page's title.
	Name string `toml:"name"`

	// Root is the project directory the docs are read from. Default: the
	// directory the config file is in.
	Root string `toml:"root"`

	// Out is where the site is written. Default: _site.
	Out string `toml:"out"`

	// Repo is the repository's web URL. Links to repository files that are
	// not pages point into it, and the footer names it. Default: read from
	// `git remote get-url origin` when that is a GitHub remote.
	Repo string `toml:"repo"`

	// Branch is the one repository links point at. Default: main.
	Branch string `toml:"branch"`

	// Base is the published URL of the site, used where a link must be
	// absolute (llms.txt, the sitemap). Default: the GitHub Pages URL of a
	// GitHub repo; otherwise none, and the sitemap is not written.
	Base string `toml:"base"`

	// License is the label of the footer's link to LICENSE, shown when that
	// file exists. Default: "License".
	License string `toml:"license"`

	// Screens is a directory of images published under screens/. Default:
	// docs/screens when it exists.
	Screens string `toml:"screens"`

	// Pages is the site in navigation order. Empty: discovered.
	Pages []PageConfig `toml:"page"`

	Check CheckConfig `toml:"check"`
	LLMs  LLMsConfig  `toml:"llms"`
}

// PageConfig is one page.
type PageConfig struct {
	// Src is the markdown file, from Root. Empty for a generated page.
	Src string `toml:"src"`

	// Out is the published file name. Default: from Src (README.md is
	// index.html, docs/USING.md is using.html).
	Out string `toml:"out"`

	// Title is the page's name in the footer, its <title> and its H1 in the
	// corpus. Default: the markdown's first H1.
	Title string `toml:"title"`

	// Nav is the page's label in the header. Default: Title.
	Nav string `toml:"nav"`

	// Parent puts the page under a header entry (by its Out) instead of in
	// the header: it is listed in the footer and lights up its parent. That
	// keeps the header to one line as the docs grow.
	Parent string `toml:"parent"`

	// Blurb is the meta description and the page's line in llms.txt.
	// Default: the first sentence of the markdown.
	Blurb string `toml:"blurb"`

	// Intent marks a page that states intent rather than fact (a roadmap).
	// docsite itself does nothing with it; `docsite pages -intent` lists them
	// for a project's own checks.
	Intent bool `toml:"intent"`

	// Generate makes the page from a hook instead of Src.
	Generate *Hook `toml:"generate"`

	// Footer replaces the footer's sentence about where a generated page
	// comes from.
	Footer string `toml:"footer"`

	// Foot is raw HTML from a hook, written at the end of the page's <body>:
	// a script one page needs.
	Foot *Hook `toml:"foot"`
}

// Hook produces a page, or a page's extra HTML, at build time.
type Hook struct {
	// Run is a command, run in Root; its stdout is the output. It sees
	// DOCSITE_ROOT, DOCSITE_PAGE, DOCSITE_REPO and DOCSITE_BASE.
	Run []string `toml:"run"`

	// File is read instead of running anything.
	File string `toml:"file"`

	// Format is what the output is: "markdown" (the default for Generate),
	// "openapi" (an OpenAPI 3 JSON document, rendered to markdown by docsite)
	// or "html" (the only format for Foot).
	Format string `toml:"format"`
}

// CheckConfig adds to what `docsite check` verifies.
type CheckConfig struct {
	// Sources are globs (with ** and {a,b}) of files outside the site that
	// link into it by its Base URL: messages a binary prints, issue forms.
	// Each such link must name a published page and, with a #fragment, one of
	// its headings. Default: README.md and docs/**/*.md.
	Sources []string `toml:"sources"`

	// Prefixes are extra regular expressions that, followed directly by
	// page.html, count as a link into the site: `SiteURL\s*\+\s*"` for Go
	// code that builds the URL from a constant.
	Prefixes []string `toml:"prefixes"`
}

// LLMsConfig replaces the generic prose in the machine-readable files.
type LLMsConfig struct {
	// About is the paragraph in llms.txt after the summary line.
	About string `toml:"about"`

	// FullNote is the paragraph at the top of llms-full.txt that says where
	// the pages come from.
	FullNote string `toml:"full_note"`

	// IndexNote is the body of llms.txt's "How this is generated" section.
	IndexNote string `toml:"index_note"`

	// RobotsNote is the comment at the end of robots.txt's header.
	RobotsNote string `toml:"robots_note"`
}

// Site is a resolved config: every default filled in and every path absolute
// or checked.
type Site struct {
	Root, Out                string
	Name, Repo, Branch, Base string
	License, Screens         string
	Pages                    []Page
	Check                    CheckConfig
	LLMs                     LLMsConfig

	// Config is the file this was loaded from, empty when discovered.
	Config string
}

// Page is a resolved page.
type Page struct{ PageConfig }

// Home reports whether this is the home page, which the wordmark links to.
func (p Page) Home() bool { return p.Out == "index.html" }

// InHeader reports whether the page has its own header entry.
func (p Page) InHeader() bool { return !p.Home() && p.Parent == "" }

// DefaultConfig is the file Load looks for when given no path.
const DefaultConfig = "docsite.toml"

// Load reads a config file and resolves it. With configPath empty it uses
// DefaultConfig in root when that exists and discovers everything otherwise.
// A non-empty root overrides the config's own.
func Load(configPath, root string) (*Site, error) {
	var cfg Config
	base := root
	if configPath == "" && root != "" {
		if _, err := os.Stat(filepath.Join(root, DefaultConfig)); err == nil {
			configPath = filepath.Join(root, DefaultConfig)
		}
	} else if configPath == "" {
		if _, err := os.Stat(DefaultConfig); err == nil {
			configPath = DefaultConfig
		}
	}
	if configPath != "" {
		md, err := toml.DecodeFile(configPath, &cfg)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", configPath, err)
		}
		// A misspelt key would otherwise be ignored, and the setting it was
		// meant to change silently keep its default.
		if und := md.Undecoded(); len(und) > 0 {
			var keys []string
			for _, k := range und {
				keys = append(keys, k.String())
			}
			return nil, fmt.Errorf("%s: unknown keys: %s", configPath, strings.Join(keys, ", "))
		}
		if root == "" {
			base = filepath.Join(filepath.Dir(configPath), cfg.Root)
		}
	}
	if base == "" {
		base = "."
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	s, err := resolve(cfg, abs)
	if err != nil {
		return nil, err
	}
	s.Config = configPath
	return s, nil
}

func resolve(cfg Config, root string) (*Site, error) {
	s := &Site{
		Root: root, Name: cfg.Name, Repo: strings.TrimSuffix(cfg.Repo, "/"), Branch: cfg.Branch,
		Base: cfg.Base, License: cfg.License, Screens: cfg.Screens, Check: cfg.Check, LLMs: cfg.LLMs,
	}
	s.Out = cfg.Out
	if s.Out == "" {
		s.Out = "_site"
	}
	if !filepath.IsAbs(s.Out) {
		s.Out = filepath.Join(root, s.Out)
	}
	if s.Branch == "" {
		s.Branch = "main"
	}
	if s.Repo == "" {
		s.Repo = gitHubRemote(root)
	}
	if s.Base == "" {
		s.Base = pagesURL(s.Repo)
	}
	if s.Base != "" {
		s.Base = strings.TrimSuffix(s.Base, "/") + "/"
	}
	if s.License == "" {
		s.License = "License"
	}
	if s.Screens == "" {
		if fi, err := os.Stat(filepath.Join(root, "docs", "screens")); err == nil && fi.IsDir() {
			s.Screens = "docs/screens"
		}
	}
	if len(s.Check.Sources) == 0 {
		s.Check.Sources = []string{"README.md", "docs/**/*.md"}
	}

	pages := cfg.Pages
	if len(pages) == 0 {
		found, err := discover(root)
		if err != nil {
			return nil, err
		}
		pages = found
	}
	if err := s.setPages(pages); err != nil {
		return nil, err
	}
	return s, nil
}

// discover is the site without a config: README.md, then docs/*.md by name.
// Nothing deeper is scanned and nothing else at the root, so CLAUDE.md and
// the like are never published by accident.
func discover(root string) ([]PageConfig, error) {
	var out []PageConfig
	if _, err := os.Stat(filepath.Join(root, "README.md")); err == nil {
		out = append(out, PageConfig{Src: "README.md"})
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, PageConfig{Src: "docs/" + n})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no README.md and no docs/*.md: nothing to publish", root)
	}
	return out, nil
}

var outName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.html$`)

func (s *Site) setPages(cfgs []PageConfig) error {
	seen := map[string]string{}
	for i, c := range cfgs {
		p := Page{PageConfig: c}
		label := c.Src
		switch {
		case c.Src != "" && c.Generate != nil:
			return fmt.Errorf("page %d (%s): has both src and generate; a page comes from one place", i+1, c.Src)
		case c.Src == "" && c.Generate == nil:
			return fmt.Errorf("page %d (%s): needs src or generate", i+1, c.Out)
		case c.Generate != nil:
			label = c.Out
			if err := c.Generate.validate("generate", "markdown", "openapi"); err != nil {
				return fmt.Errorf("page %s: %w", c.Out, err)
			}
			if c.Out == "" {
				return fmt.Errorf("page %d: a generated page needs out", i+1)
			}
		}
		if c.Foot != nil {
			if err := c.Foot.validate("foot", "html"); err != nil {
				return fmt.Errorf("page %s: %w", label, err)
			}
		}
		if p.Out == "" {
			p.Out = defaultOut(c.Src)
		}
		// Flat, lower-case names: pages link each other by bare name, and the
		// checks match fragment links by that shape.
		if !outName.MatchString(p.Out) {
			return fmt.Errorf("page %s: out %q must be a lower-case name ending in .html, with no directory", label, p.Out)
		}
		if prev, dup := seen[p.Out]; dup {
			return fmt.Errorf("pages %s and %s are both published as %s", prev, label, p.Out)
		}
		seen[p.Out] = label
		s.Pages = append(s.Pages, p)
	}
	for _, p := range s.Pages {
		if p.Parent == "" {
			continue
		}
		var parent *Page
		for i := range s.Pages {
			if s.Pages[i].Out == p.Parent {
				parent = &s.Pages[i]
			}
		}
		if parent == nil || !parent.InHeader() {
			return fmt.Errorf("page %s: parent %q is not a page with a header entry", p.Out, p.Parent)
		}
	}
	return nil
}

func (h *Hook) validate(field string, formats ...string) error {
	if (len(h.Run) == 0) == (h.File == "") {
		return fmt.Errorf("%s: needs exactly one of run or file", field)
	}
	if h.Format == "" {
		h.Format = formats[0]
	}
	for _, f := range formats {
		if h.Format == f {
			return nil
		}
	}
	return fmt.Errorf("%s: format %q is not one of %s", field, h.Format, strings.Join(formats, ", "))
}

// defaultOut is the published name of a markdown file.
func defaultOut(src string) string {
	if src == "README.md" {
		return "index.html"
	}
	base := strings.TrimSuffix(path.Base(src), path.Ext(src))
	return strings.ToLower(base) + ".html"
}

// fillDefaults sets the titles, labels, blurbs and name that come from the
// markdown, once every page's markdown is in hand.
func (s *Site) fillDefaults(raw map[string][]byte) {
	for i := range s.Pages {
		p := &s.Pages[i]
		md := raw[p.Out]
		if p.Title == "" {
			p.Title = firstH1(md)
		}
		if p.Title == "" {
			p.Title = strings.TrimSuffix(p.Out, ".html")
		}
		if p.Blurb == "" {
			p.Blurb = firstSentence(firstParagraph(md))
		}
		if p.InHeader() && p.Nav == "" {
			p.Nav = p.Title
		}
	}
	if s.Name == "" {
		for _, p := range s.Pages {
			if p.Home() {
				s.Name = p.Title
			}
		}
	}
	if s.Name == "" {
		s.Name = filepath.Base(s.Root)
	}
}

// firstH1 is the text of the first "# " line outside a code fence.
func firstH1(md []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	fence := false
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
			continue
		}
		if !fence && strings.HasPrefix(l, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "# "))
		}
	}
	return ""
}

// firstParagraph is the first paragraph of prose, joined onto one line,
// skipping what summaryFrom skips.
func firstParagraph(md []byte) string {
	first := summaryFrom(md)
	if first == "" {
		return ""
	}
	lines := strings.Split(string(md), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != first {
			continue
		}
		para := []string{first}
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				break
			}
			para = append(para, strings.TrimSpace(next))
		}
		return strings.Join(para, " ")
	}
	return first
}

// firstSentence cuts a paragraph at its first full stop followed by a space,
// and strips the markdown a meta description would show literally.
func firstSentence(s string) string {
	s = mdLinkText.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("`", "", "**", "", "__", "").Replace(s)
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return strings.TrimSpace(s)
}

var mdLinkText = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// gitHubRemote is origin's web URL when origin is on GitHub, else empty.
func gitHubRemote(root string) string {
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	u := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	if rest, ok := strings.CutPrefix(u, "git@github.com:"); ok {
		return "https://github.com/" + rest
	}
	if strings.HasPrefix(u, "https://github.com/") {
		return u
	}
	return ""
}

// pagesURL is where GitHub Pages publishes a GitHub repository's site.
func pagesURL(repo string) string {
	rest, ok := strings.CutPrefix(repo, "https://github.com/")
	if !ok {
		return ""
	}
	owner, name, ok := strings.Cut(rest, "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return "https://" + strings.ToLower(owner) + ".github.io/" + name + "/"
}
