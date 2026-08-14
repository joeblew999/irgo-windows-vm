// Command site renders the repository's markdown into a static site.
//
// It generates; it does not author. Every sentence on the site comes from a
// markdown file that already exists and is already the source of truth for that
// subject — README for what this is, RESULTS for what was measured, UPSTREAM
// for what was fixed where, AGENTS for how the code works. Writing the site by
// hand would make a second copy of all four, and a second copy is a second
// thing to update and the one that goes stale: an example README in this
// repository did exactly that, naming four tasks that no longer existed and a
// command renamed two commits earlier.
//
// So if the site is wrong, the markdown is wrong. Fix it there — with the one
// exception this file also builds: the command reference has no source file and
// is captured from the binary, so a wrong flag on that page is a bug in Go.
//
// That claim was not true when it was first made. The template carried a
// paragraph describing what the screenshots showed, which appeared in no
// markdown file — so the repository could change what it proves while the site
// went on asserting the old thing, which is precisely the failure this design
// exists to prevent. It lives in README.md now.
//
// What the template still supplies is labels, not statements: the navigation
// titles and the pages' meta descriptions, below, and a heading over the
// screenshot gallery. Those name things rather than claim anything about them,
// and a nav cannot be generated from prose.
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ghtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed page.tmpl
var pageTmpl string

//go:embed style.css
var styleCSS []byte

// pages is the site, declared once. Order is the navigation order.
//
// Each entry names a markdown file in the repository root and the page it
// becomes. Adding a page means adding a line here; there is nowhere else to
// change, and nothing is discovered by scanning a directory — a site that
// publishes whatever happens to be lying around would have published CLAUDE.md.
var pages = []struct {
	Src, Out, Title, Blurb string
}{
	{"README.md", "index.html", "irgo-windows-vm", "What it is and what it is for"},
	{"RESULTS.md", "results.html", "Results", "What has been measured, dated"},
	{"UPSTREAM.md", "upstream.html", "Upstream", "What was found, and where it was fixed"},
	{"AGENTS.md", "agents.html", "Agents", "How the code is organised, and every trap that cost hours"},
	{"CONTRIBUTING.md", "contributing.html", "Contributing", "Setup, what to run, how to land a change"},

	// Generated, not read from disk. Src is empty and reference.go builds the
	// markdown by running the binary — see generateReference.
	{"", "reference.html", "Commands", "Every command and every flag, captured from the binary"},
}

type nav struct {
	Title, Href, Blurb string
	Current            bool
}

type page struct {
	Title, Blurb string
	Body         template.HTML
	Nav          []nav
	Repo         string
	Screens      []string

	// Source is the markdown file this page was rendered from, empty for the
	// one page captured from the binary.
	//
	// The template needs it because the footer used to tell every page's reader
	// that "if this page is wrong, the markdown is wrong" — which is false on
	// the command reference, on the very page whose own body says it was
	// captured from the binary. Somebody finding a wrong default was being sent
	// to edit a file that does not exist.
	Source string
}

