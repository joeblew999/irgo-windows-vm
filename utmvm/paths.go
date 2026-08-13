package utmvm

// Where this project puts things, in one place, and overridable.
//
// Four of these are large — an ISO is 5 GB, a VM is 25 GB, and building an ISO
// needs room for a copy of one plus the result — so "it goes in the repo" is
// not a decision that can be made on the developer's behalf. A laptop with
// 47 GB free cannot host the working VM and a scratch remaster at the same
// time, and the fix is an external disk, not a smaller plan.
//
// It is also a safety boundary. The paths that must never be written are the
// ones already in use, and the only way to check that reliably is to have them
// all named in one place rather than spelled out at each call site.
//
// Every directory can be overridden by an environment variable, so a run can be
// pointed somewhere else without editing anything:
//
//	IRGO_ROOT          everything below defaults under this   (default: cwd)
//	IRGO_CACHE_DIR     ISOs and other large downloads         (default: <root>/.cache)
//	IRGO_BIN_DIR       cross-compiled probe binaries          (default: <root>/.bin)
//	IRGO_WORK_DIR      scratch for building images            (default: <root>/.work)
//	IRGO_VM_DIR        UTM bundles                            (default: UTM's Documents)
//	IRGO_UPSTREAM_DIR  glaze and native clones                (default: ~/workspace/go/src/github.com/crgimenes)
//	IRGO_SCREENS_DIR   screenshots, for documentation         (default: <root>/docs/screens)

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths is every directory this project reads or writes.
type Paths struct {
	Root     string
	Cache    string
	Bin      string
	Work     string
	VMs      string
	Upstream string

	// Screens is where screenshots go. Unlike the rest it is meant to be
	// COMMITTED: a screenshot of Windows Setup running from a self-built ISO
	// is evidence, and evidence that lives only on the machine that produced
	// it convinces nobody. Small PNGs, unlike everything else here.
	Screens string
}

// DefaultPaths resolves the layout, applying environment overrides.
//
// It never creates anything: a command that only reads should not leave
// directories behind, and the one that needs scratch space asks for it.
func DefaultPaths() Paths {
	root := envOr("IRGO_ROOT", "")
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}

	p := Paths{
		Root:     root,
		Cache:    envOr("IRGO_CACHE_DIR", filepath.Join(root, ".cache")),
		Bin:      envOr("IRGO_BIN_DIR", filepath.Join(root, ".bin")),
		Work:     envOr("IRGO_WORK_DIR", filepath.Join(root, ".work")),
		Upstream: envOr("IRGO_UPSTREAM_DIR", defaultUpstreamDir()),
		Screens:  envOr("IRGO_SCREENS_DIR", filepath.Join(root, "docs", "screens")),
	}

	// UTM decides this one, not us: it only reads bundles from its own
	// container. An override exists because a bundle can be built elsewhere and
	// moved, but the default is the only place UTM will look.
	p.VMs = envOr("IRGO_VM_DIR", "")
	if p.VMs == "" {
		if d, err := DefaultVMDir(); err == nil {
			p.VMs = d
		}
	}
	return p
}

// ISO is the conventional path for the Windows installation media.
func (p Paths) ISO() string { return filepath.Join(p.Cache, "win11-arm64.iso") }

// Screenshot resolves a name to a path under Screens, creating the directory.
//
// A bare name gets .png and lands in the documentation directory, so a shot
// taken while debugging is already in the right place to be committed. An
// absolute path, or one with a separator, is honoured as given — sometimes a
// screenshot really is scratch.
func (p Paths) Screenshot(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("utmvm: screenshot needs a name")
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, filepath.Separator) {
		return name, os.MkdirAll(filepath.Dir(name), 0o755)
	}
	if filepath.Ext(name) == "" {
		name += ".png"
	}
	if err := os.MkdirAll(p.Screens, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(p.Screens, name), nil
}

// EnsureWork creates the scratch directory and reports how much room is left on
// its filesystem.
//
// need is what the caller is about to use. Building an ISO copies ~5 GB out of
// one and writes ~5 GB back, so it is checked up front: running out halfway
// leaves a part-written image that looks plausible, and the failure arrives as
// a boot that hangs rather than as "no space left".
func (p Paths) EnsureWork(need int64) (string, error) {
	if err := os.MkdirAll(p.Work, 0o755); err != nil {
		return "", err
	}
	free, err := FreeBytes(p.Work)
	if err != nil {
		return p.Work, nil // not fatal; the write will say so if it matters
	}
	if need > 0 && free < need {
		return "", fmt.Errorf("utmvm: %s has %s free, and this needs about %s\n"+
			"  point somewhere with more room: IRGO_WORK_DIR=/Volumes/<disk>/irgo-work",
			p.Work, HumanBytes(free), HumanBytes(need))
	}
	return p.Work, nil
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

// CheckWritable refuses a destination that would damage something in use.
//
// Three separate refusals because each is a different mistake, and the
// consequence of getting any of them wrong is a 4.2 GB re-download from a
// rate-limited source:
//
//   - it is immutable, so somebody deliberately protected it;
//   - it is hardlinked, so writing here empties other names for the same file,
//     one of which is usually the media a VM boots from;
//   - it is inside a VM bundle, where UTM owns the layout.
func (p Paths) CheckWritable(dest string) error {
	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}

	if p.VMs != "" {
		if rel, rErr := filepath.Rel(p.VMs, abs); rErr == nil && !hasDotDot(rel) {
			return fmt.Errorf("utmvm: %s is inside UTM's bundle directory (%s)\n"+
				"  Nothing here writes there directly; use `irgo-winvm vm`.", Home(abs), Home(p.VMs))
		}
	}

	if _, err := os.Stat(abs); err != nil {
		return nil // does not exist yet, which is the normal case
	}
	if flags, ok := fileFlags(abs); ok && flags&uchgFlag != 0 {
		return fmt.Errorf("utmvm: %s is immutable — it was protected on purpose\n"+
			"  It was protected on purpose; clear the flag by hand if you mean it: chflags nouchg %s",
			Home(abs), Home(abs))
	}
	if _, nlink, ok := inodeInfo(abs); ok && nlink > 1 {
		return fmt.Errorf("utmvm: %s has %d names — it is ONE file shared with %d other place(s),\n"+
			"  and writing here empties all of them.",
			Home(abs), nlink, nlink-1)
	}
	return nil
}

func hasDotDot(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[:3] == "../"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultUpstreamDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "workspace", "go", "src", "github.com", "crgimenes")
}
