# Contributing

This file is the mechanics: how to get set up, what to run, how to land a
change.

It deliberately says nothing about how the code is written. That is
[AGENTS.md](AGENTS.md), and it is not optional reading — most of the duplication
this project has had to remove was written by someone who did not check what
already existed. Read it before writing code, not after.

## Setup

[mise](https://mise.jdx.dev) pins the toolchain, so there is one step:

```sh
mise install       # Go and golangci-lint, at the versions CI uses
mise run go:check  # proves it works
```

Nothing else is required to build or test the Go code. The VM work needs macOS
on Apple Silicon and UTM, which `vm-create` installs itself.

`mise tasks` lists everything. The names are the commands they run.

## Before you push

Two tasks, the same two CI runs:

```sh
mise run go:check   # build, vet and test every module, cross-compile every target
mise run go:lint    # unused, ineffassign, staticcheck, errcheck
```

`go:check` covers four separate Go modules and cross-compiles for Linux and
Windows as well as macOS. That is not ceremony: deleting a function from
`sysfile_other.go` once passed every check being run, because they were all
darwin.

## The three cycle tests

These are not run by CI and cannot be — they need UTM, an Apple Silicon host and
real media. Run the one you touched.

| task | what it does | cost |
|---|---|---|
| `mise run iso:test` | delete the ISO and rebuild it from the `.esd` | ~50s, and 4.9 GB of disk once (see the note in the task) |
| `mise run vm:test` | create and delete a VM, against a disposable name | minutes; refuses to run if a VM is up, since it restarts UTM |
| `mise run app:test` | push a binary to the VM, run it, remove it | ~20s; needs a VM with Windows on it |

Use a disposable VM for anything destructive. `vm:test` already does — it builds
`irgo-test-cycle`, never your real one. Losing a 45-minute install to a test is
not a trade worth making.

## Running the probes

The four guest programs are what this repository exists to run. Each has a task:

```sh
mise run app:create:probe           # headless: clipboard, power, single-instance, mmap
mise run app:create:glaze-all       # windowed: tray, menus, file dialogs, app icon
mise run app:create:verify          # glaze's portless app:// path
mise run app:create:verify-events   # glaze's Events bridge
```

Each has a matching `app:delete:*`. `app:create:glaze-all:hands` leaves the
window up on the guest's desktop to drive by hand instead of reporting.

When something is stuck, `mise run vm:screen` photographs the guest — from the
host a stuck boot and a working one look identical.

## A glaze or native bug is fixed at crgimenes, not here

**Non-negotiable, and the reason this project exists.** A failing probe means a
patch to [crgimenes/glaze](https://github.com/crgimenes/glaze) or
[crgimenes/native](https://github.com/crgimenes/native), never a workaround in
this repository — a bug worked around in an example still ships to everyone
using those libraries, and the workaround hides it.

Record what you found and where it was fixed in [UPSTREAM.md](UPSTREAM.md).

## Commits

One concern per commit, each verified on its own. A refactor landed as a single
commit cannot be reviewed and cannot be bisected.

Say what changed and *why it was wrong before*. The commit log here is the only
record of things that cost hours and are invisible in the diff — that `utmctl`
exits 0 on failure, that `del` exits 1 when a glob matches nothing. If you had
to measure something to be sure, put the measurement in the message.

Correct a measurement wherever it appears, including [RESULTS.md](RESULTS.md),
which is dated on purpose.

## Releases

CI publishes them; you tag them.

```sh
git tag -a v0.1.2 -m "..." && git push origin v0.1.2
```

`release.yml` runs the same gate as `check.yml`, then `go:build`, then publishes
the binaries and `SHA256SUMS`. The version comes from the tag and from nowhere
else, so nothing in the tree needs editing first.

The checksums are reproducible: `mise run go:build` on the same tag produces
byte-identical binaries, which is why `-buildvcs=false` is there. If you change
the build, check that is still true rather than assuming it.

## Not settled yet

- There is **no LICENSE file**. Until there is, nobody has been told what they
  may do with this, so add one before the repository goes public.
