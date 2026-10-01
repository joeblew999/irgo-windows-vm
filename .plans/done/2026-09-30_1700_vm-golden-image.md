# A ready Windows VM in minutes: build a golden image once, then clone it

Status: phases 0–1 built and measured 1 Oct 2026 (a VM of your own in 23 s, docs/RESULTS.md); phase 2 (the R2 cache) merged, Worker transport in progress · 2026-10-01
## What was built (phase 1), and where it differs from the plan below

Branch `worktree-agent-ab62bcd315eea9030`. Unit-tested with negative controls;
nothing here has touched UTM or a VM yet.

- **`autounattend.xml`**: `PreventDeviceEncryption` in specialize, under
  `Microsoft-Windows-Deployment` (the only component whose `RunSynchronous`
  runs in that pass). Test checks pass and component.
- **Locks**: machine (`mutation.lock`, kept the old name), per VM
  (`mutation-vm-<name>.lock`, case-folded, UUIDs resolved to names, odd names
  hashed) and stage (`mutation-stage.lock`, for `bin/`, which `app-upload`
  writes and `app-delete` clears). Declared per command as `Locks` in
  `internal/command` (it replaced `Mutates`). All-or-nothing acquisition;
  refusals name the busy lock.
- **`vm-golden-create -vm <src>` / `vm-golden-delete`** as planned. Differences:
  the clone is AppleScript `duplicate … with properties {configuration:{name,
  drives, network interfaces}}`, not `utmctl clone` — one call that clones,
  renames (UTM moves the bundle to `<name>.utm`), drops every drive but the NVMe
  disk and sets the MAC, which is read back and compared. No per-region SHA-256
  in the manifest: this process cannot read `disk.img` (App Data protection),
  and the hashes are only needed for phase 2. `golden.json` lives under the
  application root. Over MCP it is always a job (`command.DetachAlways`).
- **`vm-create`**: clones the golden image when there is one (`-golden=false`
  to install instead) and says when it falls back. Drive IDs are not
  regenerated: UTM's scripting makes them read-only, and the plan called it
  optional.
- **Found while building, and fixed, beyond the plan**: this process is refused
  *writes* to UTM's container too (`touch` → Operation not permitted), so the
  old `vm-create` could not make a bundle at all from an agent session, and the
  guest-tools ISO in UTM's cache could be neither read nor linked. So
  `vm-create` now writes the bundle to `vm/staging/` and has UTM `import` it
  (no restart), keeps the guest tools under `vm/`, ejects the install medium
  with `update configuration` instead of a plist edit, and clones the media
  instead of hardlinking it (a clone copies BSD flags, measured, so the clone's
  `uchg` is cleared). **Nothing restarts UTM any more**; `RestartUTM` and the
  guard it briefly had are gone. Also: `vm-delete` ran `utmctl` by bare name
  (worked only where Homebrew linked it); self-booting was asked of the
  bundle's ISO copy (a different inode, so "not self-booting", the answer that
  types at Setup).

Left: phase 0 on a disposable VM (next), then the numbers below replace the
estimates. Phase 2 untouched.

## Symptom

A new developer, CI runner or agent that wants a Windows 11 ARM64 VM has to run
`iso-create` and then `vm-create -install`:

- `iso-create` from nothing is measured at 250 s (RESULTS.md).
- The install is **about 45 minutes**. That figure is still an estimate: ROADMAP
  lists the long job as the one unverified claim.
- `vm-create` calls `RestartUTM()` (vm_create.go:197, 957), which quits UTM and
  so stops every VM that is running. You cannot make a second VM while
  `irgo-win11` is in use without interrupting it.
- The mutation lock is a single machine-wide `flock` (lock_darwin.go), and
  `app-create` takes it. So even with several VMs, only one `app-create` can run
  on the machine at a time.

The goal: any developer or agent gets a usable VM in minutes. Several agents on
one Mac should each get their own VM.

## Recommendation, in one paragraph

Build the golden image **locally, once per machine**, with `vm-create` on a
throwaway VM name. **Seal** it: turn off BitLocker, turn off hibernation, clean
up the component store, TRIM, shut down. Keep the sealed bundle under the app
root. Give every agent its own VM as an **APFS clone** of that bundle, with a
fresh UUID, MAC and name, registered through UTM without restarting it. A
clone's disk costs nothing up front and nothing to create (measured below).

