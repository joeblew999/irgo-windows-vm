# Contributing

This page covers the mechanics: how to set up, what to run, and how to land a
change.

How the code is written is in [DEVELOPMENT.md](DEVELOPMENT.md). **Read it
before you write code.** Most of the duplication this project has had to remove
was written by someone who didn't check what already existed.

## Set up

[mise](https://mise.jdx.dev) pins the toolchain:

```sh
mise install       # Go and golangci-lint, at the versions CI uses
mise run go:check  # confirms the setup works
```

That is all you need to build and test the Go code. Work on the VM itself also
needs macOS on Apple Silicon and UTM, which `vm-create` installs.

`mise tasks` lists every task. Each task's name matches the command it runs.

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

Don't write your own `sleep` loop around `gh run list --commit`. It matches only
the full 40-character SHA, and a short one returns an empty list, which looks
exactly like a run that hasn't started.

`go:check` covers all **three** Go modules (the root, `examples` and `site`) and
cross-compiles for Linux and Windows as well as macOS. Deleting a function from
`sysfile_other.go` once passed every check being run, because they were all
darwin.

The modules are split so each binary carries only what it needs:

- `examples` builds against glaze and native, the libraries under test, which
  must never reach the binary users download. `go list -deps ./cmd/irgo-winvm`
  names nineteen third-party modules, and neither is among them.
- `site` needs a markdown parser the tool has no reason to ship.

More on the layout is in [DEVELOPMENT.md](DEVELOPMENT.md#repository-layout).

## Run the cycle tests

These need UTM, an Apple Silicon host and real media, so CI can't run them.
Run the one for the stage you changed.

| task | what it does | cost |
|---|---|---|
| `mise run iso:test` | deletes the ISO and rebuilds it from the `.esd` | ~50 s, and 4.9 GB of disk once (see the note in the task) |
| `mise run vm:test` | creates and deletes a VM under a disposable name | minutes; leaves running VMs alone (UTM imports the bundle, no restart) |
| `mise run app:test` | pushes a binary to the VM, runs it, removes it | ~20 s; needs a VM with Windows installed |

Use a disposable VM for anything destructive. `vm:test` already does: it builds
`irgo-test-cycle`, never your real VM. Losing a 45-minute install to a test is
not worth it.

## Does glaze work?

One test suite answers this: `examples/conformance` (the tests are listed in
[DEVELOPMENT.md](DEVELOPMENT.md#the-conformance-suite)). Run it with:

```sh
mise run glaze:mac       # natively on this Mac: ~5 s, no VM
mise run glaze:windows   # in the VM: the real gate
```

Both run `irgo-winvm glaze-check` (`-windows` for the VM): build the suite with
`go test -c`, run the binary with `-test.v=test2json`, and read the results
through `go tool test2json` — no output is grepped. Each ends with one line:

| verdict | means | exit |
|---|---|---|
| `YES` | everything passed or skipped by design | 0 |
| `KNOWN BUGS ONLY` | the only failures are upstream bugs recorded in UPSTREAM.md (`glazecheck.KnownUpstream`), reported or not | 0 |
| `NO` | a test failed that is not a known upstream bug | non-zero |
| `UNEXPECTED PASS` | a known upstream failure passes: update the list and [UPSTREAM.md](UPSTREAM.md) | non-zero |
| `CANNOT TELL` | the suite never ran (the guest agent went away), which says nothing about glaze | non-zero |

Every run records every test in [GLAZE-STATUS.md](GLAZE-STATUS.md): commit,
glaze and native versions (or the linked clone's branch and commit), and each
test's result with its first message. The full output and the test2json events
go to the log directory, and both paths are printed. Commit that file with the
change it describes.

To read the last answer without running anything: `irgo-winvm glaze-status`. It
also says whether the answer still matches the tree. Agents get the same through
the `glaze-status` and `glaze-check` MCP tools.

The suite is plain `go test`, so it also runs by hand:
`go -C examples test ./conformance` (opens windows), `-short` for the headless
tests only, `-run TestAppScheme` for one.

CI runs it too: the `conformance` workflow runs `glaze-check` on GitHub's
`macos-latest` and `windows-11-arm`, shows the table in the job summary, and
uploads the record, the log and the events as an artifact.

- Run `glaze:mac` on every edit.
- Run `glaze:windows` before you commit.

### Test your own changes to glaze or native

Point everything at local clones first. Both commands above then test your
edits:

```sh
mise run upstream:clone && mise run upstream:link
mise run glaze:mac && mise run glaze:windows
mise run upstream:verify         # their own tests, then this repo's, against the clones
mise run upstream:lint && mise run upstream:test:windows
mise run upstream:unlink         # back to the released versions
```

### Run one test on the VM by hand

Build the suite for Windows, then hand the binary to `app-create` with the
test flags you want:

```sh
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go -C examples test -c -o $PWD/.bin/win/conformance.test.exe ./conformance
irgo-winvm app-create -gui .bin/win/conformance.test.exe -test.v -test.run TestAppScheme
```

Inside the repo,
`irgo-winvm` is on your PATH: mise puts `.bin/` there, and any task (or
`mise run go:tool`) rebuilds it.

- `mise run glaze:hands` leaves glaze-all's window open on the guest's desktop,
  so you can drive it by hand.
- If a `-gui` run fails because there is no desktop session, run
  `irgo-winvm vm-repair -reboot`.
- If something looks stuck, run `irgo-winvm vm-screen` to photograph the guest.
  From the host, a stuck boot and a working one look identical.

## The docs site

<https://joeblew999.github.io/irgo-windows-vm/> is generated from the markdown
in this repository and published by `pages.yml` on every push to `main`. There
is no separate copy to edit: if a page is wrong, fix the markdown. The one
exception is the [command reference](#the-command-reference), which has no
source file.

```sh
mise run site:serve    # build and serve at http://localhost:8127
mise run site:build    # build only, into site/dist (gitignored)
```

`site:serve` stops whatever already holds the port. Otherwise a leftover server
keeps answering and you review the *old* build without knowing.

### What CI checks

None of these failures is visible on a page that renders, and each has happened.
CI fails on:

- a local link to a file the site doesn't publish;
- an absolute URL that has been rewritten as a repository path;
- a fragment link to a heading that doesn't exist;
- a page that appears in the HTML site but not the corpus, or the reverse (see
  below). A corpus missing a page still looks complete;
- a screenshot no page mentions (see [Screenshots](#screenshots)).

### Copies for machines

Beside each HTML page, the site publishes the same page as plain markdown, with
the extension swapped (`results.html` has `results.md`). It also publishes
**`llms.txt`**, an index, and **`llms-full.txt`**, all the documentation in one
file (~67 KB) for readers that prefer one request to six.

Point machines at these rather than at the repository's `.md` files, which lack
the command reference.

They are not a second copy. Each corpus entry is written in the same loop that
renders the HTML page, from the same markdown, in one pass over the page list
in `site/main.go`.

### Add a page

`README.md`, `RESULTS.md`, `UPSTREAM.md`, `DEVELOPMENT.md` and this file each
become a page. To add one, add a line to `pages` in `site/main.go`. Nothing is
discovered by scanning a directory, so nothing is published by accident.

### The command reference

The reference has no source file. The site build compiles the CLI and captures
`irgo-winvm help` and `-h` for every command, so no flag, default or usage
string is ever transcribed. `iso-create -fetch` computes its usage text from a
constant, so only a captured copy is correct.

Two commands exist for tooling rather than for people:

- **`irgo-winvm commands`** prints one command name per line. The reference
  generator and the documentation test both read it, so neither scrapes the
  usage text or drifts from what the binary accepts.
- **`irgo-winvm version`** prints the version stamped in at build time, or
  `dev` when built by hand. `doctor` shows the same value in its first row.

### Screenshots

Published screenshots live in `docs/screens/vm/`, and the tool puts them there:
`mise run vm:shots` copies the newest shot of each stage under the stage's name.
Don't copy them in by hand.

**CI fails on a screenshot no page mentions.** A slow boot produces
`booting-3`, `booting-4` and so on, and `vm:shots` publishes whatever a run
produced. Give each new shot a caption that names its file, or delete it. A
picture nobody explains is not evidence.

## Fix glaze and native bugs upstream

**This is non-negotiable, and it is why the project exists.** A failing probe
means a patch to [crgimenes/glaze](https://github.com/crgimenes/glaze) or
[crgimenes/native](https://github.com/crgimenes/native), never a workaround
here. A bug worked around in an example still ships to everyone using those
libraries, and the workaround hides it.

Record what you found, and where it was fixed, in [UPSTREAM.md](UPSTREAM.md).

## Commits

- **One concern per commit**, each verified on its own. A refactor landed as one
  commit can't be reviewed or bisected.
- **Say what changed and why the old code was wrong.** The commit log is the only
  record of things that cost hours and don't show in the diff, such as `utmctl`
  exiting 0 on failure, or `del` exiting 1 when a glob matches nothing.
- **Include measurements.** If you measured something to be sure, put the
  numbers in the message.
- **Correct a measurement everywhere it appears**, including
  [RESULTS.md](RESULTS.md), which is dated on purpose.

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
| `brew install --cask joeblew999/tap/irgo-winvm` | the cask GoReleaser writes and pushes to [joeblew999/homebrew-tap](https://github.com/joeblew999/homebrew-tap) |
| `go install …/cmd/irgo-winvm@vX.Y.Z` | the module proxy; the binary reports the tag from its build info |
| the raw binary | the release's assets |

**The tap** needs, once: the public repository `joeblew999/homebrew-tap` (empty
is fine; GoReleaser writes `Casks/irgo-winvm.rb`), and a repository secret
`HOMEBREW_TAP_GITHUB_TOKEN` here holding a fine-grained token with Contents
read and write on that repository. Without the secret the cask is not uploaded
and the release notes leave the brew line out; everything else publishes. The
cask clears the quarantine flag after install, because the binary is not
signed with an Apple Developer ID and Homebrew quarantines what a cask
downloads.

**Gatekeeper.** The binaries are ad-hoc signed by the Go linker (arm64 requires
a signature to run at all) and not notarized. A file with no quarantine flag
runs; one a browser downloaded is refused until `xattr -d
com.apple.quarantine` clears it. `curl`, `go install` and the cask's hook leave
no flag. Signing and notarizing need an Apple Developer account, which this
project does not have.

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
release is in [RESULTS.md](RESULTS.md).

## Licence

MIT; see [LICENSE](../LICENSE). Contributions are offered under it.

The licence is a claim about the whole published binary, not just this
repository's code, so every dependency linked into it was checked. All are
permissive; nothing in the module graph is copyleft.

| licence | modules |
|---|---|
| MIT | `anchore/go-lzo`, `diskfs/go-diskfs`, `djherbis/times`, `sirupsen/logrus`, `google/jsonschema-go`, `modelcontextprotocol/go-sdk`, `segmentio/asm`, `segmentio/encoding` |
| BSD | `elliotwutingfeng/asciiset`, `google/uuid`, `pierrec/lz4`, `pkg/xattr`, `ulikunitz/xz`, `yosida95/uritemplate`, `golang.org/x/sys`, `golang.org/x/oauth2`, `golang.org/x/sync`, `golang.org/x/time` |
| Apache-2.0 | `klauspost/compress` |

- **Nineteen modules**, up from eleven before the MCP server. The eight it added
  are `go-sdk` and what it pulls in: `jsonschema-go`, `segmentio/asm`,
  `segmentio/encoding`, `uritemplate`, `x/oauth2`, `x/sync` and `x/time`.
  `x/oauth2` arrives even if the auth package is unused; importing `mcp` is
  enough.
- **`go-sdk` is mid-relicence**: Apache-2.0 for new contributions, MIT for older
  un-relicensed ones. Both are permissive and neither adds a condition beyond
  attribution. None of the eight ships a `NOTICE` file.
- **`klauspost/compress` (Apache-2.0)** is the only licence with a condition
  beyond attribution, and it ships no `NOTICE` file, so there is nothing to
  carry.

**If you add a dependency, re-check this table.** `go list -deps ./cmd/irgo-winvm`
lists what actually reaches a user.
