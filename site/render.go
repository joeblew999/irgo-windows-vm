package main

// Markdown to HTML: which goldmark extensions this site uses, and the three
// small AST changes it makes on top of them.
//
// Everything here changes how a page LOOKS, never what it says. The corpus
// (llms.txt, llms-full.txt, the per-page .md files) is built from the same
// rewritten markdown before any of this runs, so nothing added here can make
// the HTML and the plain text disagree about content.
//
// Heading IDs are the one thing on a page other pages depend on —
// UPSTREAM.md#utm, USING.md#what-it-exits-with — and nothing here
// computes them. goldmark's parser.WithAutoHeadingID still does, from the raw
// heading line, before any transformer below runs. The anchor links, the table
// of contents and the heading dates all READ the ID the parser assigned; none
// of them writes one. TestEveryAnchorResolves is what notices if that stops
// being true.

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"regexp"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	alerts "github.com/thiagokokada/goldmark-gh-alerts"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"go.abhg.dev/goldmark/anchor"
	"go.abhg.dev/goldmark/toc"
)

// Chroma's two GitHub styles, one per colour scheme. Written out as CSS
// classes by syntaxCSS rather than inlined into every <span>, because inline
// colours cannot follow prefers-color-scheme: a light theme's dark-blue
// keyword is unreadable on a dark background, and the page has no way to
// change an inline style.
const (
	lightStyle = "github"
	darkStyle  = "github-dark"
)

// syntaxFile is the highlighting stylesheet the build writes beside style.css.
const syntaxFile = "syntax.css"

// newMarkdown is the one place the parser is configured.
func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM, // tables: half this repo's docs are tables
			extension.Footnote,

			// Curly quotes, apostrophes and ellipses — but not dashes or
			// angle quotes. This is documentation for a command-line tool:
			// `--save-state` written outside backticks would become an en
			// dash followed by "save-state", a flag nobody can type. The docs
			// already use real em-dashes, so the substitution buys nothing.
			extension.NewTypographer(extension.WithTypographicSubstitutions(extension.TypographicSubstitutions{
				extension.EnDash:          nil,
				extension.EmDash:          nil,
				extension.LeftAngleQuote:  nil,
				extension.RightAngleQuote: nil,
			})),

			// A "#" on every heading, linking to itself, so a section can be
			// linked to without reading the page source for its ID. Before the
			// text, and hung in the margin by the stylesheet: after it, it
			// would land beside a dated heading's label rather than its title.
			&anchor.Extender{
				Texter:     anchor.Text("#"),
				Position:   anchor.Before,
				Attributer: anchor.Attributes{"class": "anchor", "aria-hidden": "true", "tabindex": "-1"},
			},

			// Only blocks that name a language are highlighted — sh, go and
			// json today. Unlabelled blocks are command output and stay plain: guessing a
			// lexer for "PASS: JS -> Go" colours it as whatever chroma thinks
			// it resembles, which is noise pretending to be meaning.
			highlighting.NewHighlighting(
				highlighting.WithStyle(lightStyle),
				highlighting.WithGuessLanguage(false),
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
				highlighting.WithWrapperRenderer(codeWrapper),
			),

			// > [!NOTE] and friends, rendered the way GitHub renders them, so
			// a callout written for the repository reads the same here.
			&alerts.GhAlerts{},

			decorations{},
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(ghtml.WithUnsafe()),
	)
}

// renderedPage is one page's HTML: the article, and its table of contents.
type renderedPage struct {
	Body template.HTML

	// TOC is the page's h2 and h3 headings as a nested list, or empty when
	// there are too few to be worth navigating.
	TOC template.HTML
}

// minTOCEntries is the smallest table of contents worth showing. A page with
// two sections does not need a sidebar to find them.
const minTOCEntries = 3