Offer **Cloudflare R2 only as a private, owner-only cache** for the owner's own
licensed machines and CI. Store the image as sparse-aware, fixed-offset,
zstd-compressed, SHA-256-addressed chunks, and download it with the existing
resumable, verifying downloader. **Never use a public bucket, GitHub Release or
public OCI image.** The Windows licence does not allow redistribution (see
Legal), so that decides the design: build locally by default, private cache
optional.

## Evidence (measured 2026-09-30 on this Mac: M2 Pro, 12 cores, 16 GiB, macOS 27.0, UTM 4.7.5)

The rules were followed: `irgo-win11` was never started, stopped, suspended or
edited. The only guest-side actions were read-only queries run through
`utmctl exec`, which wrote their output to `C:\Windows\Temp\golden-probe*.txt`
and then deleted it. `utmctl list` showed it `started` before and after.

### 1. What is in the bundle, and how big it really is

The bundle's directory cannot be listed from a shell: `ls` and `cat` in
`~/Library/Containers/com.utmapp.UTM/Data/Documents` return **`Operation not
permitted`**, even unsandboxed. This is macOS App Data protection (TCC). `stat`
on a known path does work, which is how `doctor` reports "ok". **This matters for
the design:** a tool that `cp`s the bundle out of UTM's container needs Full
Disk Access for its host process. Otherwise the copy has to be done by UTM
itself (`utmctl clone`, AppleScript `export`), because UTM can read its own
container.

```
stat -f "%N %z bytes, %b blocks, links=%l" <bundle>/Data/*
```

