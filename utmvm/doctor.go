package utmvm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// VerifiedVersion is the UTM release this package's config schema was read
// from — not guessed, but taken from utmapp/UTM at that tag.
//
// This matters more than it looks. UTM's config.plist is decoded by Swift
// Codable with non-optional fields, and a schema mismatch surfaces as a single
// generic "cannot import this VM" with no indication of which field is wrong.
// Reading `main` instead of the matching tag is how an afternoon disappears:
// at the time of writing main was v5.0.4 while the installed app was v4.7.5,
// and they disagreed.
const VerifiedVersion = "4.7.5"

// SchemaConfigurationVersion is the value UTM v4 accepts. UTM rejects anything
// higher outright, so a major-version jump is a real compatibility signal
// rather than cosmetic drift.
const SchemaConfigurationVersion = 4

// ErrUTMNotInstalled is returned when UTM is absent and could not be installed.
var ErrUTMNotInstalled = errors.New("UTM is not installed")

// GuestToolsURL is where UTM itself downloads the guest tools from.
//
// Taken from the string table of UTM.app's own binary rather than guessed, so
// it is the URL the application uses and not one that merely happens to work
// today.
//
// Fetching it ourselves is what makes a one-command setup possible at all. The
// alternative — and what this used to say — was to tell the developer to open
// UTM, create a throwaway VM and pick "Install Windows guest tools" from a
// menu. That is not a step a setup command can take, it is not discoverable,
// and skipping it produces a VM that boots perfectly and is then unreachable:
// no network, no `utmctl exec`, no IP, and nothing saying why.
const GuestToolsURL = "https://getutm.app/downloads/utm-guest-tools-latest.iso"

// Installing UTM from nothing.
//
// The promise this project makes is that a developer runs one binary on a
// machine with nothing on it. "First install a package manager" is one
// prerequisites for a tool whose entire job is removing them.
//
// UTM publishes a signed .dmg on its GitHub releases, so there is nothing to
// require — fetch it, mount it, copy the app out, unmount. That is the whole
// procedure, and it is the same one a person performs by hand.
//
// `hdiutil` is used to mount, and it is the one place in this repository where
// that is unavoidable: a .dmg is an APFS or HFS+ filesystem in a wrapper, and
// there is no Go implementation that reads either. The README's "no hdiutil"
// is about generating disk images, which this repo does do in Go; reading
// somebody else's signed installer is a different problem with no Go answer.

// utmReleaseAPI is the GitHub release the .dmg comes from. Latest rather than a
// pin: UTM's schema version is checked separately at DetectUTM, so a mismatch
// is reported rather than silently accepted, and pinning here would install a
// version older than the one a developer would get by hand.
const utmReleaseAPI = "https://api.github.com/repos/utmapp/UTM/releases/latest"

// ---- external tools ----
// Installing the things this project needs, in one place.
//
// There are three of them — UTM, wimlib, xorriso — and before this they were
// three different stories: UTM shelled out to a package manager inline, wimlib
// and xorriso printed a line for the developer to copy, and each had its own
// idea of what to say when it was missing. Same job, three implementations,
// three behaviours.
//
// A developer running one binary should not be handed a shopping list. If the tool
// is there, use it; if it is not, say so once, in one voice.

// Target is a desktop build a developer might need to run.
type Target string

const (
	TargetMacOS   Target = "macos"
	TargetWindows Target = "windows"
	TargetLinux   Target = "linux"
)

// Coverage describes how — or whether — the current host can run a target.
type Coverage struct {
	Target Target
	How    string // "native", "vm", or "" when unavailable
	Note   string
}

// Runnable reports whether the target can actually be exercised here.
func (c Coverage) Runnable() bool { return c.How != "" }

// Everything this project depends on that does not live in the repository.
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