// renderMarkdown parses once and renders twice: the article, and the table of
// contents from the same tree, so every TOC link names an ID that the article
// really carries.
func renderMarkdown(md goldmark.Markdown, src []byte) (renderedPage, error) {
	doc := md.Parser().Parse(text.NewReader(src))

	var body bytes.Buffer
	if err := md.Renderer().Render(&body, src, doc); err != nil {
		return renderedPage{}, err
	}
	out := renderedPage{Body: template.HTML(body.String())}

	tree, err := toc.Inspect(doc, src, toc.MinDepth(2), toc.MaxDepth(3), toc.Compact(true))
	if err != nil {
		return renderedPage{}, fmt.Errorf("building the table of contents: %w", err)
	}
	cleanTOC(tree.Items, headingTitles(doc, src))
	if countTOC(tree.Items) < minTOCEntries {
		return out, nil
	}
	var list bytes.Buffer
	if err := md.Renderer().Render(&list, src, toc.RenderList(tree)); err != nil {
		return renderedPage{}, fmt.Errorf("rendering the table of contents: %w", err)
	}
	out.TOC = template.HTML(list.String())
	return out, nil
}

func countTOC(items toc.Items) int {
	n := 0
	for _, it := range items {
		if len(it.ID) > 0 {
			n++
		}
		n += countTOC(it.Items)
	}
	return n
}

// cleanTOC fixes two things about the titles toc.Inspect collects.
//
// Dates: each entry becomes the heading's title minus its date, when it has
// one. "A self-built ISO installs Windows — verified 12 Aug 2026" is a good
// heading and a bad sidebar entry: the date is what wraps.
//
// Entities: the typographer turns an apostrophe into an ast.String holding the
// entity "&rsquo;", toc.Inspect copies that into the title verbatim, and the
// TOC renderer writes titles as raw text — which escapes the "&". So "guest's
// output" was published as "guest&rsquo;s output", visibly, in the sidebar.
// Unescaping here hands the renderer the character, which it writes as is.
func cleanTOC(items toc.Items, short map[string][]byte) {
	for _, it := range items {
		if s, ok := short[string(it.ID)]; ok {
			it.Title = s
		}
		it.Title = []byte(html.UnescapeString(string(it.Title)))
		cleanTOC(it.Items, short)
	}
}

// headingTitles maps the ID of every heading that carries a headingMeta to its
// text without it.
func headingTitles(doc ast.Node, src []byte) map[string][]byte {
	out := map[string][]byte{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		id, ok := h.AttributeString("id")
		if !ok {
			return ast.WalkSkipChildren, nil
		}
		idb, _ := id.([]byte)
		var title bytes.Buffer
		hasMeta := false
		for c := h.FirstChild(); c != nil; c = c.NextSibling() {
			if c.Kind() == kindHeadingMeta {
				hasMeta = true
				continue
			}
			writePlain(&title, src, c)
		}
		if hasMeta {
			out[string(idb)] = bytes.TrimSpace(title.Bytes())
		}
		return ast.WalkSkipChildren, nil
	})
	return out
}

func writePlain(dst *bytes.Buffer, src []byte, n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		dst.Write(n.Segment.Value(src))
	case *ast.String:
		dst.Write(n.Value)
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			writePlain(dst, src, c)
		}
	}
}

// codeWrapper puts every fenced block in a <div> that names its language, so
// the stylesheet can label it and the page script can give it a copy button
// that does not scroll away with a long line.
//
// goldmark-highlighting hands an unhighlighted block to the wrapper instead of
// writing its own <pre><code>, so the wrapper has to write them — otherwise
// every plain block on the site would lose its <pre> and collapse onto one
// line.
func codeWrapper(w util.BufWriter, ctx highlighting.CodeBlockContext, entering bool) {
	lang, hasLang := ctx.Language()
	if entering {
		_, _ = w.WriteString(`<div class="codeblock"`)
		if hasLang && len(lang) > 0 {
			_, _ = w.WriteString(` data-lang="`)
			_, _ = w.Write(util.EscapeHTML(lang))
			_ = w.WriteByte('"')
		}
		_ = w.WriteByte('>')
		if !ctx.Highlighted() {
			_, _ = w.WriteString("<pre><code>")
		}
		return
	}
	if !ctx.Highlighted() {
		_, _ = w.WriteString("</code></pre>")
	}
	_, _ = w.WriteString("</div>\n")
}