| file | apparent | allocated | note |
|---|---|---|---|
| `Data/disk.img` | 68,719,476,736 (64 GiB) | 48,586,256 × 512 = **24.88 GB (23.2 GiB)** | **raw** sparse file, from `Truncate` in `createSparse`. Not qcow2. UTM passes no `format=`, so QEMU probes it as raw |
| `Data/install.iso` | 5,264,431,104 | 5.26 GB | **links=1, a separate copy**, not the hardlink `linkOrCopy` intends (media ISO inode 280256482, bundle's 280093019). A golden image must leave it out |
| `Data/guest-tools.iso` | 127 MB | shared | links=2, hardlinked to UTM's copy |
| `Data/unattend.iso` | 69,632 | | not needed after install |
| `Data/efi_vars.fd` | 655,360 | | UEFI NVRAM. Holds the Windows Boot Manager entry, so it must travel with the disk |
| `Data/tpmdata` | 16,576 | | **swtpm state for the TPM 2.0 device**, a plain file (see 2) |
| `config.plist`, `screenshot.png` | 3,690 / 671,983 | | |

`qemu-img` is not installed, and it could not open the file anyway because of
TCC. What `qemu-img info` would report is already known: format raw, virtual
size 64 GiB, disk size 23.2 GiB.

Inside the guest (read-only queries):

| | value |
|---|---|
| OS | Windows 11 Pro 10.0.26100 ARM64, installed 13 Aug 2026 |
| C: | 67.58 GB, 36.33 GB free, so **31.25 GB used** (NTFS) |
| `hiberfil.sys` / `pagefile.sys` / `swapfile.sys` | 3.43 GB / 0.54 GB / 0.02 GB |
| WinSxS | 11.04 GB actual, **6.50 GB of it "Backups and Disabled Features"**, cleanup recommended |
| TRIM | `DisableDeleteNotify = 0`: the guest sends TRIM |
| **BitLocker** | **`Used Space Only Encrypted`, 100 %, XTS-AES 128, Protection Off, Key Protectors: None Found** |
| TPM | present, 2.0, IBM (swtpm), initialised, ready for storage |
| activation | RETAIL channel, key ends 3V66T (the generic KMS client key), **License Status: Notification, 0xC004F034**, i.e. unactivated |
| `dev` | enabled, PasswordExpires empty (never) |
| WebView2 | 154.0.4258.37 |
| QEMU-GA | running |
| machine SID prefix | S-1-5-21-2037460206-1020179993-2821855600; ComputerName `WIN11ARM` |

NTFS reports 31.25 GB used, but the host has only 24.88 GB allocated. UTM
passes `discard=unmap,detect-zeroes=unmap` on every writable drive
(UTMQemuConfiguration+Arguments.swift L872-880, v4.7.5). So zero or trimmed
regions may be holes on the host. Whether QEMU on macOS actually punches holes
in the raw file is **not determined**. Phase 0 measures it.

**The finding that changes the plan: Windows 11 24H2 turned on automatic Device
Encryption by itself.** Every used block on `disk.img` is XTS-AES ciphertext,
and ciphertext does not compress:

```
dd if=/dev/urandom of=rand.bin bs=1m count=2048     # stands in for XTS-AES output
zm rand.bin   ->  zstd fastest 100.0 %, default 100.0 %  (2 GiB in, 2 GiB out)
```

So compressing today's bundle as it is would save **nothing**. It would upload
about 25 GB. The golden image has to be decrypted (`manage-bde -off C:`), and new
installs should be kept from encrypting at all (see the change list).

### 2. How compressible a decrypted Windows install is

No decrypted installed disk is readable here, so this is a proxy: Windows 11
Pro (ESD index 6) applied as files, then tarred.

```
wimlib-imagex info win11-arm64.esd       # index 6 "Windows 11 Pro": 23.27 GB total, 10.41 GB hardlinked
wimlib-imagex apply win11-arm64.esd 6 pro/    # 94 s; du 12.2 GiB
tar -cf pro.tar -C pro .                      # 144 s; 13,300,642,816 bytes
zm pro.tar                                    # klauspost/compress v1.20.1, 12 goroutines
```

| zstd level | output | ratio | compress | decompress |
|---|---|---|---|---|
| fastest (≈1) | 6.22 GB | 46.8 % | 28 s (478 MB/s) | 8 s (1.6 GB/s) |
| default (≈3) | 5.85 GB | **44.0 %** | 34 s (392 MB/s) | 8 s (1.7 GB/s) |
| better (≈7) | 5.57 GB | 41.9 % | 48 s (275 MB/s) | 8 s (1.8 GB/s) |

- SHA-256 in Go (`crypto/sha256`, ARMv8 SHA extensions): **1,522 MB/s** on the
  same 13.3 GB file (8.7 s). `shasum -a 256` took 42.8 s.
- APFS clone of that 13.3 GB dense file: **`cp -c` real 0.00 s**, and `df`
  unchanged. The R2 research agent also cloned a 20 GiB sparse file in 3 ms, and
  it stayed sparse.
- Download from Cloudflare's edge (`speed.cloudflare.com/__down`, 8 × 25 MB,
  one connection): **46 to 62 MB/s, about 55 MB/s**.

Estimated size of a sealed golden image. This is an estimate, labelled as one;
Phase 1 replaces it with a measurement:

- 24.9 GB allocated today
- less hiberfil (up to 3.4 GB)
- less WinSxS backups after `/ResetBase` (up to 6.5 GB)
- that leaves roughly **15 to 18 GB of data**
- at the measured 42 to 47 %, that is **about 7 to 8.5 GB compressed**

Estimated wall clock for a new machine pulling from R2:

| step | time |
|---|---|
| download 8 GB at 55 MB/s | about 2.5 min |
| verify | overlaps the download (1.5 GB/s) |
| decompress 17 GB | about 10 to 15 s |
| first boot to agent | **unmeasured** |

Target: **under 5 minutes, against 250 s plus about 45 minutes today.** On one
machine, a clone of a local golden image costs 0 s plus boot.

### 3. What must change per copy, and what does not

From UTM's source at v4.7.5 (Platform/UTMData.swift, Services/UTMSWTPM.swift,
Services/UTMQemuVirtualMachine.swift) and `utmctl help`:

