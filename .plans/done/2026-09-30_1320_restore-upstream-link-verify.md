# Restore the upstream workflow: `upstream:clone` / `link` / `verify` / `unlink`

Status: DONE · 2026-09-30 · prerequisite for `2026-09-30_1250_glaze-webview2-stale-registration.md`

## Why

This repo's rule is that a glaze or native bug is fixed upstream, in local clones, and proven here.
The tasks that did that were deleted with the old `mise.toml` in `e533764` (2026-08-13, "Delete
mise.toml and REFACTOR.md"). When `mise.toml` came back, they did not. Traces remain:
`.gitignore` still says *"`mise run upstream:verify` writes one [go.work] to test against local
glaze/native clones"*, and `UPSTREAM.md` still lists findings as `PATCHED LOCALLY` with no task
that can reproduce the patch. Without them there is no repeatable way to test an upstream edit
against our probes.

## Facts (checked 2026-09-30)

- Local clones exist: `~/workspace/go/src/github.com/crgimenes/glaze` and `…/native`.
- Both upstreams' default branch is now **`trunk`**. The glaze clone was left on the stale
  `master` (last commit 2026-08-03); `origin/HEAD` now points at `trunk`.
- glaze `trunk` requires **go 1.27.1**; this repo is on toolchain go1.27.1 (bumped in `5a73e31`).
- The old tasks wrote `go 1.26.5` in go.work and called `vm:run`, which no longer exists; today's
  runners are `app:create:probe`, `app:create:verify`, `app:create:verify-events`,
  `app:create:glaze-all`.
- `go.work` / `go.work.sum` are already gitignored.

## Change — `mise.toml`

### `[env]` (new section; one answer for tasks and the Go code)

```toml
[env]
# Where local clones of glaze and native live. A bug in either is fixed THERE,
# not worked around here — UPSTREAM.md tracks each one.
UPSTREAM_DIR = "{{env.HOME}}/workspace/go/src/github.com/crgimenes"
```

### `upstream:clone` — idempotent

```toml
[tasks."upstream:clone"]
description = "Clone glaze and native into $UPSTREAM_DIR, or bring existing clones to trunk"
run = '''
set -e
mkdir -p "$UPSTREAM_DIR"
for repo in glaze native; do
  d="$UPSTREAM_DIR/$repo"
  [ -d "$d/.git" ] || git clone "https://github.com/crgimenes/$repo.git" "$d"
  git -C "$d" fetch --quiet origin
  git -C "$d" remote set-head origin -a >/dev/null
  def=$(git -C "$d" symbolic-ref --short refs/remotes/origin/HEAD | sed 's|origin/||')
  cur=$(git -C "$d" branch --show-current)
  # never move a fix branch: only a clone sitting on a stale default gets switched
  if [ "$cur" = "master" ] || [ "$cur" = "$def" ]; then
    git -C "$d" switch --quiet "$def" && git -C "$d" pull --quiet --ff-only
  fi
  echo "$repo: on $(git -C "$d" branch --show-current) (upstream default: $def)"
done
'''
```

### `upstream:link` / `upstream:unlink` — as before, with today's Go

Restore verbatim from `git show e533764^:mise.toml`, with one change: `go 1.26.5` → `go 1.27.1`
in the generated go.work. Keep the comment explaining why the `replace` lines are required
(`use` alone still resolves glaze from the module cache, so the edit under test would be silently
ignored).

### `upstream:verify` — their tests, ours, and today's Windows binaries

```toml
[tasks."upstream:verify"]
description = "Run glaze's and native's own tests, then build and test ours against them"
depends = ["upstream:link"]
run = '''
set -e
echo "--- glaze's own tests ---";  go -C "$UPSTREAM_DIR/glaze"  test ./...
echo "--- native's own tests ---"; go -C "$UPSTREAM_DIR/native" test ./...
echo "--- this repo against them ---"
mise run go:check
echo
echo "prove it on Windows (still LINKED):"
echo "  mise run app:create:probe | app:create:verify | app:create:verify-events | app:create:glaze-all"
echo "then: mise run upstream:unlink"
'''
```

`go:check` already builds, vets and tests every module and cross-compiles every target with
`CGO_ENABLED=0`, so it replaces the old hand-rolled build loop. The `app:create:*` tasks already
build the Windows binaries they push, so while linked they carry the local edit.

## Docs

- `README.md` / `AGENTS.md`: one short section "Working on glaze or native" —
  `upstream:clone` → edit in `$UPSTREAM_DIR/<repo>` on a branch → `upstream:verify` →
  `app:create:*` on the VM → `upstream:unlink` → PR from the fork remote.
- `UPSTREAM.md` status legend: `PATCHED LOCALLY` means "reproducible with `upstream:verify`".

## Verification

1. `mise run upstream:clone` → both report `on trunk`. Run it twice: the second run changes nothing.
2. `mise run upstream:link` → `go.work` exists with `go 1.27.1` and both `replace` lines;
   `go list -m github.com/crgimenes/glaze` from `glaze-probes/` prints the local path.
3. `mise run upstream:verify` → glaze tests, native tests, `go:check` all green on unmodified clones.
4. Sanity that the link is real: add `panic("linked")` to glaze's `New` in the clone →
   `mise run app:create:verify` fails with that panic; remove it.
5. `mise run upstream:unlink` → `go.work` gone; `go list -m` shows the released version again.

## Out of scope

The WebView2 fix itself (its own plan), and any change inside glaze or native.

## Outcome (2026-09-30)

Done as planned, with three differences found while doing it:
- **Link lists every module** via `go work init && go work use -r .` instead of the old hand list:
  `site/` (added after the tasks were first written) was missing, and `go:check` failed with
  "directory prefix . does not contain modules listed in go.work".
- **TOML literal strings (`'''`)** for the scripts, like the rest of the file: a basic string
  rejects the backslash-backtick escapes in the go.work comment.
- **Dirty-clone guard** in `upstream:clone`: both clones held uncommitted work, the
  `PATCHED LOCALLY` fixes from UPSTREAM.md. They were saved first, locally, on branches:
  glaze `patch/nsapp-new-blocks` and `patch/errunsupported-wraps-std`, native
  `patch/errunsupported-wraps-std`. The clones are now on `trunk`.

Verified: clone twice, idempotent (both on trunk); link makes `go list -m` resolve glaze to the
local path; `upstream:verify` green (glaze tests, native tests, go:check across 5 modules); a panic
added to the local glaze reached our build; unlink returns to glaze v0.0.61.