// syntaxCSS is the highlighting stylesheet: the light style, then the dark one
// inside a prefers-color-scheme query. Generated from chroma at build time
// rather than pasted into style.css, so a chroma upgrade that renames a token
// class cannot leave the stylesheet colouring classes that no longer exist.
func syntaxCSS() ([]byte, error) {
	f := chromahtml.New(chromahtml.WithClasses(true))
	var b bytes.Buffer
	b.WriteString("/* Generated by site/render.go from chroma's " + lightStyle + " and " + darkStyle + " styles. Do not edit. */\n")
	light, dark := styles.Get(lightStyle), styles.Get(darkStyle)
	// styles.Get returns the fallback rather than nil for an unknown name, so
	// a renamed style would silently produce a stylesheet in the wrong theme.
	if light.Name != lightStyle || dark.Name != darkStyle {
		return nil, fmt.Errorf("chroma has no %q or %q style (got %q, %q)", lightStyle, darkStyle, light.Name, dark.Name)
	}
	// Both scoped, not light as the default with dark overriding it. The two
	// styles do not colour the same token types: github colours punctuation
	// #1f2328 and github-dark leaves it to inherit, so with the light rules
	// unscoped every brace and colon in a dark-mode JSON block was near-black
	// on near-black. Seen in a screenshot of mcp.html, not by any test.
	b.WriteString("@media not all and (prefers-color-scheme: dark) {\n")
	if err := f.WriteCSS(&b, light); err != nil {
		return nil, err
	}
	b.WriteString("}\n")
	b.WriteString("@media (prefers-color-scheme: dark) {\n")
	if err := f.WriteCSS(&b, dark); err != nil {
		return nil, err
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// --- the site's own AST changes ----------------------------------------------

// decorations registers the two transformers below and their renderers.
type decorations struct{}

func (decorations) Extend(m goldmark.Markdown) {
	// goldmark runs transformers in ASCENDING priority — the smaller number
	// first. The heading split must run before the anchor extension (100)
	// appends its "#" link as the heading's last child, because it only looks
	// at the run of plain text at the END of a heading; at 500 it ran second,
	// found the anchor there, and silently did nothing on every page.
	m.Parser().AddOptions(parser.WithASTTransformers(
		util.Prioritized(tableScroller{}, 50),
		util.Prioritized(headingMetaTransformer{}, 50),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(decorationRenderer{}, 500),
	))
}

var (
	kindTableScroll = ast.NewNodeKind("TableScroll")
	kindHeadingMeta = ast.NewNodeKind("HeadingMeta")
	kindHeadingSep  = ast.NewNodeKind("HeadingSep")
)

// tableScroll wraps a table so it scrolls sideways inside its own box.
//
// A table cannot be made to scroll by styling the <table> alone without giving
// up table layout — `display: block` on it, which the old stylesheet used,
// makes the columns size to their content instead of the page and loses the
// full-width rule lines. The trap tables and the measurement tables here have
// five columns of prose; on a phone they must scroll inside the article, never
// widen the page.
type tableScroll struct{ ast.BaseBlock }

func (*tableScroll) Kind() ast.NodeKind { return kindTableScroll }
func (n *tableScroll) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, nil, nil)
}

type tableScroller struct{}

func (tableScroller) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	var tables []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Kind() == east.KindTable {
			tables = append(tables, n)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	// Collected first and moved after: re-parenting a node while ast.Walk is
	// iterating its siblings skips the next one.
	for _, t := range tables {
		wrap := &tableScroll{}
		t.Parent().ReplaceChild(t.Parent(), t, wrap)
		wrap.AppendChild(wrap, t)
	}
}

// headingMeta is the dated tail of a heading — "verified 12 Aug 2026" in
// "A self-built ISO installs Windows — verified 12 Aug 2026" — set apart so it
// reads as a label rather than as part of the title.
//
// RESULTS.md is a list of measurements, and every one of its sections says
// when it was measured in the heading. The date is the most important thing
// about a measurement after the number, and in a 1.35rem heading it was
// indistinguishable from the title.
type headingMeta struct{ ast.BaseInline }

func (*headingMeta) Kind() ast.NodeKind { return kindHeadingMeta }
func (n *headingMeta) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, nil, nil)
}