func main() {
	root := flag.String("root", "..", "repository root to read markdown from")
	out := flag.String("out", "dist", "directory to write the site into")
	repo := flag.String("repo", "https://github.com/joeblew999/irgo-windows-vm", "repository URL")
	base := flag.String("base", "https://joeblew999.github.io/irgo-windows-vm/", "where the site is published; llms.txt links are absolute because whatever reads them has no page to resolve against")
	serve := flag.Bool("serve", false, "after building, serve it for checking locally")
	port := flag.Int("port", 8127, "port for -serve")
	flag.Parse()

	if err := build(*root, *out, *repo, *base); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if !*serve {
		return
	}

	// Serving lives here rather than in a second command, because it must serve
	// what was just built. A separate server could be pointed at a stale dist
	// from an earlier run, and checking a site that no longer matches the
	// markdown is worse than not checking it.
	freePort(*port)
	addr := fmt.Sprintf("localhost:%d", *port)
	fmt.Printf("\n  http://%s  — ctrl-c to stop\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.FileServer(http.Dir(*out)),
		ReadHeaderTimeout: 5 * time.Second, // a bare ListenAndServe has no timeouts at all
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// freePort stops whatever is already listening on the port, and says what it
// stopped.
//
// A previous run left in the background is the normal case: ctrl-c in one
// terminal, a `&` forgotten in another, and the next `site:serve` dies with
// "address already in use" while the page you are looking at is served by the
// old build. That is worse than an error — the site looks stale rather than
// broken, and the fix is invisible.
//
// It names the process before killing it rather than killing silently, because
// this will one day match something that is not a previous copy of this
// server, and the only defence against that is saying so.
//
// Never fatal: if lsof is missing or the kill is refused, ListenAndServe
// reports the real problem a moment later.
func freePort(port int) {
	out, err := exec.Command("lsof", "-nP", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return // nothing listening, or no lsof: either way there is nothing to do
	}
	for _, pid := range strings.Fields(string(out)) {
		name := "?"
		if b, nErr := exec.Command("ps", "-p", pid, "-o", "comm=").Output(); nErr == nil {
			name = strings.TrimSpace(string(b))
		}
		fmt.Printf("  port %d was held by %s (pid %s) — stopping it\n", port, filepath.Base(name), pid)
		if kErr := exec.Command("kill", pid).Run(); kErr != nil {
			fmt.Fprintf(os.Stderr, "  could not stop pid %s: %v\n", pid, kErr)
			continue
		}
	}
	// The socket is not free the instant the process is signalled.
	time.Sleep(300 * time.Millisecond)
}

func build(root, out, repo, siteURL string) error {
	// Rebuilt from scratch every time. Leaving the previous run's files in
	// place means a page that has been deleted stays published, which is the
	// same class of problem as a stale copy: the site says something the
	// repository no longer does.
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	screens, err := copyScreens(root, out)
	if err != nil {
		return err
	}

	tmpl, err := template.New("page").Parse(pageTmpl)
	if err != nil {
		return err
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM), // tables: half this repo's docs are tables
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(ghtml.WithUnsafe()),
	)

	var corpus []corpusEntry
	for _, p := range pages {
		var raw []byte
		if p.Src == "" {
			// Generated by running the binary. Fatal on any failure, for the
			// same reason a missing source file is: a page that loses a command
			// still renders, and looks right.
			g, gErr := generateReference(root)
			if gErr != nil {
				return gErr
			}
			raw = g
		} else {
			src := filepath.Join(root, p.Src)
			r, rErr := os.ReadFile(src)
			if rErr != nil {
				// Named, and fatal. A missing source silently producing a
				// shorter site is how a page disappears without anyone
				// noticing.
				return fmt.Errorf("reading %s: %w", src, rErr)
			}
			raw = r
		}

		// The rewritten markdown, named rather than passed inline, because the
		// corpus is built from exactly what the HTML is built from. Two
		// traversals of `pages` would be two chances to skip a page; this is
		// one pass producing both renderings.
		body := rewriteLinks(raw, repo)

		var buf bytes.Buffer
		if cErr := md.Convert(body, &buf); cErr != nil {
			return fmt.Errorf("converting %s: %w", p.Src, cErr)
		}
		corpus = append(corpus, corpusEntry{Title: p.Title, Out: p.Out, Blurb: p.Blurb, Markdown: body})

		var navs []nav
		for _, q := range pages {
			navs = append(navs, nav{Title: q.Title, Href: q.Out, Blurb: q.Blurb, Current: q.Out == p.Out})
		}

		var rendered bytes.Buffer
		data := page{Title: p.Title, Blurb: p.Blurb, Body: template.HTML(buf.String()), Nav: navs, Repo: repo, Source: p.Src}
		if p.Out == "index.html" {
			data.Screens = screens
		}
		if eErr := tmpl.Execute(&rendered, data); eErr != nil {
			return fmt.Errorf("rendering %s: %w", p.Out, eErr)
		}
		if wErr := os.WriteFile(filepath.Join(out, p.Out), rendered.Bytes(), 0o644); wErr != nil {
			return wErr
		}
		from := p.Src
		if from == "" {
			from = "(the binary)"
		}
		fmt.Printf("  %-18s <- %s\n", p.Out, from)
	}

	if err := os.WriteFile(filepath.Join(out, "style.css"), styleCSS, 0o644); err != nil {
		return err
	}

	// The same pages again, as one file and as an index of themselves.
	//
	// base has a trailing slash so the links below concatenate cleanly, and is
	// absolute because llms.txt is read by things that did not fetch it from a
	// browser and have no document to resolve against.
	base := strings.TrimSuffix(siteURL, "/") + "/"
	summary := summaryFrom(indexPage(corpus).Markdown)
	if err := os.WriteFile(filepath.Join(out, corpusFull), renderCorpusFull(corpus, base), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, corpusIndex), renderCorpusIndex(corpus, base, summary), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, sitemapFile), renderSitemap(corpus, base), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, robotsFile), renderRobots(base), 0o644); err != nil {
		return err
	}
	fmt.Printf("  %-18s <- %d pages, one file\n", corpusFull, len(corpus))
	for _, f := range []string{corpusIndex, sitemapFile, robotsFile} {
		fmt.Printf("  %-18s <- the same list\n", f)
	}

	// Tells GitHub Pages not to run Jekyll over the output. Without it, Pages
	// ignores any file or directory whose name starts with an underscore, which
	// is a silent 404 for whatever happened to be named that way.
	if err := os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644); err != nil {
		return err
	}

	fmt.Printf("  %-18s (%d screenshots)\n", "screens/", len(screens))
	return nil
}

