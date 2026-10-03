# docsite

Turns a project's markdown, `README.md` and a `docs/` folder, into a static
site, and checks it. Every page comes from a markdown file that already exists,
or from a hook the project runs to generate it. If the site is wrong, fix the
markdown or the hook. There is no copy of the docs to keep in sync.

```sh
go install github.com/joeblew999/irgo-windows-vm/docsite/cmd/docsite@latest

docsite build     # render into _site/ (or the config's out)
docsite serve     # build, then serve at http://localhost:8127
docsite check     # build into a temp dir and check it; exits 1 on any problem
docsite pages     # list the pages (-intent: only those marked intent)
```

Every command takes `-config`, `-root`, `-out` and `-sha`. Run `docsite help`
for the full list.

## Without a config

`README.md` becomes `index.html`, and each `docs/*.md` becomes a page named
after its file in lower case (`docs/USING.md` becomes `using.html`). Pages
follow in file-name order. Each page's title is its first H1, and its
description is its first sentence. Nothing deeper than `docs/` is published,
and nothing else from the root, so `CLAUDE.md` is never published by accident.

The repository URL is read from `git remote get-url origin` when that is
GitHub, and the published URL is then the GitHub Pages URL. Without either,
links to repository files are left as written, so `check` reports them, and
no sitemap is written, because a sitemap's URLs must be absolute.

## What a site has

- **Pages** with a header nav that fits on one line (pages can sit under a
  parent entry), a footer listing every page, and a contents sidebar for pages
  with three or more sections, folded into a disclosure on a phone.
- **A `#` link** on every heading. Heading ids are what plain goldmark gives,
  and `check` fails if anything changes one.
- **Highlighting** for fenced code that names a language, done with chroma's
  CSS classes in `syntax.css`: `github` for light mode and `github-dark` for
  dark. Unlabelled blocks stay plain. Every block gets a copy button.
- **Front matter left out.** A page that opens with a `---` block (the
  `title`, `nav_order` and `parent` GitHub Pages' Jekyll reads) is rendered
  without it.
- **GitHub alerts** (`> [!NOTE]`), tables that scroll sideways in their own
  box, dates in headings (`— verified 12 Aug 2026`) set apart as labels, and
  curly quotes. Dashes are left alone, so `--flag` stays typeable.
- **Links rewritten** the way GitHub resolves them. A link to a markdown file
  that is a page goes to that page. A link to a screenshot goes to the
  published copy. Any other repository file goes to `<repo>/blob/<branch>/`.
- **For machines:** each page again as `.md`, plus `llms.txt` (an index),
  `llms-full.txt` (every page in one file), `sitemap.xml` and `robots.txt`.
- **A footer** that names the source file, or for a generated page says what
  generates it, and a build stamp (commit and time; `SOURCE_DATE_EPOCH` makes
  the build reproducible).

## Config: `docsite.toml`

Optional. Every key may be left out. Paths are from `root`, and `root` is
relative to the config file. A misspelt key is an error.

```toml
name = "widget"                   # wordmark and <title> tail; default: home page title
root = "."                        # project directory
out = "_site"                     # output directory
repo = "https://github.com/me/widget"
branch = "main"
base = "https://me.github.io/widget/"   # published URL, for llms.txt and the sitemap
license = "MIT"                   # footer label for LICENSE, shown if the file exists
screens = "docs/screens"          # images published under screens/

[check]
# Files outside the site that link into it by its base URL. Each link must
# name a published page and, with a #fragment, one of its headings.
# ** and {a,b} work. Default: README.md and docs/**/*.md.
sources = ["{cmd,internal}/**/*.go", "README.md"]
# Extra regexps that, followed by page.html, count as a link into the site.
prefixes = ['SiteURL\s*\+\s*"']

[llms]          # replace the generic prose in llms.txt, llms-full.txt, robots.txt
about = "..."
full_note = "..."
index_note = "..."
robots_note = "..."

# The pages, in navigation order. Leave them all out to discover them.
[[page]]
src = "README.md"                 # out defaults to index.html: the home page

[[page]]
src = "docs/USING.md"
out = "using.html"                # default: from src
title = "Using it"                # default: the first H1
nav = "Use"                       # header label; default: title
blurb = "Each command, and what it exits with"   # default: first sentence

[[page]]
src = "docs/ROADMAP.md"
parent = "using.html"             # in the footer, lights up its parent's entry
intent = true                     # states intent, not fact; see `docsite pages -intent`

[[page]]
out = "reference.html"
title = "Commands"
footer = "Captured from the binary at build time."
generate = { run = ["go", "run", "./tools/docs", "reference"] }

[[page]]
out = "api.html"
generate = { file = "api/openapi.json", format = "openapi" }

[[page]]
src = "docs/STATUS.md"
foot = { run = ["./scripts/live-status.sh"], format = "html" }
```

## Hooks

A hook makes a page, or the end of a page, at build time. It is either
`run`, a command whose stdout is the output, or `file`, read as it is.

| `format` | for | output |
|---|---|---|
| `markdown` (default) | `generate` | a page, rendered like any other |
| `openapi` | `generate` | an OpenAPI 3 JSON document. docsite renders the endpoints, auth, parameters, bodies, answers and schemas |
| `html` | `foot` | raw HTML written before `</body>`, for a script one page needs |

Commands run in the project root with `DOCSITE_ROOT`, `DOCSITE_PAGE`,
`DOCSITE_REPO` and `DOCSITE_BASE` set, all at once. A hook that fails, or
prints nothing, fails the build and names the page. A generated page is
rewritten, rendered, put in the corpus and checked exactly like a file.

To switch a page from one generator to another, such as a hand-written API
page to a rendered OpenAPI file, change that page's `generate` line.

## What `check` verifies

It builds into a temporary directory, then reads what was built:

- every page is in the HTML, its `.md` copy, `llms-full.txt` (in order),
  `llms.txt` and the sitemap, and the sitemap parses;
- every local `href` and `src` names a file the build wrote, and no absolute
  URL has been glued onto a repository path;
- every `#fragment` link names an id on its page;
- every image in both renderings exists, and every published screenshot is
  mentioned by some page;
- no id appears twice on a page, the header never repeats a link, every table
  is in its scroll box, and each footer names the right source;
- heading ids are what plain goldmark gives;
- links into the site from the `[check] sources` files resolve.

## Dependencies

[goldmark](https://github.com/yuin/goldmark) with its GFM, footnote and
typographer extensions, [anchor](https://github.com/abhinav/goldmark-anchor)
and [toc](https://github.com/abhinav/goldmark-toc),
[goldmark-gh-alerts](https://github.com/thiagokokada/goldmark-gh-alerts),
[chroma](https://github.com/alecthomas/chroma) and
[BurntSushi/toml](https://github.com/BurntSushi/toml). All pure Go.

Code blocks go straight to chroma (`highlight.go`) rather than through
goldmark-highlighting, which has not been committed to since 2023. Rendered
side by side, the two gave the same bytes for every page of the first site
built with this.