// datedTail matches the part of a heading after its last " — ", when that part
// contains a date written the way these docs write dates: 12 Aug 2026.
var datedTail = regexp.MustCompile(`^(.*) — ([^—]*\b\d{1,2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]* \d{4}\b[^—]*)$`)

type headingMetaTransformer struct{}

func (headingMetaTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var headings []*ast.Heading
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering {
			if h.Level >= 2 && h.Level <= 3 {
				headings = append(headings, h)
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for _, h := range headings {
		splitDatedHeading(h, src)
	}
}

// splitDatedHeading moves a heading's dated tail into a headingMeta.
//
// Only the run of plain text at the end of the heading is considered, so a
// date inside a code span, or a heading whose last element is a link, is left
// exactly as written. The separator " — " is kept inside the label, hidden
// visually, so the heading still reads as one sentence to a screen reader and
// to anything that copies it.
func splitDatedHeading(h *ast.Heading, src []byte) {
	// The trailing run of Text nodes, and where each starts in the joined text.
	var run []*ast.Text
	for c := h.LastChild(); c != nil; c = c.PreviousSibling() {
		t, ok := c.(*ast.Text)
		if !ok {
			break
		}
		run = append([]*ast.Text{t}, run...)
	}
	if len(run) == 0 {
		return
	}
	var joined []byte
	starts := make([]int, len(run))
	for i, t := range run {
		starts[i] = len(joined)
		joined = append(joined, t.Segment.Value(src)...)
	}
	m := datedTail.FindSubmatchIndex(joined)
	if m == nil {
		return
	}
	sep := m[3] // end of the title, where " — " begins

	// The Text node the separator starts in, split there if it starts mid-node.
	i := len(run) - 1
	for starts[i] > sep {
		i--
	}
	first := run[i]
	if off := sep - starts[i]; off > 0 {
		seg := first.Segment
		before := ast.NewTextSegment(text.NewSegment(seg.Start, seg.Start+off))
		first.Segment = text.NewSegment(seg.Start+off, seg.Stop)
		h.InsertBefore(h, first, before)
	}

	meta := &headingMeta{}
	h.InsertBefore(h, first, meta)
	for _, t := range run[i:] {
		h.RemoveChild(h, t)
		meta.AppendChild(meta, t)
	}

	// The separator itself, into its own hidden span. first now starts with
	// it, unless goldmark split the text inside it — then it stays visible,
	// which is untidy and still correct.
	if seg := first.Segment; seg.Len() > len(headingSepText) &&
		string(seg.Value(src)[:len(headingSepText)]) == headingSepText {
		sepNode := &headingSep{}
		sepNode.AppendChild(sepNode, ast.NewTextSegment(text.NewSegment(seg.Start, seg.Start+len(headingSepText))))
		first.Segment = text.NewSegment(seg.Start+len(headingSepText), seg.Stop)
		meta.InsertBefore(meta, first, sepNode)
	}
}

const headingSepText = " — "

// headingSep is the " — " between a title and its date: kept in the text,
// hidden from the eye, since the label's styling already separates them.
type headingSep struct{ ast.BaseInline }

func (*headingSep) Kind() ast.NodeKind { return kindHeadingSep }
func (n *headingSep) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, nil, nil)
}

type decorationRenderer struct{}

func (decorationRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindTableScroll, func(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			// Focusable and labelled, because a region that scrolls is only
			// reachable from the keyboard if it can take focus.
			_, _ = w.WriteString(`<div class="table-scroll" tabindex="0" role="region" aria-label="Table (scrolls sideways)">` + "\n")
		} else {
			_, _ = w.WriteString("</div>\n")
		}
		return ast.WalkContinue, nil
	})
	reg.Register(kindHeadingMeta, func(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.WriteString(`<span class="heading-meta">`)
		} else {
			_, _ = w.WriteString(`</span>`)
		}
		return ast.WalkContinue, nil
	})
	reg.Register(kindHeadingSep, func(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.WriteString(`<span class="visually-hidden">`)
		} else {
			_, _ = w.WriteString(`</span>`)
		}
		return ast.WalkContinue, nil
	})
}