| item | must change? | what UTM does / what to do |
|---|---|---|
| `Information.UUID` | yes | `utmctl clone` (since 4.2.3) always assigns a new UUID. `listRefresh`/`listAdd` re-UUID a colliding bundle and **show two VMs, not refuse**, but it is uncertain whether the new UUID is written to `config.plist`. Write a fresh UUID ourselves, so nothing depends on that |
| MAC address | **yes, and UTM does not do it** | `clone` regenerates MACs only if the global setting `IsRegenerateMACOnClone` is on, **default false** (added 4.7.2). Two running clones with one MAC on Shared/vmnet would compete for one DHCP lease. Write `randomMAC()` ourselves |
| VM name | yes | `-vm` / `--name` |
| drive `Identifier`s | optional | clone keeps them, so the NVMe `serial=` stays the same. Harmless for a standalone guest. Regenerate anyway, since `newUUID()` exists |
| `efi_vars.fd` | **no, must be kept** | holds the Windows Boot Manager entry. Clone copies it |
| `tpmdata` | **keep with its disk** | swtpm runs with `--tpmstate backend-uri=file://Data/tpmdata --tpm2` and no key or keychain, so it is not tied to the host. Its only link to the UUID is the socket filename. **`--disposable` does not cover it**: swtpm still writes `tpmdata` in place during a disposable run |
| BitLocker | **seal must decrypt** | with protectors "None Found" a copy boots today. If Windows ever adds a TPM protector, a clone given a *fresh* `tpmdata` would ask for a recovery key. Decrypt the golden image, and prevent Device Encryption at install |
| machine SID | no | irrelevant for standalone, non-domain VMs. Sysprep would reset it but re-runs OOBE, costs minutes and risks the `dev` setup. Do not sysprep |
| ComputerName `WIN11ARM` | optional | every clone has it. It only matters for NetBIOS name conflicts between clones on one network. `Rename-Computer` plus a reboot if it ever does |
| guest agent, WebView2, `dev` never-expiring password, auto-logon | no | all baked in (measured above) |
| activation | no technical change | every copy is unactivated (Notification). **Legally each running instance needs its own licence** (see Legal) |
| `install.iso`, `unattend.iso` | drop | 5.26 GB of dead weight. Leave them out of the plist's drive list and out of the bundle |

Things that do not fit:

