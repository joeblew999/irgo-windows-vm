# Working on this repository

Read this before writing code, not after. It is the approach, not an index —
any list of where things live goes stale the day someone moves them.

## Three stages, isolated

`iso` gets the Windows media. `vm` makes a VM. `run` puts a binary on it. Each
has an undo, and each owns its own paths and constants in its own files:

- **iso** knows nothing about UTM. It is a download from Microsoft and an ISO
  built with `xorriso`, and it works on a machine that has never had a
  hypervisor. It once called into UTM's bundle directory to decorate an error
  message, which meant fetching media required UTM to be installed.
- **vm** owns UTM entirely: finding it, installing it, `utmctl`, the bundle
  layout, the guest tools.
- **run** drives the guest through `vm`'s `utmctl`, and owns the guest-side
  paths.

Dependencies run one way: `vm` and `run` use the ISO API, nothing comes back.
`doctor` reports on all three and calls into them rather than holding its own
copy of where anything is.

If you are about to make one stage reach into another's paths, stop.

## One way to do each thing

There were four ways to run a binary in the guest, differing only in where it
landed and which session ran it, and every caller had to pick. There were three
answers to where media lives, all live at once. A second route is a second
answer.

## Nothing returns success without checking it did the thing

Nearly every bug here was this: a download renamed without verifying its length,
`ExitCode: 0` when the output could not be fetched, `nil` after five failed
boots, an error value that could never be non-nil, an ISO built and never
checked.

Closing a file you *wrote* can fail, and that is where a full disk shows up.
Closing one you read cannot.

An operation that cannot report failure also cannot be undone, because it does
not know what it did.

## Every action owes an undo

The commands come in pairs. The undo is what lets a failed step be fixed and
re-run, and it must work from any starting point — stopping early because half
the work was already gone leaves the other half behind for good.

Deleting nothing is success, not an error, or the undo cannot be run twice.

## "Cannot tell" is not "safe"

Guards are written `if ok && bad { refuse }`, so a question that cannot be
answered does not refuse — it allows. Every guard here stands in front of
something destructive, so that is backwards.

Answer three ways: yes, no, and *could not determine*. The caller handles the
third explicitly, refusing by default.

## Check before you claim

Everything below was asserted wrongly first, then measured:

- `ln` to an immutable file is `EPERM`, so protecting the ISO silently turned a
  hardlink into a 5 GB copy.
- `rm` on a bundle holding that hardlink fails, so every VM built from a
  protected ISO was undeletable.
- `utmctl delete` prints its failure and **exits 0**.
- `utmctl` will not drop a registry entry whose bundle is gone, so removing a
  bundle behind its back makes a phantom it cannot then recover from.
- `net/http` already rejects a short body, so a length check added to the
  downloader was unreachable — proven by disabling it and watching the test
  still pass.

Grep counts comment mentions as call sites; it gave three wrong answers in one
afternoon. **Use the compiler and the analysers**: delete the symbol, rebuild,
run the tests, put it back if either fails. `mise run go:lint` finds what grep
does not.

And check on every platform. Deleting a function from `sysfile_other.go` passed
every check that was running, because they were all darwin. `mise run go:check`
cross-compiles.

## A test that cannot fail is not a test

Every assertion needs a negative control: **break the thing, watch the test go
red, put it back.** Ten seconds, by hand, when you write the test. There was a
mise task automating this across eight mutations; it was fifty lines of shell
matching exact source text, so it broke on the first rename and once left a
mutated file in a commit. The habit catches what matters — a test born vacuous —
and the machinery only caught tests weakened later, which did not happen.

This is not theory. A test for the scan cache passed against a mutation that
disabled the check it was testing, because the case it built also changed the
other field. And a test for "the build records its verdict" is marked as not
proving that, because deleting the build's call leaves it green.

If a property can only be verified by measurement, record the measurement in
`RESULTS.md` with a date rather than writing a test that looks like coverage.

## Say what is happening, and where

A command that prints nothing for fifty seconds is indistinguishable from one
that has hung. Announce each step *before* doing it, name every path, and print
elapsed time. "Not found" without a location cannot be checked.

That is how the 77-second ARM64 scan was found: it was always there, and nothing
said so.

## The comments are findings

Long comments record what cost hours and is not recoverable from the code: why
the display is `virtio-ramfb-gl`, why ESD image 3 needs `--boot`, why
`utmctl suspend --save-state` must never be called, why `%q` must not be
re-escaped for AppleScript.

Move them with their code. Do not compress or tidy them. If one is wrong, fix
the fact — do not delete the explanation. When you correct a measurement, look
for the other copy.

## Verify against the VM

Unit tests cover the ISO stage well, the VM and run stages only at the edges.
Anything touching a real guest is proven by running it, and those paths fail
silently.

Use a disposable VM. Running a binary pushes it into the guest and executes it,
which is a mutation; losing a 45-minute install to a test is not a trade worth
making.

## A glaze or native bug is fixed at crgimenes

Non-negotiable, and the reason this project exists. A bug worked around in an
example still ships to everyone using those libraries, and the workaround hides
it. `UPSTREAM.md` is the ledger.

## Do not

- Split the package up. Its parts are coupled.
- Touch the assets, the answer file or the plist template without running a real
  install. UTM rejects a bad config with one generic *"cannot import this VM"*
  that names no field.
- Export anything nothing uses.
- Put logic in `mise.toml`. Tasks call the binary; if a task needs to do more
  than that, it belongs in the binary.
- Land a refactor in one commit. One concern per commit, each verified.
