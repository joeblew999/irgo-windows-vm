# Working on this repository

Read this before writing code, not after. It is approach, not an index — any
list of where things live goes stale the day someone moves them.

Most of the mess here was written by an AI that did not check what already
existed, then described what it had done rather than what was true. The second
half is worse than the first.

## Check before you claim

Every one of these was asserted wrongly first, then measured:

- `ln` to an immutable file is `EPERM`, so protecting the ISO silently turned a
  hardlink into a 5 GB copy.
- `rm -rf` on a bundle holding that hardlink fails, so every VM built from a
  protected ISO was undeletable.
- `utmctl delete` prints its failure and **exits 0**.
- `utmctl` will not drop a registry entry whose bundle is gone, so removing a
  bundle behind its back makes a phantom it cannot then recover from.
- `net/http` already rejects a short body, so a length check added to the
  downloader was unreachable — proven by disabling it and watching the test
  still pass.

Grep counts comment mentions as call sites; it gave two wrong answers in one
afternoon. **Use the compiler**: delete the symbol, rebuild, run the tests, put
it back if either fails.

## Nothing returns success without checking it did the thing

Nearly every bug here is one operation: renaming a download without verifying
it, reporting exit code 0 when the output could not be fetched, returning nil
after five failed boots, an error value that can never be non-nil, building an
ISO and checking nothing about it.

An operation that cannot report failure also cannot be undone, because it does
not know what it did.

## Every action owes an undo

The commands come in pairs — make a VM and delete it, put a binary on the guest
and remove it. The undo is not bookkeeping: it is what lets a failed step be
fixed and re-run. Its absence is why guest litter accumulated one file per
invocation, forever.

## "Cannot tell" is not "safe"

Guards are written `if ok && bad { refuse }`, so a question that cannot be
answered does not refuse — it allows. Every guard here stands in front of
something destructive, so that is backwards.

Answer three ways: yes, no, and *could not determine*. The caller handles the
third explicitly, refusing by default.

## One way to do each thing

There were four ways to run a binary in the guest, differing only in where it
landed and which session ran it, and every caller had to know which to pick.

If you are about to add a second route to something that already has one, stop.

## The comments are findings

Long comments record what cost hours and is not recoverable from the code: why
the display is `virtio-ramfb-gl`, why ESD image 3 needs `--boot`, why
`utmctl suspend --save-state` must never be called, why `%q` must not be
re-escaped for AppleScript.

Move them with their code. Do not compress or tidy them. If one is wrong, fix
the fact — do not delete the explanation. When you correct a measurement, look
for the other copy: three pairs of comments here assert opposite facts about the
same experiment, because each was fixed in one place only.

## Verify against the VM, not the compiler

Unit tests cover config generation, ISO inspection, the catalog, prune and
delete. Everything else is only proven by running a real VM, and those paths
fail silently — a wrong boot command produces a prompt nobody sees.

Use a disposable VM. Running a binary pushes it into the guest and executes it,
which is a mutation; losing a 45-minute install to a test is not a trade worth
making.

`RESULTS.md` is the contract. If a number there changes, either you broke
something or you have a new measurement — decide which before committing.

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
- Land a refactor in one commit. One concern per commit, each verified.
