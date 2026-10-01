# Conventions

How code in this repository is written. Each rule exists because its absence
caused a real defect here, and each names that defect. Read them, with
[Architecture](ARCHITECTURE.md) and [Known traps](TRAPS.md), before writing
code: most of the duplication this project has had to remove was written by
someone who did not check what already existed.

## One way to do each thing

Every operation has exactly one implementation, and every question (where media
lives, how a binary runs in the guest) has one answer. A second route is a
second answer that drifts. Example: there were once four ways to run a binary in
the guest, differing only in where it landed and which session ran it, and
three answers to where media lives, all in use at once.

## Nothing reports success it did not verify

A function returns success only after checking that the effect happened. An
operation that cannot report failure also cannot be undone, because it does not
know what it did. Example: a download was renamed into place without checking
its length. The same defect appeared as `ExitCode: 0` when the output could not
be fetched, `nil` after five failed boots, an error value that could never be
non-nil, and an ISO built and never checked. Closing a file you *wrote* can fail
— that is where a full disk shows up — so that error is checked; closing a file
you read cannot.

## Commands come in do/undo pairs

Every command that changes state has an undo, so a failed step can be cleaned up
and re-run. The undo must work from any starting point, and deleting nothing is
success, so it can run twice. Example: `cmd`'s `del` exits 1 on a glob that
matches nothing, so an undo built on it succeeded while there was something to
remove and failed as soon as there was not.

## Guards answer yes, no, or cannot tell

A guard written `if ok && bad { refuse }` allows the action when the question
cannot be answered. Every guard here protects something destructive, so that is
backwards. Guards return three answers — yes, no, *could not determine* — and
the caller handles the third explicitly, refusing by default. Example:
`glaze-status` reports each recorded verdict as current, stale or cannot tell,
and `glaze-check` ends with `YES`, `NO` or `CANNOT TELL` when the guest agent
went away.

## Every check has a negative control

A test that cannot fail is not a test. When writing one, break the code it
covers, watch the test fail, and restore it; record the control in the test's
comment. Example: a test for the scan cache passed against a mutation that
disabled the check it covered, because its test case also changed the other
field.

If a property can only be verified by measurement, record the measurement with
a date in [RESULTS.md](RESULTS.md) rather than writing a test that looks like
coverage; a test for "the build records its verdict" is marked as not proving
that, because deleting the build's call leaves it green. Controls are run by
hand, not automated: a mise task that applied eight mutations matched exact
source text, broke on the first rename, once left a mutated file in a commit,
and was removed.

## Measure, do not assert

Behaviour of UTM, Windows and the filesystem is established by running it, not
by reasoning. Example: a length check added to the downloader was unreachable,
because `net/http` already rejects a short body — proven by disabling the check
and watching the test still pass. The other measured surprises are in
[Known traps](TRAPS.md). For code, use the compiler and analysers rather
than grep, which counts comment mentions as call sites and gave three wrong
answers in one afternoon: delete the symbol, rebuild, run the tests, and put it
back if either fails. `mise run go:lint` finds what grep does not.

## Say what is happening, and where

A command that prints nothing for fifty seconds cannot be told apart from one
that has hung. Announce each step before doing it, name every path, and print
elapsed time; "not found" without a location cannot be checked. Example: the
77-second ARM64 scan was always there and was found only once the tool said so.

## Messages link the site, not the source

Nothing a release user runs assumes a checkout: messages link
[the site](https://joeblew999.github.io/irgo-windows-vm/) (`utmvm.SiteURL`),
never a `docs/` path, and the two commands that need the source
(`glaze-check`, `glaze-status`) say so. Comments may cite `docs/` paths; they
are read in a checkout.

## Comments follow Go norms

A doc comment says what the thing does and, in a sentence or two, the
non-obvious why. A measured trap or a warning stays in the code, tightly worded:
why the display is `virtio-ramfb-gl`, why ESD image 3 needs `--boot`, why
`utmctl suspend --save-state` must never be called. The story of how it was
found belongs in [RESULTS.md](RESULTS.md) or [Known traps](TRAPS.md), not in the
code. If a comment is wrong, fix the fact, and look for any other copy of a
measurement you correct.

## Upstream bugs are fixed upstream

A bug in glaze or native is fixed in [crgimenes](https://github.com/crgimenes),
not worked around here. A workaround in an example still ships the bug to every
user of those libraries and hides it. Example: `examples/conformance` (and
`glaze-all`) carry one marked stand-in for the `ErrUnsupported` fix, to be
deleted when a release contains it. A test for an upstream bug is never
skipped to make a run green: it fails, and `glazecheck.KnownUpstream` names it
as a known upstream bug (see [the conformance suite](TESTING.md#the-conformance-suite)). [UPSTREAM.md](UPSTREAM.md) is the ledger.

## Do not

- Split `internal/utmvm`. Its parts are coupled.
- Touch the assets, the answer file or the plist template without running a real
  install. UTM rejects a bad config with one generic *"cannot import this VM"*
  that names no field.
- Export anything nothing uses.
- Put logic in a task, whether in `mise.toml` or `mise-tasks/`. Tasks call the
  binary; anything more belongs in the binary.
- Land a refactor in one commit. One concern per commit, each verified.
