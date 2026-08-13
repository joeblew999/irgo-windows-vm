// Command site renders the repository's markdown into a static site.
//
// It generates; it does not author. Every word on the site comes from a file
// that already exists and is already the source of truth for that subject —
// README for what this is, RESULTS for what was measured, UPSTREAM for what was
// fixed where, AGENTS for how the code works. Writing the site by hand would
// make a second copy of all four, and a second copy is a second thing to update
// and the one that goes stale: an example README in this repository did exactly
// that, naming four tasks that no longer existed and a command renamed two
// commits earlier.
//
// So if the site is wrong, the markdown is wrong. Fix it there.
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	{"AGENTS.md", "agents.html", "Working on it", "How the code is organised, and every trap that cost hours"},
	{"CONTRIBUTING.md", "contributing.html", "Contributing", "Setup, what to run, how to land a change"},
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
}

func main() {
	root := flag.String("root", "..", "repository root to read markdown from")
	out := flag.String("out", "dist", "directory to write the site into")
	repo := flag.String("repo", "https://github.com/joeblew999/irgo-windows-vm", "repository URL")
	serve := flag.Bool("serve", false, "after building, serve it for checking locally")
	port := flag.Int("port", 8080, "port for -serve")
	flag.Parse()

	if err := build(*root, *out, *repo); err != nil {
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

func build(root, out, repo string) error {
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

	for _, p := range pages {
		src := filepath.Join(root, p.Src)
		raw, rErr := os.ReadFile(src)
		if rErr != nil {
			// Named, and fatal. A missing source silently producing a shorter
			// site is how a page disappears without anyone noticing.
			return fmt.Errorf("reading %s: %w", src, rErr)
		}

		var buf bytes.Buffer
		if cErr := md.Convert(rewriteLinks(raw, repo), &buf); cErr != nil {
			return fmt.Errorf("converting %s: %w", p.Src, cErr)
		}

		var navs []nav
		for _, q := range pages {
			navs = append(navs, nav{Title: q.Title, Href: q.Out, Blurb: q.Blurb, Current: q.Out == p.Out})
		}

		var rendered bytes.Buffer
		data := page{Title: p.Title, Blurb: p.Blurb, Body: template.HTML(buf.String()), Nav: navs, Repo: repo}
		if p.Out == "index.html" {
			data.Screens = screens
		}
		if eErr := tmpl.Execute(&rendered, data); eErr != nil {
			return fmt.Errorf("rendering %s: %w", p.Out, eErr)
		}
		if wErr := os.WriteFile(filepath.Join(out, p.Out), rendered.Bytes(), 0o644); wErr != nil {
			return wErr
		}
		fmt.Printf("  %-18s <- %s\n", p.Out, p.Src)
	}

	if err := os.WriteFile(filepath.Join(out, "style.css"), styleCSS, 0o644); err != nil {
		return err
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

// localLink matches a markdown link to something in the repository: anything
// that is not absolute, not a bare anchor, and not a mail link.
var localLink = regexp.MustCompile(`\]\(([^)#:][^)#]*?)(#[^)]*)?\)`)

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
	return localLink.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := localLink.FindSubmatch(m)
		target, anchor := string(sub[1]), string(sub[2])

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
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	dstDir := filepath.Join(out, "screens")
	if mErr := os.MkdirAll(dstDir, 0o755); mErr != nil {
		return nil, mErr
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		if cErr := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name())); cErr != nil {
			return nil, cErr
		}
		names = append(names, e.Name())
	}
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
