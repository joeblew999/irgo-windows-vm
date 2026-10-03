# The docs site: charter's GitHub Pages site in place of docsite

State: proposal, waiting on the owner. Nothing here is built.

## Symptom

`docs/` now has charter's layout (sections, front matter, `docs/writing.md`,
`docs/_config.yml`, `mise run docs:check`), but the published site is still
docsite's: `pages.yml` builds `site/dist` with `mise run site:build` and
deploys it as the Pages artifact. charter's way is GitHub Pages rendering
`docs/` itself with Jekyll and the Just the Docs theme (`docs/_config.yml`),
switched on by `mise run docs:pages`. One repository has one Pages site, so it
is one or the other.

## Why docsite still publishes it (3 Oct 2026)

Switching is not a docs change. What depends on docsite today:

| what | depends on docsite how | measured |
|---|---|---|
| Released binaries' messages | link `using.html#what-it-exits-with`, `using.html#setting-up-the-bucket`, `agents.html#…`, `threat-model.html`, `mcp.html`, `reference.html`, `api.html` (`utmvm.SiteURL`, issue forms, `.github/actions/run/action.yml`). Jekyll would publish `docs/guides/using.md` as `guides/using.html` | `docsite check`: 28 links into the site from 196 source files |
| Heading ids | the links above and 1116 fragment links rely on goldmark's ids (`docsite check` fails if one moves). Jekyll's kramdown makes its own ids; whether they match here (`#1b-glaze--absolute-app-urls-…`, `#why--gui-exists`, dated headings) is not measured | not measured |
| Three pages with no markdown | Commands, MCP and Worker API are generated at build time by `site/` hooks; Jekyll on Pages runs no code | |
| The Glaze status page | `pages.yml` imports the latest conformance run's record and screenshots (`glaze-check -import`) before building, and the `glaze-live` hook ends the page | |
| Machine copies | docsite publishes each page as `.md`, `llms.txt` and `llms-full.txt` with the generated pages in them; charter's `llms.txt` links the raw markdown on GitHub, which has no generated pages, and has no `llms-full.txt` | |
| The Worker | serves `site/dist` as its static assets (`docs/worker.md`, "The site") | |

Changing release assets, workflows or the Worker is out of scope for the
change that made `docs/` charter-shaped, so the two coexist: the markdown is
the same, docsite renders it (it drops the front matter, `docsite/build.go`
`readSource`), and `docs/_config.yml` waits unused.

## The change, in steps that each stand alone

1. **Commit the generated pages as markdown.** A task writes
   `docs/reference/commands.md`, `docs/reference/mcp.md` and
   `docs/reference/worker-api.md` from the same hooks, with front matter, and
   `go:check` fails if they are stale. docsite then renders them as `src`
   pages, and charter's `llms.txt` gets them.
2. **Measure the ids.** Render `docs/` with the GitHub Pages gem set (the
   `github-pages` gem in a container) and compare every heading id with
   goldmark's, as `checkHeadingIDsStable` does. Every difference is either a
   heading changed in the same commit as every link to it, or a reason to stop.
3. **Keep every published URL.** `redirect_from` (jekyll-redirect-from, one of
   the plugins GitHub Pages allows) on each page for its docsite name
   (`using.html`, `agents.html`, …). A redirect drops nothing after `#` in the
   browser, so the anchors hold if step 2 found them equal.
4. **Glaze status without a build step.** Either a workflow commits the
   imported record after each conformance run on `main`, or the page shows
   only the committed record and the Worker's live view stays on the Worker.
   The owner's choice.
5. **The Worker's assets.** It serves `site/dist` today; it would serve the
   Jekyll output, or nothing and redirect to Pages. A Worker change, on its own.
6. **Switch.** Pages source to `main` `/docs` (`gh api -X PUT repos/joeblew999/irgo-windows-vm/pages`
   with `build_type=legacy`; `mise run docs:pages` POSTs, which fails on a
   repository that has Pages), then delete `pages.yml`, `docsite/`, `site/`
   and the `site:*` tasks, and move `docsite check`'s check of links into the
   site from the code into a Go test.

## How to verify

- Every URL `docsite check` lists as a link into the site answers 200 (or
  redirects to a page that does) on the new site, with its fragment present.
- `mise run docs:check` passes, and `llms.txt` lists every page including the
  three that were generated.
- A released binary's `irgo-winvm help` links open the right section.