// Externals returns every file and directory outside the repository that the
// project relies on, in the order a new machine acquires them.
//
// repoRoot may be empty; the entries that live inside the working tree but
// outside git are then skipped rather than guessed at.
func Externals(repoRoot string) []External {

	// One layout, reported as it is. This used to rewrite Cache, Bin and Work
	// to repo-relative directories when run inside a checkout, and consult
	// IRGO_* variables that no longer exist — so doctor described a third set
	// of locations that neither the ISO code nor anything else used.

	list := []External{
		{
			Name: "Go toolchain",
			Path: lookPath("go"),
			Why: "deliberately not pinned by mise: each go.mod's `go` directive is a floor and " +
				"the toolchain mechanism fetches what a module needs. Two managers on that job disagree.",
			Fix:  "install Go however you like; go.mod will say if it is too old",
			Kind: KindTool,
		},
		{
			Name: "UTM.app",
			Path: AppPath,
			Why:  "the hypervisor. Everything here drives it through utmctl, which lives inside the bundle.",
			Fix:  "irgo-winvm vm, which downloads and installs it",
			Kind: KindTool,
		},
		{
			Name: "UTM guest tools ISO",
			Path: mustGuestToolsPath(),
			Why: "the QEMU guest agent and the virtio-net driver. Without it a VM boots and " +
				"is then unreachable: no network, no `utmctl exec`, no IP.",
			Fix:  "open UTM once and let it download them; there is no supported way to fetch them ourselves",
			Kind: KindTool,
		},
		{
			Name: "Windows 11 ARM64 ISO",
			Path: isoPath(),
			Why: "the installation media. Microsoft's, not redistributable, and 5 GB. " +
				"Downloaded or built by irgo-winvm iso-create.",
			Fix:  "irgo-winvm iso-create -fetch",
			Kind: KindMedia,
			Skip: repoRoot == "",
		},
		{
			Name: "the VM itself",
			Path: mustVMDir(),
			Why: "machine state, not source: a 64 GB sparse disk with Windows installed on it. " +
				"Rebuildable from the ISO in about an hour, unattended.",
			Fix:  "irgo-winvm vm",
			Kind: KindState,
			Dir:  true,
		},
		{
			Name: "probe binaries",
			Path: VMStageDir(),
			Why:  "cross-compiled Windows binaries. Gitignored, rebuilt in seconds, but a restart used to lose them.",
			Fix:  "go build -o " + VMStageDir() + " ./probe/...",
			Kind: KindBuilt,
			Dir:  true,
			Skip: repoRoot == "",
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
	// First entry to claim an inode owns it; the rest report it as shared. The
	// order above therefore matters, and the ISO is listed before the VM bundle
	// deliberately, so the story reads "the bundle re-uses the cached ISO"
	// rather than the reverse.
	seen := map[uint64]bool{}
	out := make([]External, 0, len(list))
	for _, e := range list {
		if e.Skip {
			continue
		}
		e.stat(seen)
		out = append(out, e)
	}
	return out
}

// Kind groups externals by what it means when one is missing.
type Kind string

const (
	// KindTool is installed software. Missing means nothing works at all.
	KindTool Kind = "tool"
	// KindMedia is downloaded, non-redistributable media.
	KindMedia Kind = "media"
	// KindState is a machine's state — expensive to rebuild, not source.
	KindState Kind = "state"
	// KindBuilt is generated from this repository in seconds.
	KindBuilt Kind = "built"
	// KindUpstream is a clone of a dependency, needed only to fix it.
	KindUpstream Kind = "upstream"
)

// External is one thing outside the repository that the project relies on.
type External struct {
	Name string
	Path string
	Why  string
	Fix  string
	Kind Kind
	Dir  bool
	Skip bool

	// Filled in by stat.
	Present bool
	Bytes   int64 // blocks this entry is the first to account for
	Shared  int64 // blocks already accounted for by an earlier entry
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
			e.Shared += used
			return
		}
		seen[ino] = true
	}
	e.Bytes += used
}

// Missing reports the externals that are not present, most important first.
func Missing(list []External) []External {
	var out []External
	for _, e := range list {
		if !e.Present {
			out = append(out, e)
		}
	}
	return out
}

// TotalBytes is what all the present externals occupy on disk.
func TotalBytes(list []External) int64 {
	var n int64
	for _, e := range list {
		n += e.Bytes
	}
	return n
}

