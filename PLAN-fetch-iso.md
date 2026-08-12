# Replacing CrystalFetch — plan

Getting a bootable Windows 11 ARM64 ISO with one command, so a developer can
clone this repository and run it without a GUI app, a Microsoft account, or
knowing which of six download buttons is the right one.

This is scoped to **exactly what this project needs** and nothing else.
CrystalFetch is a general tool with a UI; we need one image, in one shape, that
boots one hypervisor.

Status: **the download half is built and tested. The conversion half is not.**
`PLAN.md` item 6 is the parent; this file is the detail.

**No showstoppers, and the hard part is done.** A self-built ISO booted UTM
straight into Windows Setup and installed unattended on 12 Aug 2026 — the risk
this plan was most worried about is retired, with a picture in
`docs/screens/`.

The whole replacement needs **two** external tools, both Homebrew formulae, and
both are what CrystalFetch itself bundles:

| tool | for | why nothing else will do |
|---|---|---|
| `wimlib` | reading `.esd` | LZMS compression; no Go implementation exists worth trusting |
| `xorriso` | writing the ISO | `hdiutil` produces media UTM will not boot — measured twice |

What remains is **the catalog CAB's LZX compression** (stage 1), which is the
last thing standing between a clean clone and one command. It is self-contained
and needs no dependency: Microsoft ships a Go LZX decoder in
`go-winio/wim/lzx`.

---

## What we need

| | |
|---|---|
| architecture | **ARM64 only.** x64 is what glaze's CI already covers; the whole point of this project is the arch nobody tests |
| edition | **CLIENTCONSUMER.** `autounattend.xml` selects Pro from it |
| language | **en-us.** One. Others are a flag, not a feature |
| builds | **the current one.** Not a version picker, not a build archive |
| output | a **bootable** ISO that UTM's aarch64 firmware can start, and that Windows Setup will read our answer-file CD alongside |

## What we do NOT need

Each of these is real work that CrystalFetch does and we should not:

- **UUP dump / Insider builds.** A whole second download pipeline for builds we
  have no reason to test.
- **Every language and edition.** 153 images in the catalog; we want one.
- **A UI, progress windows, a download queue.** One command, one progress line.
- **Password removal (`chntpw`), driver injection, unattend editing.** This repo
  already generates its own answer-file CD, which is a better mechanism: it
  leaves Microsoft's media untouched and byte-identical.
- **Building on x64 hosts.** VM creation already refuses anywhere but Apple
  Silicon.

Cutting these is most of why this is tractable at all.

---

## Where the bytes come from — settled

Two routes exist and only one works.

**The software-download API is a dead end.** It is what `quickget` automates.
Its first three steps still answer — product edition `3324`, session permit, SKU
list (`20086` for English US) — and the fourth, the one that returns a link,
refuses:

```
{"Errors":[{"Key":"ErrorSettings.SentinelReject",
            "Value":"Sentinel marked this request as rejected."}]}
```

Tried with a browser user-agent, a cookie jar, a referer, both `/tags` and
`/fp/tags` fingerprint endpoints, and delays between steps. This is deliberate
anti-automation. Do not build on it: it is maintained against us.

**The Media Creation Tool catalog works, and is better.** A CAB at
`https://go.microsoft.com/fwlink/?LinkId=841361` listing every image with a
direct `dl.delivery.mp.microsoft.com` URL, a size, and **a SHA-1**. No session,
no cookies, no rejection. Resumable (`Accept-Ranges: bytes`).

It is provably the source of the ISO already in `.cache`: filtered to
ARM64/en-us/consumer it returns **exactly one** entry, build
`26100.4349.250607-1500`, which is the build of the working ISO.

The SHA-1 is the part no GUI gives you. A download you cannot verify is a
download you have to trust.

---

## The three stages

### 1. Fetch — **done**

`irgo-winvm fetch-iso` parses the catalog, filters to one image, and downloads
it with resume and SHA-1 verification. Pure Go, no dependencies.

Tested: staging file, cleanup, resume, refusing to overwrite an existing file,
rejecting a bad hash and not installing it.

It also **refuses to write anywhere near the working ISO**, naming the other
hardlinks, because `.cache/win11-arm64.iso` is the same inode as the media the
VM boots from.

