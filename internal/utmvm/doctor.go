package utmvm

import (
	"os"
	"path/filepath"
)

// mediaPath is the media doctor should report: whatever iso-create would use.
//
// It reported "MISSING" while iso-create resolved media in milliseconds,
// because it looked only for the downloaded name and this project usually has
// the built one. A diagnostic that disagrees with the thing it diagnoses is
// worse than no diagnostic.
func mediaPath() string {
	for _, p := range []string{isoBuiltPath(), isoPath()} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return isoPath()
}

// Records are what a run leaves behind: the log, and the screenshots.
//
// Reported by doctor because a file nobody can find is a file nobody uses, and
// both of these exist precisely for the moment something went wrong — which is
// exactly when hunting for them is hardest.
func Records() []External {
	return []External{
		{
			Name: "log",
			Path: LogPath(),
			Why:  "every line every command printed, with timestamps. Appended to across runs.",
			Fix:  "written on the first command that runs",
		},
		{
			Name: "screenshots",
			Path: ShotDir(),
			Why:  "one per install stage, and one whenever a boot is driven. What a stuck VM actually looks like.",
			Fix:  "written during vm-create, or on demand with irgo-winvm vm-screen",
			Dir:  true,
		},
	}
}

// Externals returns every file and directory outside the repository that the
// project relies on, in the order a new machine acquires them.
//
// There is a lot of it, it is large, and none of it is in git — so a clone is
// nowhere near enough to run any of this, and the gap is invisible until
// something fails a long way from the cause. A missing guest-tools ISO does not
// say "missing guest-tools ISO"; it says the VM has no network and `utmctl
// exec` does nothing.
//
// Each entry names what it is, where it is, why it is outside, and how to get
// it back. `irgo-winvm doctor` prints them with their real sizes and whether
// they are actually there, so the answer is measured rather than remembered.
//
// The sizes are the reason for most of it: ~33 GB of ISO and disk image, which
// belongs in git under no circumstances, plus a VM bundle that is machine state
// rather than source.
//
// It once took a repoRoot and skipped the entries inside the working tree when
// that was empty. It takes nothing now: none of what it reports lives in the
// tree.
func Externals() []External {

	// One layout, reported as it is. This used to rewrite Cache, Bin and Work
	// to repo-relative directories when run inside a checkout, and consult
	// IRGO_* variables that no longer exist — so doctor described a third set
	// of locations that neither the ISO code nor anything else used.

	list := []External{
		{
			Name: "Go toolchain",
			Path: lookPath("go"),
			Why: "pinned in mise.toml, which every go.mod's `go` directive matches, so CI and a " +
				"maintainer build with the same Go. Outside mise the directive is a floor and the " +
				"toolchain mechanism fetches what a module needs.",
			Fix: "mise install, or any Go at least as new as go.mod says",
		},
		{
			Name: "UTM.app",
			Path: AppPath,
			Why:  "the hypervisor. Everything here drives it through utmctl, which lives inside the bundle.",
			Fix:  "irgo-winvm vm, which downloads and installs it",
		},
		{
			Name: "UTM guest tools ISO",
			Path: guestToolsPathOrEmpty(),
			Why: "the QEMU guest agent and the virtio-net driver. Without it a VM boots and " +
				"is then unreachable: no network, no `utmctl exec`, no IP.",
			Fix: "open UTM once and let it download them; there is no supported way to fetch them ourselves",
		},
		{
			Name: "Windows 11 ARM64 ISO",
			Path: mediaPath(),
			Why: "the installation media. Microsoft's, not redistributable, and 5 GB. " +
				"Downloaded or built by irgo-winvm iso-create.",
			Fix: "irgo-winvm iso-create -fetch",
		},
		{
			Name: "Windows 11 ARM64 .esd",
			Path: ISOSourcePath(),
			Why: "what the ISO is built FROM, and the one thing here that cannot be rebuilt " +
				"locally. iso-delete keeps it on purpose and only -all removes it; without it, " +
				"rebuilding the ISO means downloading 4.2 GB from Microsoft again.",
			Fix: "irgo-winvm iso-create -fetch",
		},
		{
			Name: "the VM itself",
			Path: vmDirOrEmpty(),
			Why: "machine state, not source: a 64 GB sparse disk with Windows installed on it. " +
				"Rebuildable from the ISO in about an hour, unattended.",
			Fix: "irgo-winvm vm",
			Dir: true,
		},
	}

	// One inode set across the whole inventory, not one per entry.
	//
	// The Windows ISO is hardlinked into at least three of these — ~/Downloads,
	// .cache, and the VM bundle's Data/install.iso — because that is how it
	// gets used twice without costing 10 GB. Summing the entries naively
	// reports 15 GB of ISO that does not exist, and the total is the number
	// somebody compares against their free space.
	//
	// Nothing prints a total any more — TotalBytes had no caller once doctor
	// became one table, and was deleted on 30 Sep 2026 — but the rows still
	// depend on this: without it the VM bundle's row counts the ISO it
	// hardlinks as its own.
	//
	// First entry to claim an inode owns it; the rest do not count it again. The
	// order above therefore matters, and the ISO is listed before the VM bundle
	// deliberately, so the story reads "the bundle re-uses the cached ISO"
	// rather than the reverse.
	seen := map[uint64]bool{}
	out := make([]External, 0, len(list))
	for _, e := range list {
		e.stat(seen)
		out = append(out, e)
	}
	return out
}

// External is one thing outside the repository that the project relies on.
type External struct {
	Name string
	Path string
	Why  string
	Fix  string
	Dir  bool

	// Filled in by stat.
	Present bool
	Bytes   int64 // blocks this entry is the first to account for
}

// stat measures the entry, counting each inode's blocks once across the whole
// inventory. seen carries that state between entries.
func (e *External) stat(seen map[uint64]bool) {
	fi, err := os.Stat(e.Path)
	if err != nil {
		return
	}
	e.Present = true
	if !fi.IsDir() {
		e.add(e.Path, seen)
		return
	}
	_ = filepath.WalkDir(e.Path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			// An unreadable subtree is not worth failing an inventory over —
			// UTM's container has directories this process cannot enter.
			return nil //nolint:nilerr
		}
		e.add(p, seen)
		return nil
	})
}

// add accounts for one file, using ALLOCATED blocks rather than apparent size.
// The VM's disk image is a 64 GB sparse file that has only ever touched ~28 GB;
// reporting the 64 would make the total a fiction.
func (e *External) add(p string, seen map[uint64]bool) {
	used, ok := diskUsage(p)
	if !ok {
		return
	}
	ino, nlink, haveInode := inodeInfo(p)
	if haveInode && nlink > 1 {
		if seen[ino] {
			return
		}
		seen[ino] = true
	}
	e.Bytes += used
}

// vmDirOrEmpty is UTM's bundle directory, or empty when it cannot be resolved.
// Only for the inventory, which reports rather than acts.
func vmDirOrEmpty() string {
	d, err := DefaultVMDir()
	if err != nil {
		return ""
	}
	return d
}

// guestToolsPathOrEmpty is where UTM caches its guest tools, or empty when it
// cannot be resolved. Asked of the vm code rather than spelled out again:
// doctor reports on UTM, it does not know where UTM keeps things.
func guestToolsPathOrEmpty() string {
	p, err := guestToolsPath()
	if err != nil {
		return ""
	}
	return p
}