// HumanBytes formats a byte count the way the rest of the CLI does.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n == 0:
		return "—"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// lookPath resolves a tool on PATH, or returns a name that will read as
// missing. An empty Path would stat the working directory and report present,
// which is the wrong answer stated confidently.
func lookPath(name string) string {
	t := ISOTool{Name: name}
	if !t.resolve() {
		return filepath.Join("(not on PATH)", name)
	}
	p := t.Path
	// Resolve symlinks: package-manager bin dirs are link farms, and the interesting
	// answer is which install it actually points at.
	if real, rErr := filepath.EvalSymlinks(p); rErr == nil {
		return real
	}
	return p
}

// Home shortens a path for display, so the inventory is readable rather than
// three lines of container path per row.
func Home(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || runtime.GOOS == "windows" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// A Windows 11 install consumes roughly this much once it settles. The sparse
// disk starts near zero, so the free-space figure at creation time is
// misleading — the cost arrives later, during install, when failing is most
// expensive.
const WindowsInstallBytes = 30 << 30

// SpaceCheck reports whether a target directory can host an install.
type SpaceCheck struct {
	FreeBytes     int64
	RequiredBytes int64
	ISOBytes      int64 // 0 when the ISO is hardlinked and therefore free
	OK            bool
}

func (s SpaceCheck) String() string {
	return fmt.Sprintf("%s free, ~%s needed", HumanBytes(s.FreeBytes), HumanBytes(s.RequiredBytes))
}

// CheckSpace verifies there is room before a VM is created.
//
// Worth doing up front because the failure mode is so bad: the sparse disk and
// hardlinked ISO make a new bundle look almost free, then Windows Setup runs
// out of space mid-install and leaves a corrupt VM that has to be rebuilt from
// scratch — after a 20-minute wait.
//
// The ISO costs nothing when it can be hardlinked into the same volume, so it
// is only counted when a copy would be needed.
func CheckSpace(targetDir, isoPath string) (SpaceCheck, error) {
	var s SpaceCheck

	free, err := statfsAvailable(targetDir)
	if err != nil {
		return s, fmt.Errorf("checking free space on %s: %w", targetDir, err)
	}
	s.FreeBytes = free
	s.RequiredBytes = WindowsInstallBytes

	if isoPath != "" && !sameVolume(targetDir, isoPath) {
		if n, err := fileSize(isoPath); err == nil {
			s.ISOBytes = n
			s.RequiredBytes += n
		}
	}

	s.OK = s.FreeBytes >= s.RequiredBytes
	return s, nil
}

// sameVolume reports whether two paths live on one filesystem, in which case a
// hardlink works and the ISO is free.
//
// A false answer only costs an over-estimate of the space needed, which is the
// safe direction — so a platform that cannot tell says no.
func sameVolume(a, b string) bool { return sameDevice(a, b) }

func fileSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// mustVMDir is UTM's bundle directory, or empty when it cannot be resolved.
// Only for the inventory, which reports rather than acts.
func mustVMDir() string {
	d, err := DefaultVMDir()
	if err != nil {
		return ""
	}
	return d
}

// FreeBytes is the space available to this user on the filesystem holding path.
func FreeBytes(path string) (int64, error) {
	// Walk up until something exists: the directory being asked about is often
	// the one about to be created.
	probe := path
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return 0, fmt.Errorf("utmvm: no existing parent of %s", path)
		}
		probe = parent
	}
	return statfsAvailable(probe)
}

// appRoot is the one directory this tool owns. vm, iso and run each hang their
// own locations off it, so there is a single answer to "where does anything
// go" without any of the three owning the others' paths.
func appRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Library", "Application Support", "irgo-winvm")
}

// mustGuestToolsPath is where UTM caches its guest tools, or empty when it
// cannot be resolved. Asked of the vm code rather than spelled out again:
// doctor reports on UTM, it does not know where UTM keeps things.
func mustGuestToolsPath() string {
	p, err := guestToolsPath()
	if err != nil {
		return ""
	}
	return p
}
