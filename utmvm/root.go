package utmvm

// The few things all three stages share: where everything lives, and how to
// print a path or a byte count.
//
// Deliberately small. Anything that belongs to one stage lives with that stage;
// this is only what iso, vm and run all genuinely need, and doctor is the table
// that reports on them rather than the drawer they get stuffed into.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

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
