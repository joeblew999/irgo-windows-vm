package utmvm

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

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Externals returns every file and directory outside the repository that the
// project relies on, in the order a new machine acquires them.
//
// repoRoot may be empty; the entries that live inside the working tree but
// outside git are then skipped rather than guessed at.
func Externals(repoRoot string) []External {
	home, _ := os.UserHomeDir()
	utmData := filepath.Join(home, "Library", "Containers", "com.utmapp.UTM", "Data")

	// Honour the same overrides everything else does, so an inventory taken
	// with IRGO_CACHE_DIR set reports where the files actually are rather than
	// where they would be by default — which is the whole point of taking one.
	paths := DefaultPaths()
	if repoRoot != "" {
		paths.Root = repoRoot
		if os.Getenv("IRGO_CACHE_DIR") == "" {
			paths.Cache = filepath.Join(repoRoot, ".cache")
		}
		if os.Getenv("IRGO_BIN_DIR") == "" {
			paths.Bin = filepath.Join(repoRoot, ".bin")
		}
		if os.Getenv("IRGO_WORK_DIR") == "" {
			paths.Work = filepath.Join(repoRoot, ".work")
		}
	}

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
			Name: "mise",
			Path: lookPath("mise"),
			Why:  "runs everything in mise.toml. Nothing here requires it — the tasks are all plain `go` commands.",
			Fix:  "brew install mise",
			Kind: KindTool,
		},
		{
			Name: "UTM.app",
			Path: AppPath,
			Why:  "the hypervisor. Everything here drives it through utmctl, which lives inside the bundle.",
			Fix:  "brew install --cask utm   (or `irgo-winvm doctor`, which offers to)",
			Kind: KindTool,
		},
		{
			Name: "UTM guest tools ISO",
			Path: filepath.Join(utmData, "Library", "Application Support",
				"GuestSupportTools", "utm-guest-tools-latest.iso"),
			Why: "the QEMU guest agent and the virtio-net driver. Without it a VM boots and " +
				"is then unreachable: no network, no `utmctl exec`, no IP.",
			Fix:  "open UTM once and let it download them; there is no supported way to fetch them ourselves",
			Kind: KindTool,
		},
		{
			Name: "Windows 11 ARM64 ISO",
			Path: paths.ISO(),
			Why: "the installation media. Microsoft's, not redistributable, and 5 GB. " +
				"Hardlinked into .cache so nothing depends on ~/Downloads and the second copy costs no space.",
			Fix:  "fetch with CrystalFetch, then: mkdir -p .cache && ln <the.iso> .cache/win11-arm64.iso",
			Kind: KindMedia,
			Skip: repoRoot == "",
		},
		{
			Name: "the VM itself",
			Path: paths.VMs,
			Why: "machine state, not source: a 64 GB sparse disk with Windows installed on it. " +
				"Rebuildable from the ISO in about an hour, unattended.",
			Fix:  "irgo-winvm up -iso .cache/win11-arm64.iso -name irgo-win11",
			Kind: KindState,
			Dir:  true,
		},
		{
			Name: "probe binaries",
			Path: paths.Bin,
			Why:  "cross-compiled Windows binaries. Gitignored, rebuilt in seconds, but a restart used to lose them.",
			Fix:  "mise run probes",
			Kind: KindBuilt,
			Dir:  true,
			Skip: repoRoot == "",
		},
		{
			Name: "glaze clone",
			Path: filepath.Join(paths.Upstream, "glaze"),
			Why: "only needed to FIX glaze rather than work around it, which is this repo's rule. " +
				"Uncommitted work in here is invisible from this repository and easy to lose.",
			Fix:  "mise run upstream:clone   (then upstream:diff to see what is in it)",
			Kind: KindUpstream,
			Dir:  true,
		},
		{
			Name: "native clone",
			Path: filepath.Join(paths.Upstream, "native"),
			Why:  "as above, for crgimenes/native.",
			Fix:  "mise run upstream:clone",
			Kind: KindUpstream,
			Dir:  true,
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
	p, err := exec.LookPath(name)
	if err != nil || p == "" {
		return filepath.Join("(not on PATH)", name)
	}
	// Resolve symlinks: Homebrew's bin is a link farm, and the interesting
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