- UTM **snapshots**: suspend is refused with NVMe ("Suspend is not supported
  when an emulated NVMe device is active"), and the snapshot feature on `main`
  (5.0 beta) needs qcow2. The disk is raw and NVMe is required.
- **`utmctl start --disposable`**: QEMU `-snapshot` works with raw disks, but
  it mutates `tpmdata`, and one registered VM can only run once at a time. It
  resets a single VM; it does not run VMs in parallel.
- **Registering a copy**: a bundle dropped into `Documents` is found only when
  UTM's main window appears, which in practice means `RestartUTM`, and that
  kills running VMs. Two routes work without a restart:
  - `utmctl clone <golden> --name X`. The golden VM must be registered and
    stopped. UTM uses `copyfile(.clone|.dataSparse)`, so the copy is instant
    and sparse on APFS.
  - AppleScript `import new virtual machine from <file>` (4.6.1+). It copies
    with `FileManager.copyItem`, which clones on APFS. Unmeasured.

### 4. Distribution options

| option | fits? | numbers |
|---|---|---|
| **APFS clone on one Mac** | **yes, the default** | 0.00 s for 13.3 GB. Shared blocks counted once by `df`. Answers "several agents, each with a VM". Caveat: writes to a clone can hit ENOSPC later, so run the existing `CheckSpace` per clone. Time Machine backs clones up at full size (unmeasured) |
| **R2, private bucket** | **yes, owner-only** | max object 4.995 TiB. Single PUT up to 4.995 GiB. Multipart parts 5 MiB to 5 GiB, max 10,000, all but the last equal size. Standard storage $0.015/GB-month, first 10 GB free, **egress free**. Class B $0.36/M. So 15 GB stored is **$0.075/month** and 30 GB is $0.30, at 10 or 100 downloads. Presigned GET 1 s to 7 days, only on `<acct>.r2.cloudflarestorage.com`. `Range` GETs supported. Bucket-scoped read-only tokens. Multipart ETag is not a whole-file hash, so **publish our own SHA-256 manifest**. The CDN does not cache objects over 512 MB (non-Enterprise), which is another reason to chunk |
| GitHub Releases | no | 2 GiB per asset, so 4 to 13 parts. Private-repo assets need a token. Public assets would be redistribution |
| ghcr.io / ORAS | no | 10 GB per layer and a **10-minute upload timeout**. Private package pricing is "currently free", subject to change. ORAS does not resume inside a blob |
| desync (casync) over S3/R2 | not needed | Go library, BSD-3, v1.1.4 (22 Sep 2026), seed-based delta. But content-defined chunking solves *shifting* byte streams, and a disk image does not shift: blocks stay at their offsets. Reflink on extract is Linux-only, so on macOS it copies |

**Chunk format (proposed).** Walk `disk.img` with `SEEK_DATA`/`SEEK_HOLE`. For
each 64 MiB region that holds data:

- zstd-compress it (default level);
- name it by the SHA-256 of the **uncompressed** region;
- store it at `chunks/<sha256>.zst`.

`manifests/<id>.json` lists `{offset, sha256, zsize}` plus the small files
(`efi_vars.fd`, `tpmdata`, a `config.plist` template without UUID or MAC), the
Windows build, the WebView2 version and the tool version that sealed it.

- **Deltas come free.** A new golden image uploads only regions whose hash
  changed. A client with an older image fetches only the missing hashes and
  clones the rest from its local copy.
- **Holes stay holes.** Regions absent from the manifest are never written.
- **Resume per chunk.** Each chunk is small enough that a failed one simply
  restarts.

`isoDownload` (iso_create.go:355) already resumes a `.part` with `Range` and
verifies a digest before renaming. **Reuse it with a SHA-256 option.** It is
SHA-1 only today, because that is what Microsoft's catalog publishes. Do not
write a second downloader.

## Legal: this decides the design

The Windows 11 licence terms (one document now covers retail and OEM:
`microsoft.com/content/dam/microsoft/usetm/documents/windows/11/oem-(pre-installed)/UseTerms_OEM_Windows_11_English.pdf`,
Apr 2024):

- **§2a** "install and run one instance of the software on your device"
- **§2d(iv)** "If you want to use the software on more than one virtual device,
  you must obtain a separate license for each instance."
- **§2c** "you may not … publish, copy (other than the permitted backup copy),
  rent, lease, or lend the software; … transfer the software (except as
  permitted)"
- **§5** "You are authorized to use this software only if you are properly
  licensed and the software has been properly activated"
- **§2e** allows one backup copy.

Microsoft's website Terms of Use, which govern the download itself, say: "Any
reproduction or redistribution of the Software not in accordance with the
License Agreement is expressly prohibited."

What this repo installs: the **retail consumer ESD from Microsoft's catalog**,
as Pro, with the **generic KMS client key**, which selects the edition and does
not activate. It is not an evaluation image, and it is unactivated (measured:
Notification, 0xC004F034).

- **Not allowed:** a public bucket, public release asset or public OCI image of
  the installed disk. Also not allowed: mirroring the ESD or ISO, or handing the
  image to other people, unactivated or not. Unactivated does not mean
  "unlicensed is fine": §5 makes activation a condition of authorised use.
- **Allowed:** each user building their own image from Microsoft's servers and
  caching it on their own machine. This is how dockur/windows, UTM, Parallels
  and CrystalFetch all work, and it is what `iso-create` already does.
- **Allowed with conditions:** the owner keeping their own golden image in a
  **private** R2 bucket for **their own** machines and CI. Every running clone
  needs its own Windows 11 Pro licence and activation (§2d(iv)). Microsoft's
  Apple Silicon page says the same: "A unique license is required for each
  instance of Windows 11 Pro, either on hardware or in a virtual machine."
- **Grey:** a private cache shared inside an organisation, where every consumer
  holds a licence. Volume-licensing re-imaging rights normally cover that; the
  retail terms do not.
- The Enterprise Evaluation (90 days, ARM64 available, version 26H2) does not
  give a redistribution right either. Microsoft's dev VMs are discontinued: the
  page redirects since Oct 2024.

Not legal advice. But the text is unambiguous about public distribution, so the
design does not offer it.

## Cause

The expensive part is Windows Setup, and nothing keeps its result:

- the only installed VM is the shared one;
- copying it out is blocked by TCC and wasted by BitLocker;
- making another means `RestartUTM`, which interrupts it.

## The exact change, phased

Each phase ends in something measured and recorded in RESULTS.md.

### Phase 0: close the unknowns, change nothing in the repo

Run against a **disposable** VM only. The first one has to be installed the slow
way, about 45 minutes, and that install also closes ROADMAP's long-job item.

1. Time `vm-create -install` end to end, with `irgo-win11` shut down by its
   owner first, because it restarts UTM.
2. In that VM, run `Optimize-Volume -DriveLetter C -ReTrim` and read `stat %b`
   before and after. That shows whether TRIM punches holes in the raw file on
   macOS.
3. `manage-bde -off C:`, then `powercfg /h off`, then
   `Dism /Online /Cleanup-Image /StartComponentCleanup /ResetBase`, then ReTrim
   again, then a guest-initiated shutdown. Record the allocated blocks and the
   zstd size of the data regions.
4. `utmctl clone <disposable> --name <disposable>-c1`. Time it, compare
   `df`, and check whether the clone's `config.plist` UUID differs. Then run two
   clones at once: do both get DHCP leases with the same MAC? This measures
   whether the MAC rewrite is essential. Record boot-to-agent time.
5. Check whether a process without Full Disk Access can read the clone.

### Phase 1: seal and clone locally. The default, and legal for anyone

- **`assets/autounattend.xml`**: in `specialize`, add
  `reg add HKLM\SYSTEM\CurrentControlSet\Control\BitLocker /v PreventDeviceEncryption /t REG_DWORD /d 1 /f`.
  This keeps the disk compressible and removes the TPM-protector risk. Put the
  reason in a comment beside it, as the password-expiry fix did.
- **`vm-golden-create -vm <name>`** (undo: **`vm-golden-delete`**):
  - Refuses unless the source VM is a disposable one, never `DefaultVMName`
    without `-force`. Answers yes, no, or cannot tell; cannot tell refuses.
  - Runs the seal steps from Phase 0 step 3 through `utmctl exec`, polling
    rather than sleeping.
  - Shuts the guest down from inside and waits for `stopped`.
  - Gets the bytes out through UTM (`utmctl clone`), so no Full Disk Access is
    needed.
  - Stores the result as the registered, stopped VM `irgo-golden`, with
    `install.iso` and `unattend.iso` ejected and removed.
  - Writes a manifest with the Windows build, WebView2 version, tool version,
    date and SHA-256 per region.
  - Verifies by booting a clone and waiting for the agent before it reports
    success.
- **`vm-create -from-golden`** is not a new command: it is how `vm-create` gets
  its VM when a golden image exists. Undo is the existing **`vm-delete`**. It:
  - runs `utmctl clone irgo-golden --name <vm>`, with no `RestartUTM`;
  - rewrites MAC and drive IDs through AppleScript `update configuration`
    (whatever Phase 0 shows actually works);
  - starts the VM and waits for the agent;
  - prints each step with elapsed time.

  It falls back to the install path when there is no golden image, and says so.
- **Locks**: split the single `mutation.lock` into a machine lock (iso-\*,
  vm-golden-\*) and a per-VM lock `mutation-<vm>.lock` (vm-create on that name,
  app-create, vm-delete, vm-repair). Otherwise N agent VMs still serialise on
  every `app-create`. Keep flock, keep "cannot determine" refuses. Clone takes
  the machine lock only for the clone call itself, which takes seconds, so it
  cannot race `vm-golden-delete`.
- `doctor` reports the golden image: its build, age, and allocated versus
  apparent size.

### Phase 2: the owner's private R2 cache (optional)

- **`vm-golden-push <s3-url>`** and **`vm-golden-pull <s3-url>`**. Undo of
  pull is `vm-golden-delete`. Undo of push is `vm-golden-push -delete <id>`,
  with `-force`, which removes the manifest but not chunks another manifest
  references.
- Credentials come from the environment (a bucket-scoped R2 token, read-only for
  pull). Nothing is baked into the binary.
- **Refuse a bucket that allows anonymous reads.** Probe it with an unsigned
  HEAD on the manifest; readable means refuse, and cannot tell also refuses.
  Print the licence condition on every push.
- Chunk format as above. Download through `isoDownload` with SHA-256 added.
  Rebuild into `disk.img` with holes preserved, then register as in Phase 1.
- No Cloudflare resources are created by this plan. The owner makes the bucket.

**Built 30 Sep–1 Oct 2026** (branch `worktree-agent-a5ef843430003b858`, on
main, independent of phase 1). Transport only: a bundle directory to the
bucket and back. docs/DEVELOPMENT.md, "The private R2 cache", is the reference.

- `vm-golden-push -bundle <dir>` / `vm-golden-pull [-dir] [-id]`, always jobs
  over MCP. Undos: `vm-golden-push -delete -force [-id]` (manifest, `latest`
  moved to the newest remaining, every unreferenced chunk collected) and
  `vm-golden-pull -delete -force` (the pull directory). Both undos succeed on
  nothing.
- Format as proposed, with three changes. Regions are found by reading and a
  zero check, not `SEEK_DATA`/`SEEK_HOLE` (portable; reading holes is fast).
  The manifest records each chunk's compressed SHA-256 too, which is what
  `isoDownload` verifies, and the uncompressed SHA-256 (the name) is checked
  after decompressing. Each file also gets a tree hash (SHA-256 of its region
  SHA-256s), checked by reading the rebuilt file back.
- `isoDownload` takes a `digest` (SHA-1 or SHA-256); pull uses it with
  presigned GET URLs, so there is still one downloader.
- Privacy: the Cloudflare API's `domains/managed` and `domains/custom`; on or
  cannot tell refuses. The unsigned-HEAD probe was not built: R2's behaviour
  for an anonymous request to the S3 endpoint could not be measured here (a
  made-up account id fails the TLS handshake), and the API answers the
  question directly.
- Pulled bundles land in `golden-pull/` under the runtime data, **not yet
  registered with UTM**. Joining phase 1: have `vm-golden-create` (or a
  `vm-golden-pull` step) import `golden-pull/<bundle>` as `irgo-golden` and
  move its `golden.json` to `GoldenManifestPath`; then pull's undo becomes
  `vm-golden-delete`, and `goldenJSONName` here merges with
  `goldenManifestName` there. Push needs a readable copy (`-bundle`) because
  of TCC; an AppleScript export from UTM would be the default source.
- Tests against an in-process fake R2 (S3 subset and the two API calls):
  round trip with holes and a repeated region, delta push (one chunk), corrupt
  chunk (fetched twice, refused, nothing in place), altered manifest, wrong
  file hash, interrupted download resumed with `Range`, public and unreadable
  bucket refused before any S3 request, removal and its GC, manifest path
  traversal. Negative controls run by hand for each.
- Not verified: anything against a real R2 bucket. That is the acceptance test
  once the owner has made one.

Not doing: public hosting of any kind, GitHub Releases, ghcr.io, desync, qcow2
conversion, sysprep.

## How to verify

- Phase 0 numbers go into RESULTS.md with commands and a date:
  - install duration
  - allocated blocks before and after ReTrim and before and after decryption
  - sealed zstd size
  - clone time
  - boot-to-agent time
  - the two-clones-one-MAC result
- `vm-create -vm a1` with a golden image present gives a VM answering
  `app-create verify.exe` in minutes, and `irgo-win11` stays `started` the whole
  time. **That is the acceptance test for "does not interrupt".**
  Negative control: remove the golden image, and `vm-create` must say it is
  falling back to a full install, not silently take 45 minutes.
- Two clones run `app-create` at the same time and both succeed (per-VM lock).
  Negative control: the same VM twice must return exit 6.
- `vm-golden-create` on `irgo-win11` without `-force` exits 5.
- The chunk round trip is tested in Go against a sparse test file: holes stay
  holes (`stat %b`), and a corrupted chunk fails its SHA-256 and is refetched.
  Break the hash compare and watch the test go red.
- The R2 pull is verified against a real private bucket the owner creates.
  A public bucket must be refused, and so must one whose access cannot be
  determined.

## Scratch used and removed

Everything is gone again: the applied Pro tree (12.2 GiB), `pro.tar` (13.3 GB),
the clone and `rand.bin` (2 GiB), all deleted. Free space was 62 GiB before and
59 GiB after; APFS lags. The tools `zm` (zstd measurer) and `hs` (SHA-256
timer) were Go programs in the session scratchpad and were not added to the repo.

## Closed — 1 Oct 2026

Done: golden image, a new VM in 23 s (verified), private R2 cache through the Worker (8.4 GB, pulled byte-identical in 4 min 37 s), vm-create auto-pull. Live test of auto-pull on a fresh Mac → follow-ups.
