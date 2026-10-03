---
title: This repository
nav_order: 6
has_children: true
---

# This repository: set up, check, land a change, release

How to set up, what to run, and how to land a change. Before you write code,
read [Rules](rules.md), [Architecture](concepts/architecture.md) and
[Known traps](reference/traps.md): most of the duplication this project has had to remove
was written by someone who didn't check what already existed.

## What to read before you change something

| page | read it before you |
|---|---|
| [Rules](rules.md) | write any code: the rules, and the defect behind each |
| [Architecture](concepts/architecture.md) | change a package, a lock, a job, the data on disk, pushes, the golden image or the cache |
| [Known traps](reference/traps.md) | touch UTM, the ISO, the answer file, the guest or a window on Windows |
| [The Cloudflare Worker](worker.md) | change `worker/` |
| [Testing](guides/testing.md) | change `examples/`, or claim glaze works |
| this page | push, release, or add a page ([where it goes](#where-a-topic-goes)) |
| [Writing docs](writing.md) | write or change a page in `docs/` |
| [Using it](guides/using.md) | change what a command does for its user, or an exit code |
| [Upstream bugs](reference/upstream.md) | work around anything in glaze, native or UTM (don't: fix it there) |
| [Findings](findings.md) | state a number: what has been measured, dated |
| [Glaze status](GLAZE-STATUS.md) | say whether glaze works (generated: never edit it) |
| [VM status](VM-STATUS.md) | say whether a VM has what the project relies on (generated: never edit it) |
| [Roadmap](roadmap.md) | pick up what is next |
| [Threat model](concepts/threat-model.md) | touch the HTTP transport, or open a port in the guest |

## Set up

[mise](https://mise.jdx.dev) pins the toolchain:

```sh
mise install       # every tool pinned in mise.toml, at the versions CI uses
mise run go:check  # confirms the setup works
```

`mise install` also fetches the Worker's TinyGo (1.2 GB), binaryen, node and
wrangler ([why](worker.md#traps)). That is all you need to build and test the
Go code. Work on the VM itself also
needs macOS on Apple Silicon and UTM, which `vm-create` installs.

`mise tasks` lists every task ([what they are](#mise-tasks)).

## Before you push

Run the same two checks CI runs:

```sh
mise run go:check   # build, vet and test every module; cross-compile every target
mise run go:lint    # unused, ineffassign, staticcheck, errcheck
```

After you push, wait for CI with:

```sh
mise run ci:watch   # exits non-zero if any workflow on your commit failed
```

Run the [glaze gates](guides/testing.md#does-glaze-work) when you touch anything they
exercise, and the [cycle test](guides/testing.md#run-the-cycle-tests) for the stage you
changed.

### What the checks cover

- **`go:check` covers all four Go modules** (the root, `examples`, `docsite`
  and `worker`), checks the docs site (`docsite check`, below), and
  cross-compiles for Linux and Windows as well as macOS. Deleting
  a function from `sysfile_other.go` once passed every check being run, because
  they all ran on darwin. The module split is in
  [Architecture](concepts/architecture.md#go-modules); `go list -deps ./cmd/irgo-winvm`
  names nineteen third-party modules, and neither glaze nor native is among
  them.
- **`go:lint` pins `GOOS=darwin`**, so a Mac and the Linux CI runner lint the
  same code. On Linux `statfsAvailable` is a stub that always errors, and
  staticcheck rightly reports `fErr == nil && free < n` in `iso_create.go` as
  never true (SA4023). That failed CI on every push from e1a4243 to 79f8a55,
  while the same command passed on the Mac that wrote it.
- **Nothing that touches a real guest is in CI.** Unit tests cover the iso
  stage well, and the vm and app stages only at the edges; those paths are
  proven by running them ([Testing](guides/testing.md#run-the-cycle-tests)).
- **Wait for CI with `mise run ci:watch`**, never a hand-written `sleep` loop
  around `gh run list --commit`, which matches only the full 40-character SHA:
  a short one returns an empty list, which looks exactly like a run that
  hasn't started. There were five such loops before the task existed; two were
  wrong in the same way. It watches every workflow the commit started, not just
  `check`, exits non-zero if any failed, and takes `SHA=<commit>` for a commit
  other than HEAD.

### mise tasks

`mise tasks` lists them. `mise.toml` holds the tools, the environment and the
one-line tasks. Anything longer is an executable script in `mise-tasks/`, where
the path is the name (`mise-tasks/go/check` is `go:check`), `#MISE` lines at the
top declare its description and dependencies, and findings are comments beside
the lines they explain. Until 30 Sep 2026 all of it was shell inside TOML
strings: 516 lines that no editor, `bash -n` or shellcheck could see.

A task exists only for what the binary cannot do on its own: checks, builds,
the glaze gates, upstream work and the create-and-delete cycles. No task merely
wraps a command; call the command.

- **Tools are pinned in `mise.toml` and nowhere else**, so CI installs what a
  maintainer has; `jdx/mise-action` reads it. Every `go.mod` says `go 1.27.1` to
  match. mise's reading of the Go version from `go.mod` is deprecated (removed
  in 2026.11.0), hence the pin.
- **`go:tool` is the one build of the tool.** Every task runs `.bin/irgo-winvm`
  rather than `go run ./cmd/irgo-winvm`, and `.bin` is on `PATH` in this
  directory, so `irgo-winvm doctor` works by hand. `sources` and `outputs` let
  mise skip the build when nothing under `cmd/` or `internal/` changed. From
  977136e to 30 Sep 2026 the task built `./irgo-winvm` while `outputs` named
  `.bin/irgo-winvm`, so every task and `.mcp.json` ran whatever stale binary
  `.bin` held (in a fresh clone, none). mise warned on every run — `did not
  generate expected output` — and nobody read it. After editing `cmd/` or
  `internal/`, run any task or `mise run go:tool` before calling the binary by
  hand.
- **`site:build` renders the markdown and never hand-written pages**, so the
  site cannot drift from the repository. **`site:serve` builds and serves in one
  command** on purpose: a separate server can be pointed at a stale `dist`.
- **`vm:shots` copies the newest screenshot of each stage** from `shots/` into
  `docs/screens/vm` under its stage name. The originals carry timestamps, so no
  document can point at them.
- **`UPSTREAM_DIR`** is where the local glaze and native clones live, read by the
  `upstream:*` tasks. It was restored from the `mise.toml` deleted in e533764.

## Fix glaze and native bugs upstream

**This is non-negotiable, and it is why the project exists.** A failing probe
means a patch to [crgimenes/glaze](https://github.com/crgimenes/glaze) or
[crgimenes/native](https://github.com/crgimenes/native), never a workaround
here. A bug worked around in an example still ships to everyone using those
libraries, and the workaround hides it.

Record what you found, and where it was fixed, in [Upstream bugs](reference/upstream.md).

To build and test this repository against your local clones of glaze and native,
see [Test your own changes to glaze or native](guides/testing.md#test-your-own-changes-to-glaze-or-native).

## Commits

- **One concern per commit**, each verified on its own. A refactor landed as one
  commit can't be reviewed or bisected.
- **Say what changed and why the old code was wrong.** The commit log is the only
  record of things that cost hours and don't show in the diff, such as `utmctl`
  exiting 0 on failure, or `del` exiting 1 when a glob matches nothing.
- **Include measurements.** If you measured something to be sure, put the
  numbers in the message.
- **Correct a measurement everywhere it appears**, including
  [Findings](findings.md), which is dated on purpose.

## The docs site

`docs/` is kept the way every repository of the owner's keeps it, by
[charter](https://github.com/joeblew999/charter): the sections and the rules
for a page are in [Writing docs](writing.md), which charter writes, and every
page opens with front matter (`title`, `nav_order`, `parent`).

```sh
mise run docs:check    # charter's files are current, and docs/ has nothing a program can fault
mise run docs:setup    # write docs/_config.yml, docs/writing.md, docs/llms.txt, docs/_sass/ again
mise run docs:review   # have Claude bring docs/ into line with docs/writing.md
```

`docs:check` is not in CI: CI installs only the Go tools, and `charter docs
-check` asks GitHub for the repository's description. Run it before you push a
change to `docs/`.

<https://joeblew999.github.io/irgo-windows-vm/> is generated from the same
markdown by this repository's own generator, docsite, and published by
`pages.yml` on every push to `main`; charter's GitHub Pages site (Jekyll over
`docs/`, which `docs/_config.yml` configures) is not switched on. Why, and what
replacing docsite would take: `.plans/2026-10-03_1200_docs-site-charter.md`.
There is no separate copy to edit: if a page is wrong, fix the markdown. The
exceptions are the [command reference](#the-command-reference), the MCP page
and the Worker API page, which are generated from the code.

```sh
mise run site:serve    # build and serve at http://localhost:8127
mise run site:build    # build only, into site/dist (gitignored)
mise run site:check    # build into a temporary directory and check it
```

`site:serve` stops whatever already holds the port. Otherwise a leftover server
keeps answering and you review the *old* build without knowing.

### How it is built

The generator is **docsite** (`docsite/`, its own module, with
[its own README](https://github.com/joeblew999/irgo-windows-vm/blob/main/docsite/README.md)):
a tool for any project with a `docs/` folder, which imports nothing from this
repository and will move to a repository of its own. This repository is its
first user. What is particular to this site is in `site/`:

- **`site/docsite.toml`**: the pages in navigation order, their nav labels,
  parents and descriptions, which pages state intent, the link-check sources,
  and the prose of `llms.txt` and `robots.txt`.
- **The hooks** (`site/main.go`, run as `go run ./site <name>`): the pages
  with no markdown file, each generated from the code it describes. `reference`
  and `mcp` capture the binary; `api` reads wire's route table; `glaze-live`
  is the script that ends the Glaze status page, asking the Worker for the
  newest run.

To change the API page's generator, for example to render the OpenAPI document
(`worker/openapi.json`) instead, change that page's `generate` line in the
config: `generate = { file = "worker/openapi.json", format = "openapi" }`.

### What CI checks

`go:check` runs `docsite check` on this site. None of these failures is
visible on a page that renders, and each has happened. It fails on:

- a local link or image to a file the site doesn't publish;
- an absolute URL that has been rewritten as a repository path;
- a fragment link to a heading that doesn't exist;
- a link into the site, from `cmd/`, `internal/`, `.github/` or `docs/`, to a
  page or heading that doesn't exist. Error messages link the site, so a renamed
  heading would otherwise break them silently;
- a page that appears in the HTML site but not the corpus, or the reverse (see
  below). A corpus missing a page still looks complete;
- a heading id that differs from what plain goldmark gives, a duplicated id, a
  table outside its scroll box, a footer naming the wrong source;
- a screenshot no page mentions (see [Screenshots](#screenshots)).

### Copies for machines

Beside each HTML page, the site publishes the same page as plain markdown, with
the extension swapped (`results.html` has `results.md`). It also publishes
**`llms.txt`**, an index, and **`llms-full.txt`**, all the documentation in one
file for readers that prefer one request to many.

Point machines at these rather than at the repository's `.md` files, which lack
the command reference.

They are not a second copy. Each corpus entry is written in the same loop that
renders the HTML page, from the same markdown, in one pass over the page list
in `site/docsite.toml`.

### Where a topic goes

Each topic has one page, and every other page links to it. A new section goes
on the page for its reader:

| page | holds |
|---|---|
| `README.md` | what this is, install, a three-step quick start, links. Nothing else |
| `docs/README.md` | the home page: what it is for and who for, what is what, what is generated, every page |
| `docs/guides.md`, `docs/concepts.md`, `docs/reference.md` | a section's pages, one line each |
| `docs/getting-started.md` | requirements, every way to install, the first VM and the first program |
| `docs/guides/using.md` | each command for its user: exit codes, costs, the VM, `-gui`, the golden image, the private cache |
| `docs/guides/agents.md` | using it from an agent: MCP, HTTP, filing issues |
| `docs/guides/testing.md` | the glaze gates, the conformance suite, `examples/drive`, desktop hygiene, the cycle tests |
| `docs/concepts/architecture.md` | how the code is built: stages, packages, locks, jobs, data, pushes, the golden image and cache internals |
| `docs/worker.md` | the Cloudflare Worker |
| `docs/rules.md` | how code here is written |
| `docs/reference/traps.md` | what fails silently, one line each |
| `docs/contributing.md` | this page: setup, checks, the site, commits, releases, triage, licence |
| `docs/findings.md` | what was measured, dated. History goes here, not in the reference pages |
| `docs/reference/upstream.md` | bugs in glaze, native and UTM, and their status |
| `docs/roadmap.md`, `docs/concepts/threat-model.md` | intent, and what the HTTP transport exposes |
| `docs/GLAZE-STATUS.md`, `docs/VM-STATUS.md` | generated by `glaze-check` and `vm-check`: never edit them by hand |
| `docs/writing.md` | the rules for a page, written by charter: never edit it here |

`AGENTS.md` and `CLAUDE.md` at the root only point into `docs/`.

### Add a page

Every file above becomes a page. To add one, put it in its section's folder
with front matter ([Writing docs](writing.md#the-mechanics)), give it a row in
its section's page and in the home page's "Every page" table, and add a
`[[page]]` to `site/docsite.toml`. Nothing is discovered by scanning a directory, so nothing
is published by accident. A page with a `nav` label is in the header; one with
`parent` set is listed in the footer and lights up its parent's entry, which
keeps the header to one line at 1280 px. A page that names commands that do not
exist yet, on purpose, gets `intent = true`, which exempts it from the check
that every command the docs name exists.

### The command reference

The reference has no source file. Its hook (`go run ./site reference`)
compiles the CLI and captures `irgo-winvm help` and `-h` for every command, so no flag, default or usage
string is ever transcribed. `iso-create -fetch` computes its usage text from a
constant, so only a captured copy is correct.

It asks the binary for the list with `irgo-winvm commands`
([tooling commands](concepts/architecture.md#adding-a-command)).

### Screenshots

Published screenshots live in `docs/screens/vm/`, and the tool puts them there:
`mise run vm:shots` copies the newest shot of each stage under the stage's name.
Don't copy them in by hand.

**CI fails on a screenshot no page mentions.** A slow boot produces
`booting-3`, `booting-4` and so on, and `vm:shots` publishes whatever a run
produced. Give each new shot a caption that names its file, or delete it. A
picture nobody explains is not evidence.

## Releases

CI publishes releases. You tag them:

```sh
git tag -a v0.1.2 -m "..." && git push origin v0.1.2
```

The version comes from the tag and nowhere else, so nothing in the tree needs
editing first. Use semver: a minor bump (v0.5.0) for new commands, flags or
behaviour, a patch (v0.5.1) for fixes only.

The release notes are the header in `.goreleaser.yaml` (install, first run, MCP,
the golden image) and the commits since the last tag, grouped. Commit subjects
starting `feat:`, `fix:` or `docs:` land in New, Fixes and Documentation; the
rest are grouped by the area the subject starts with (`vm-create:`, `site:`,
`conformance:` ...). Merges, `plans:` and `GLAZE-STATUS` commits are left out.

### How a release is built

The build is [GoReleaser](https://goreleaser.com), configured in
`.goreleaser.yaml` and pinned in `mise.toml`. Edit that file to change:

- the targets (darwin arm64 and amd64 only);
- the build flags;
- the download names (`irgo-winvm-darwin-arm64`: raw binaries, not tarballs);
- the install instructions in the release notes;
- the Homebrew cask.

What a release offers a user, and where each comes from:

| install | from |
|---|---|
| `curl -fsSL …/install.sh \| sh` | `install.sh` at the root, also attached to each release (`release.extra_files`). It verifies the binary against `SHA256SUMS` before installing it |
| `brew tap joeblew999/irgo-windows-vm https://github.com/joeblew999/irgo-windows-vm && brew install --cask irgo-winvm` | the cask GoReleaser commits to `Casks/` in this repository on each release |
| `go install …/cmd/irgo-winvm@vX.Y.Z` | the module proxy; the binary reports the tag from its build info |
| the raw binary | the release's assets |

**The tap** is this repository: GoReleaser commits `Casks/irgo-winvm.rb` on each release with the
release workflow's own token, so nothing needs setting up. The cask clears the quarantine flag after
install, because the binary is not signed with an Apple Developer ID and Homebrew quarantines what a
cask downloads.

**Gatekeeper.** The binaries are ad-hoc signed and not notarized; what that
means for a user is in [Getting started](getting-started.md#install).

`release.yml` then:

1. Asks GitHub whether the tagged commit has a green `check` run, and refuses
   to publish if it doesn't. It doesn't re-run the gate: `check` has already
   passed on a Mac and on ubuntu, and re-running it took 124 s of a 171 s
   release.
2. Runs `goreleaser release --clean`, which builds, writes `SHA256SUMS`, and
   creates the GitHub release with the header from `.goreleaser.yaml` and the
   commits since the previous tag.

Running `release.yml` by hand (workflow_dispatch) is a dry run: a snapshot build
uploaded as an artefact, with nothing published.

### Build locally

`mise run go:build` runs the same build into `dist/`: each binary is
`dist/irgo-winvm_darwin_<arch>/irgo-winvm`, published as
`irgo-winvm-darwin-<arch>`, and the cask is `dist/homebrew/Casks/irgo-winvm.rb`.
To try `install.sh` against it, copy the binaries under their published names
into a directory with `dist/SHA256SUMS` and run
`IRGO_WINVM_BASE=file://<that directory> sh install.sh`.

- On a clean checkout of a tag, it builds exactly what that release published.
- Anywhere else it is a snapshot, and the binary reports `dev` (`dev-dirty` with
  uncommitted changes), never the previous tag.

The checksums are reproducible: a clean checkout of the same tag produces
byte-identical binaries. That is why `-buildvcs=false` and `mod_timestamp` are
in `.goreleaser.yaml`. If you change the build, verify it still holds: build
twice and compare `SHA256SUMS`.

The version is compiled into the binary, so it is part of those bytes, which is
why the build reads it from the tag your checkout is on. Before v0.2.1 it
didn't: CI passed `VERSION` and a hand-run build didn't, so a local build said
`dev` and hashed differently. The byte-for-byte check against a published
release is in [Findings](findings.md).

## Triage and labels

The labels are declared once, in `.github/labels.tsv`: a header row
(`name`, `color`, `description`) and one label per row, tab-separated, with no
comment lines, so GitHub shows the file as a searchable table. Then `mise run gh:labels` creates or updates them
on GitHub with `gh label create --force`. It deletes nothing. `DRY_RUN=1`
prints the commands instead of running them, and `REPO=owner/name` points it
at another repository. `cmd/irgo-winvm/issue_test.go` fails if a form,
`report -issue` or [For agents](guides/agents.md#reporting-issues) names a label the file does not define, or if a
form and `report -issue` disagree about the headings.

| label | means |
|---|---|
| `bug` | `irgo-winvm` does the wrong thing |
| `feature` | a calling repository needs something it does not do |
| `upstream-glaze`, `upstream-native`, `upstream-utm` | the bug is theirs: recorded in [Upstream bugs](reference/upstream.md), fixed there |
| `needs-triage` | nobody has looked yet; every form adds it |
| `needs-report` | a bug without the report; triage waits for it |
| `triaged` | classified, labelled, and the next step is named in a comment |
| `agent-filed` | filed by an agent for a calling repository |
| `good first issue` | small and self-contained, with the fix described |
| `duplicate`, `wontfix` | closed, with a comment linking the original or saying why |

How agents file issues is in [For agents](guides/agents.md#reporting-issues).

Triage: read the report, move an upstream bug into [Upstream bugs](reference/upstream.md)
and label it `upstream-*`, swap `needs-triage` for `triaged`, and say the next
step in a comment.

## Licence

MIT; see [LICENSE](../LICENSE). Contributions are offered under it.

The licence is a claim about the whole published binary, not just this
repository's code, so every dependency linked into it was checked. All are
permissive; nothing in the module graph is copyleft.

| licence | modules |
|---|---|
| MIT | `anchore/go-lzo`, `diskfs/go-diskfs`, `djherbis/times`, `sirupsen/logrus`, `google/jsonschema-go`, `modelcontextprotocol/go-sdk`, `segmentio/asm`, `segmentio/encoding` |
| BSD | `elliotwutingfeng/asciiset`, `google/uuid`, `pierrec/lz4`, `pkg/xattr`, `ulikunitz/xz`, `yosida95/uritemplate`, `golang.org/x/sys`, `golang.org/x/oauth2`, `golang.org/x/sync`, `golang.org/x/time` |
| Apache-2.0 | `klauspost/compress`, `lima-vm/go-qcow2reader` |

- **Nineteen modules**, up from eleven before the MCP server. The eight it added
  are `go-sdk` and what it pulls in: `jsonschema-go`, `segmentio/asm`,
  `segmentio/encoding`, `uritemplate`, `x/oauth2`, `x/sync` and `x/time`.
  `x/oauth2` arrives even if the auth package is unused; importing `mcp` is
  enough.
- **`go-sdk` is mid-relicence**: Apache-2.0 for new contributions, MIT for older
  un-relicensed ones. Both are permissive and neither adds a condition beyond
  attribution. None of the eight ships a `NOTICE` file.
- **`klauspost/compress` and `lima-vm/go-qcow2reader` are Apache-2.0**, which
  has a condition beyond attribution: carrying a `NOTICE` file. Neither ships
  one, so there is nothing to carry.
- **`go-qcow2reader`** (added 2 Oct 2026) converts Ubuntu's qcow2 cloud image
  to the raw disk a VM has. Its `go.mod` requires no module of its own.
- **This table is behind the binary.** On 2 Oct 2026 `go list -deps` named 35
  modules, 34 before `go-qcow2reader`: the R2 client (`aws-sdk-go-v2`,
  `smithy-go`), the SMB client (`cloudsoda/go-smb2` and the `jcmturner`
  modules under it), `ebitengine/purego` and others arrived without a row
  here. Their licences have not been re-checked in this table.

**If you add a dependency, re-check this table.** `go list -deps ./cmd/irgo-winvm`
lists what actually reaches a user.