// anyLink matches every markdown link target. Which ones to rewrite is decided
// in code, not by the pattern — see hasScheme.
var anyLink = regexp.MustCompile(`\]\(([^)#]*?)(#[^)]*)?\)`)

// hasScheme reports whether a link target is absolute: https:, mailto:, and so
// on. RFC 3986 says a scheme is a letter followed by letters, digits, +, - or .
// up to the first colon.
//
// This decides what is left alone, and the first version got it wrong in a way
// that only shows on the published site. The pattern excluded ":" as the FIRST
// character of a target, which was meant to skip absolute URLs — but in
// "https://example.com" the colon is the sixth character, so every external
// link matched as a repository path and was rewritten to
//
//	https://github.com/OWNER/REPO/blob/main/https://example.com
//
// Every outbound link on the site was broken. The link checker did not catch it
// because the result is a syntactically fine absolute URL, and it only inspects
// local ones.
func hasScheme(target string) bool { return schemeRE.MatchString(target) }

var schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)

// rewriteLinks points every in-repository link at wherever that thing actually
// is once the site is built. Three destinations, and getting any of them wrong
// is a 404 on a page that looked fine locally:
//
//   - a file that becomes a page  -> that page          (AGENTS.md -> agents.html)
//   - a published screenshot      -> the published copy (docs/screens/x.png -> screens/x.png)
//   - anything else in the repo   -> the repository     (LICENSE -> github.com/.../blob/main/LICENSE)
//
// The third case was originally left alone, on the reasoning that a <base> tag
// would resolve it against the repository. It does not: <base href="./"> is the
// site's own root, so `LICENSE` resolved to a file the site does not publish
// and the only broken link on the site was the licence.
func rewriteLinks(raw []byte, repo string) []byte {
	generated := map[string]string{}
	for _, p := range pages {
		generated[p.Src] = p.Out
	}
	return anyLink.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := anyLink.FindSubmatch(m)
		target, anchor := string(sub[1]), string(sub[2])

		// Left exactly as written: anything absolute, a root-relative path, and
		// a bare anchor like (#evidence).
		if target == "" || hasScheme(target) || strings.HasPrefix(target, "/") {
			return m
		}
		if out, ok := generated[target]; ok {
			return []byte("](" + out + anchor + ")")
		}
		if rest, ok := strings.CutPrefix(target, "docs/screens/"); ok {
			return []byte("](screens/" + rest + anchor + ")")
		}
		return []byte("](" + repo + "/blob/main/" + target + anchor + ")")
	})
}

// copyScreens publishes the committed screenshots and returns their names.
//
// These are the evidence the project turns on: a Windows desktop that a Mac
// built and installed unattended, and the failure that put three Bing tabs on
// it. Prose claiming both is worth much less than the pictures.
func copyScreens(root, out string) ([]string, error) {
	srcDir := filepath.Join(root, "docs", "screens")
	dstDir := filepath.Join(out, "screens")

	// Walked, not listed. The screenshots are filed by what they show — vm/ for
	// the machine's own lifecycle, glaze/ for a program running on it — and a
	// flat ReadDir skips directories, so every one of them would have silently
	// vanished from the site the moment they were sorted into folders.
	//
	// Subdirectories are preserved in the output, so docs/screens/vm/x.png is
	// published as screens/vm/x.png and the markdown's link rewriting stays a
	// straight prefix swap.
	var names []string
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".png") {
			return nil
		}
		rel, rErr := filepath.Rel(srcDir, p)
		if rErr != nil {
			return rErr
		}
		dst := filepath.Join(dstDir, rel)
		if mErr := os.MkdirAll(filepath.Dir(dst), 0o755); mErr != nil {
			return mErr
		}
		if cErr := copyFile(p, dst); cErr != nil {
			return cErr
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
	sort.Strings(names) // glaze/ before vm/, and stable between runs
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
	if _, cErr := io.Copy(f, in); cErr != nil {
		_ = f.Close() // already failing
		return cErr
	}
	// Checked, because this is a write: a short copy produces a truncated PNG
	// that renders as a broken image rather than as an error.
	return f.Close()
}