**One gap:** the catalog CAB is **LZX**-compressed (`0x1503`), not MSZIP, so the
standard library cannot read it, and `fetch-iso` currently falls back to a
`products.xml` CrystalFetch already extracted. That works here and helps nobody
on a clean machine.

*Fix:* `github.com/Microsoft/go-winio/wim/lzx` is a pure-Go LZX decompressor
from Microsoft. CAB-LZX and WIM-LZX differ in framing but not in the algorithm.
Small, self-contained, no new runtime dependency. **Do this first — it is the
cheapest thing on the list and it removes the embarrassing fallback.**

### 2. Convert ESD → ISO tree — **needs a decision**

The catalog serves `.esd`: a WIM archive using **LZMS** compression. All 1978
entries are ESD; Microsoft publishes no ISO here.

Nothing in Go reads LZMS. `go-winio/wim` handles LZX only
(`supportedHdrFlags` excludes it), and its `decompress.go` is
`//go:build windows || linux` — it does not even compile on macOS.

**CrystalFetch's answer, which is worth copying because it is proven:** it
bundles `wimlib` as a git submodule and drives it from a bash script
(`Extras/esd2iso.sh`, a vendored copy of Paul Rockwell's `w11arm_esd2iso`). The
sequence, which is the whole conversion:

```
wimlib-imagex info   <esd>                        # image count
wimlib-imagex apply  <esd> 1 <dir>                # boot/media files
wimlib-imagex export <esd> 2 <dir>/sources/boot.wim --compress=LZX --chunk-size 32K
wimlib-imagex export <esd> 3 <dir>/sources/boot.wim --compress=LZX --chunk-size 32K --boot
for i in 4..N:
wimlib-imagex export <esd> $i <dir>/sources/install.wim --compress=LZMS --chunk-size 128K
```

`--boot` on image 3 is not optional; without it Setup fails. That is exactly the
class of thing this repository's "things that cost hours" table exists for, and
we get it for free by reading somebody else's scars.

`wimlib` is on Homebrew as `wimlib`, and **is already installed on this
machine** (1.14.5).

> **Decision needed — see "The dependency question" below.**

### 3. Master the bootable ISO — **harder than expected**

The measured facts, from the working ISO:

| file | size |
|---|---|
| `sources/install.wim` | **4,401,350,166 bytes (4.099 GiB)** |
| `sources/boot.wim` | 587,728,733 bytes |
| `efi/microsoft/boot/efisys.bin` | 1,720,320 bytes |
| `efi/microsoft/boot/efisys_noprompt.bin` | 1,720,320 bytes |
| `efi/boot/bootaa64.efi` | 2,647,480 bytes |

**`install.wim` is 101 MiB over ISO9660's 4 GiB file-size limit.** This is why
the README's trap table says Windows ISOs are UDF. It is not a stylistic choice
and it cannot be avoided by compressing harder — 4.099 GiB is what LZMS already
produced.

So the ISO must be **UDF**, or ISO9660 with multi-extent (level 3).

**`go-diskfs` cannot write UDF.** It has `ext4`, `fat12/16/32`, `iso9660` and
`squashfs` — there is no `udf` package. It *does* have El Torito
(`filesystem/iso9660/eltorito.go`), which is the other half we need, so it is
half a solution.

Options, none free:

- **`hdiutil makehybrid -iso -udf -eltorito-boot ...`** — built into macOS, no
  install. CrystalFetch has this in its script, commented out. Cheapest by far.
  Costs the README's "no `hdiutil`" claim, which was written about disk-image
  creation where Go could do the job — this is a case where it demonstrably
  cannot.
- **`mkisofs` / `xorriso`** — what CrystalFetch actually ships (a cdrtools
  fork). A second Homebrew dependency, on top of `wimlib`.
- **Teach `go-diskfs` multi-extent ISO9660 level 3** — keeps everything in Go
  and upstreams something real, but multi-extent is exactly the corner of
  ISO9660 that readers implement badly, and "Windows Setup did not read it" is a
  failure with no error message. High risk for the payoff.

**A find worth having:** `efisys_noprompt.bin` ships *inside* the media. Using
it as the El Torito boot image means the built ISO auto-boots with **no "Press
any key to boot from CD"** — which is currently worked around by typing
`\efi\boot\bootaa64.efi` at the UEFI shell and sending eight keypresses over six
seconds, a hack the README documents as costing hours and as being dangerous
(surplus presses reach Setup's UI and destroyed an install once).

**Building our own ISO could therefore delete one of this project's worst
hacks.** That is a better reason to do this than removing a GUI step.

---

## Can we just drive CrystalFetch, or borrow its tools? No — and that is useful

**It has no command-line mode.** `Contents/MacOS/CrystalFetch` is a SwiftUI app
and ignores arguments.

**Its bundled tools cannot be reused.** The bundle ships exactly the three
binaries this plan needs — `wimlib-imagex`, `mkisofs`, `cabextract`, next to
`aria2c`, `chntpw` and `esd2iso.sh`. Running any of them standalone dies
immediately with SIGTRAP (exit 133). They are signed
`llc.turing.CrystalFetch.<tool>` with `com.apple.security.inherit`: sandbox
helpers, permitted to run only as children of the sandboxed app. That is how
Apple requires helpers to ship, not something to work around.

What it *does* tell us is that the tool list here is right. Three gaps were
identified independently — LZX, LZMS, bootable ISO — and a shipped, working
implementation bundles one tool for each:

| gap | their tool | on this machine |
|---|---|---|
| LZX catalog CAB | `cabextract` | no — but `go-winio/wim/lzx` covers it in Go |
| ESD / LZMS | `wimlib-imagex` | **already installed** (Homebrew, 1.14.5) |
| bootable ISO | `mkisofs` | no — `brew install xorriso` or `cdrtools`, both bottled |

**So one tool is missing, not two,** and it is the replaceable one: `wimlib` is
the piece with no Go alternative and it is already here. Whether the ISO
masterer is needed at all is precisely what Step 0 settles.

## The dependency question

The README's headline is *"Pure Go. No `hdiutil`, no `plutil`, no shell
scripts — clone, build, run."* Stage 2 cannot honour that today, and stage 3
probably cannot either.

Three ways to hold it:

1. **Optional extra.** `fetch-iso` downloads and verifies with zero
   dependencies — true today. Conversion is a separate step that requires
   `wimlib` and says `brew install wimlib` when it is missing. Everything that
   works today keeps working with no tools, and the promise still covers all of
   it. *Recommended: it is honest, it is incremental, and the claim stays true
   for every command that currently exists.*
2. **Just require it.** One command end to end, simplest code and docs. A clean
   clone needs Homebrew before it can do anything, and the README claim has to
   be softened.
3. **Pure Go or nothing.** Wait, or write an LZMS decompressor. There is exactly
   one pure-Go attempt —
   `github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/wim/lzms`, MIT,
   published 2026-08-04, a clean-room reimplementation of wimlib's decoder that
   claims LZMS + WIM + ISO9660/El Torito + UDF and a one-call ESD→ISO builder.
   **It is a week old with zero stars and zero importers and labels itself
   experimental.** Worth evaluating, not worth depending on yet — and it would
   solve stages 2 *and* 3 at once if it turns out to be real.

Whichever is chosen, note that `wimlib` is not a *new* requirement: CrystalFetch
needs the identical tool and simply hides it inside its app bundle. The change
is that ours would be visible.

---

## The one real risk, and how to retire it on day one

Stage 3 is the risk, and its failure mode is the bad kind: a disc image that
mounts perfectly on macOS and that Windows Setup or UTM's firmware silently
ignores. No error, no log — a black screen or a UEFI shell prompt.

Two things say take that seriously rather than discovering it late:

- **This repository has already been bitten by exactly this.** From the trap
  table: *"ISO padded past its declared volume size — mounts fine on macOS,
  ignored by Setup. Trim to the PVD size."*
- **CrystalFetch's authors did not trust `hdiutil`.** It can do the job on
  paper — `makehybrid` offers `-iso -udf -eltorito-boot -no-emul-boot`, all of
  what is needed. Their script has it **commented out** and ships a bundled
  `mkisofs` instead. People who solved this looked at the free option and
  walked away.

### Step 0 — remaster the ISO we already have, and boot it

The risk can be tested **with none of the rest of this plan**: no download, no
`wimlib`, no LZMS. We have a working ISO. Take its contents and rebuild it.

```
hdiutil attach -readonly -nobrowse .cache/win11-arm64.iso
cp -R /Volumes/<label>/ /tmp/isotree/          # ~5 GB, keep the layout exactly
hdiutil makehybrid -o /tmp/remastered.iso /tmp/isotree \
    -iso -udf -eltorito-boot /tmp/isotree/efi/microsoft/boot/efisys_noprompt.bin \
    -no-emul-boot -udf-volume-name <label> -iso-volume-name <label>
irgo-winvm verify -iso /tmp/remastered.iso
irgo-winvm up -iso /tmp/remastered.iso -name iso-test
```

About an hour, and it answers the biggest unknown before anything is committed:

- **It boots** → stage 3 needs no installed tool at all, and using
  `efisys_noprompt.bin` may also delete the eight-keypress hack.
- **It does not** → we need `mkisofs`, and we know on day one rather than day
  five, with the dependency question answered by evidence instead of taste.

Either way `-name iso-test` is a throwaway VM; `irgo-win11` is untouched, and
the source ISO is immutable and read-only mounted.

### Step 0, first result: `hdiutil` CAN master it, and `-hide-iso "*"` breaks it

Run on 12 Aug 2026. The remaster itself took **10 seconds** and produced
5,268,756,480 bytes against the original's 5,267,005,440 — and
`irgo-winvm verify` reports the two as identical (ARM64, no-prompt loader
present).

It does **not** boot, and the reason is worth writing down.

Copying CrystalFetch's `--hide "*"` as `hdiutil -hide-iso "*"` was wrong. That
flag hides every file from the ISO9660 side so that only UDF carries them,
which is how they dodge the 4 GiB limit. But UTM's EDK II firmware then maps
the **empty ISO9660 volume** as `FS0:`, and the boot loader is not in it:

```
Shell> fs0:
FS0:\> \efi\boot\bootaa64.efi
FS0:\>                          <- returned instantly, nothing booted
```

The firmware did enumerate the disc — `FS0: ... /CDROM(0x0)` appears in the
mapping table — so the El Torito and UDF structures are sound. Only the
contents of the ISO9660 side are wrong.

The fix is to hide **only the file that cannot fit**, not everything:
`-hide-iso "sources/install.wim"`. Boot files stay in ISO9660 where the
firmware reads them; the 4.099 GiB `install.wim` comes from UDF, where Windows
Setup reads it. That is the shape a real Windows ISO has, which is why
`InspectISO` — an ISO9660 reader — finds `efi/boot/bootaa64.efi` in the
original at all.

**This is what Step 0 is for.** The mistake cost ten seconds of compute and one
throwaway VM on the first afternoon, instead of surfacing after `wimlib`, a
download and a week's work, where it would have looked like an ESD problem.

### Step 0, second result: it is not the hiding — `hdiutil` itself does not work

Rebuilt with **nothing hidden**. `hdiutil` accepted the 4.099 GiB `install.wim`
without complaint and produced 5,267,238,912 bytes against the original's
5,267,005,440 — **233 KB apart**, and `irgo-winvm verify` again calls the two
identical.

It fails in exactly the same way:

```
FS0:\> \efi\boot\bootaa64.efi
FS0:\>                          <- instant return, no boot, no error
```

So the `-hide-iso` mistake was real but was not the cause. Two independent
`hdiutil makehybrid` outputs, one hiding everything and one hiding nothing,
both enumerate as `FS0: ... /CDROM(0x0)` and both refuse to boot, while the
original ISO boots from the same shell command on the same firmware.

**Conclusion: `hdiutil makehybrid` cannot master bootable Windows ARM64 media
for UTM's EDK II firmware.** That is the answer Step 0 existed to get, it took
an afternoon rather than a week, and it agrees with the one piece of evidence
we had going in — CrystalFetch ships a bundled `mkisofs` and has its `hdiutil`
line commented out. They will have found this too.

**The zero-dependency option is therefore closed**, and the dependency question
below is decided by evidence rather than taste: an ISO masterer has to be
installed. `wimlib` was already required for stage 2; this makes it two tools,
which is exactly what CrystalFetch bundles.

### What is being tested now: `xorriso`

`brew install xorriso` (bottled, 1.5.8). Two things learned immediately:

- **`xorriso -as mkisofs` has no `-udf`.** UDF writing is cdrtools' `mkisofs`,
  not xorriso — so if UDF turns out to be required, the dependency is
  `cdrtools`, not `xorriso`.
- **`-iso-level 3` accepts the 4.099 GiB `install.wim`** as a multi-extent
  file, and `-e` (rather than `-b`) marks the El Torito entry as EFI platform
  0xEF, which is what a UEFI-only ARM64 disc needs. Produced 5,264,431,104
  bytes.

Whether Windows Setup will *read* a multi-extent ISO9660 file is the open
question — Microsoft ships UDF precisely because their CDFS driver handles
multi-extent badly. If it boots but Setup then fails to find `install.wim`, the
answer is cdrtools and UDF.

### Step 0, third result: **xorriso works. This is solved.**

The xorriso image (5,264,431,104 bytes) booted UTM **straight into Windows
Setup**, with no "Press any key", and installed unattended.
`docs/screens/self-built-iso-installing-windows.png` is the picture.

Everything that was open is now answered:

- **`xorriso` masters media UTM boots.** `hdiutil` does not, twice over.
- **UDF is not required.** Setup read the 4.099 GiB `install.wim` from ISO9660
  level 3 as a multi-extent file. This was the one thing genuinely in doubt —
  Microsoft ships UDF and their CDFS driver is known to handle multi-extent
  badly — and it turns out not to matter here.
- **Therefore `cdrtools` is not needed.** One masterer, `xorriso`, one formula.
- **`efisys_noprompt.bin` works**, which means self-built media can drop the
  eight-keypress boot hack entirely.

**Total external dependencies for the whole CrystalFetch replacement: two.**
`wimlib` (reads .esd) and `xorriso` (writes the ISO) — exactly the two
CrystalFetch bundles inside its app, and one of them was already installed.

Two practical notes for whoever runs a boot test, both learned the hard way:

- `boot` does not return for a VM booting Setup, because a guest agent only
  exists once Windows is installed. Judge it by
  `irgo-winvm screenshot -vm iso-test`, not by waiting for the command.
- Delete the previous `iso-test` bundle *and relaunch UTM* first. UTM lists a
  deleted VM until it is relaunched, so creating one under an existing name
  reuses the old bundle and silently tests the **previous** ISO. That is how a
  boot test lies to you, and it cost an hour here.

`mise run vm:iso-test <iso>` does both.

## Order of work

0. **Step 0 above.** Retire the only serious risk before building anything on
   top of it.
1. **LZX for the catalog CAB.** Removes the CrystalFetch-cache fallback. Small,
   self-contained, no dependency, and makes `fetch-iso` honest on a clean
   machine. Independent of everything else, so it can proceed in parallel.
2. **Decide the dependency question** — with step 0's result in hand, which
   turns it from a preference into a costed choice.
3. **Evaluate `go-sdk-winmediafoundry`** against our one ESD — a day, and if it
   works it collapses stages 2 and 3 into a library call. If it does not, we
   have lost a day and learned the ceiling.
4. **Stage 2 via `wimlib`.** Mechanical: the seven commands above, in Go, with
   the exit codes checked.
5. **Stage 3.** Whichever masterer the decision picked, with
   `efisys_noprompt.bin` as the El Torito image.
6. **Verify properly.** `irgo-winvm verify` already checks an ISO is ARM64 and
   can boot unattended. The real test is `irgo-winvm up` completing an
   unattended install from a self-built ISO with **no keystrokes at the CD
   prompt** — which is also the proof that step 5 got El Torito right.

## Safety, which is not optional here

The working ISO is hardlinked into UTM's bundle: `.cache/win11-arm64.iso`,
`~/Downloads/...en-us.iso` and the VM's `Data/install.iso` are **one file with
three names**. Anything that opens one for writing empties all three, including
the media the VM boots from.

It is therefore immutable (`irgo-winvm iso -protect`, already applied), and
`fetch-iso` refuses to write to a path that is in use. Both were done before any
of this was written, and both stay.

Re-fetching costs 4.2 GB from a rate-limited source. Nothing in this plan is
worth risking that; when in doubt, write to a new path and link it into place
after verifying.
