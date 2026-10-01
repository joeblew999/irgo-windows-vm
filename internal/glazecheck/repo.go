package glazecheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// repoModule is the first line of the root go.mod of a checkout.
const repoModule = "github.com/joeblew999/irgo-windows-vm"

// ErrNoRepo is the check being run somewhere it cannot work: outside a
// checkout, where there are no examples to build.
var ErrNoRepo = errors.New("not inside a checkout of irgo-windows-vm")

// StatusFile is where the verdict is recorded, relative to the checkout.
const StatusFile = "docs/GLAZE-STATUS.md"

// FindRepo returns the root of the checkout this is running in.
//
// The working directory first, then the directory the binary is in: `.mcp.json`
// runs .bin/irgo-winvm from the root, but an MCP client may start the server
// with some other working directory, and the binary inside .bin/ still knows
// where it came from. Every place looked is named in the error, because "not
// found" without a location cannot be checked.
func FindRepo() (string, error) {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if self, err := os.Executable(); err == nil {
		if real, rErr := filepath.EvalSymlinks(self); rErr == nil {
			self = real
		}
		starts = append(starts, filepath.Dir(self))
	}
	for _, s := range starts {
		if root, ok := repoAbove(s); ok {
			return root, nil
		}
	}
	return "", fmt.Errorf("%w: looked upwards from %s for a go.mod saying `module %s` with examples/ beside it.\n"+
		"  glaze-check and glaze-status need the repository's source: run them from a clone of\n"+
		"  https://%s", ErrNoRepo, strings.Join(starts, " and "), repoModule, repoModule)
}

// repoAbove walks up from dir to the first directory that is a checkout.
func repoAbove(dir string) (string, bool) {
	for {
		if isRepo(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// isRepo reads the module line rather than trusting a directory name: a clone
// can be called anything, and some other project's examples/ is not these.
func isRepo(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			if strings.TrimSpace(strings.TrimPrefix(line, "module ")) != repoModule {
				return false
			}
			_, sErr := os.Stat(filepath.Join(dir, "examples", "go.mod"))
			return sErr == nil
		}
	}
	return false
}

// NeedGo refuses up front when there is no toolchain to build the examples
// with, rather than failing inside the first build with exec's wording.
func NeedGo() error {
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("glaze-check builds examples/ with Go, and there is no `go` on PATH: %w", err)
	}
	return nil
}

// Tree is the checkout's own state when a check ran.
type Tree struct {
	Commit string
	// Dirty is anything uncommitted except the status file itself: the Mac run
	// writes it, and the Windows run straight after must not then report the
	// tree it ran from as dirty because of the Mac's verdict.
	Dirty bool
	// ExamplesDirty is uncommitted change under examples/, which is what
	// decides whether a recorded verdict can later be compared to the tree.
	ExamplesDirty bool
}

// ReadTree asks git. An error is returned rather than a guess: a verdict that
// names no commit cannot be checked against anything.
func ReadTree(root string) (Tree, error) {
	commit, err := run(root, "git", "rev-parse", "HEAD")
	if err != nil {
		return Tree{}, err
	}
	dirty, err := run(root, "git", "status", "--porcelain", "--", ".", ":!"+StatusFile)
	if err != nil {
		return Tree{}, err
	}
	ex, err := run(root, "git", "status", "--porcelain", "--", "examples")
	if err != nil {
		return Tree{}, err
	}
	return Tree{Commit: commit, Dirty: dirty != "", ExamplesDirty: ex != ""}, nil
}

// Dep is glaze or native as the examples actually resolved it.
type Dep struct {
	Path    string // github.com/crgimenes/glaze
	Version string // the go.mod requirement, e.g. v0.0.61

	// Dir is set when the module is LINKED — replaced by a local directory,
	// which is what `mise run upstream:link` does through go.work. Then the
	// version above is not what was built, and the clone's branch and commit
	// are.
	Dir    string
	Branch string
	Commit string
	Dirty  bool

	// Fork is set when go.mod replaces the module with another published
	// module — examples/go.mod takes native from the joeblew999 fork until
	// its input and screen packages are released — as path@version. Then
	// that, not Version, is what was built.
	Fork string
}

// Linked reports whether the build used a local clone.
func (d Dep) Linked() bool { return d.Dir != "" }

// Key is the one-token form recorded in the status file's marker, compared by
// glaze-status to decide whether a verdict still describes this tree.
func (d Dep) Key() string {
	if d.Fork != "" && !d.Linked() {
		return d.Fork
	}
	if !d.Linked() {
		return d.Version
	}
	k := "linked@" + d.Commit
	if d.Dirty {
		k += "+dirty"
	}
	return k
}

// String is the form a person reads.
func (d Dep) String() string {
	name := filepath.Base(d.Path)
	if d.Fork != "" && !d.Linked() {
		return fmt.Sprintf("%s from the fork %s (go.mod requires %s and replaces it)", name, d.Fork, d.Version)
	}
	if !d.Linked() {
		return fmt.Sprintf("%s %s (released)", name, d.Version)
	}
	dirty := ""
	if d.Dirty {
		dirty = ", with uncommitted changes"
	}
	return fmt.Sprintf("%s LINKED to %s — branch %s, commit %s%s (go.mod says %s)",
		name, d.Dir, d.Branch, d.Commit, dirty, d.Version)
}

// underTest are the libraries this repository exists to test.
var underTest = []string{"github.com/crgimenes/glaze", "github.com/crgimenes/native"}

// ReadDeps asks the Go toolchain what examples/ builds against.
//
// `go list -m` from examples/, in the same environment the build runs in, so
// it sees the root go.work exactly as the build does. Reading go.mod instead
// would report v0.0.61 while the build used a local clone — the one mistake
// this record exists to make impossible. It also returns GOWORK, the workspace
// file in effect, or "" when there is none.
func ReadDeps(root string) (deps []Dep, gowork string, err error) {
	ex := filepath.Join(root, "examples")
	gowork, err = run(ex, "go", "env", "GOWORK")
	if err != nil {
		return nil, "", err
	}
	raw, err := run(ex, "go", append([]string{"list", "-m", "-json"}, underTest...)...)
	if err != nil {
		return nil, "", err
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	for {
		var m struct {
			Path, Version string
			Replace       *struct{ Path, Version, Dir string }
		}
		if dErr := dec.Decode(&m); errors.Is(dErr, io.EOF) {
			break
		} else if dErr != nil {
			return nil, "", fmt.Errorf("reading go list -m output: %w", dErr)
		}
		d := Dep{Path: m.Path, Version: m.Version}
		// A directory replacement has no version; a module replacement has one
		// and is a published module, just a different one.
		if m.Replace != nil && m.Replace.Version != "" {
			d.Fork = m.Replace.Path + "@" + m.Replace.Version
		}
		if m.Replace != nil && m.Replace.Version == "" {
			d.Dir = m.Replace.Dir
			if d.Dir == "" {
				d.Dir = m.Replace.Path
			}
			if d.Branch, err = run(d.Dir, "git", "branch", "--show-current"); err != nil {
				return nil, "", err
			}
			if d.Branch == "" {
				d.Branch = "(detached)"
			}
			if d.Commit, err = run(d.Dir, "git", "rev-parse", "--short=12", "HEAD"); err != nil {
				return nil, "", err
			}
			st, sErr := run(d.Dir, "git", "status", "--porcelain")
			if sErr != nil {
				return nil, "", sErr
			}
			d.Dirty = st != ""
		}
		deps = append(deps, d)
	}
	if len(deps) != len(underTest) {
		return nil, "", fmt.Errorf("go list -m in %s named %d of the %d libraries under test", ex, len(deps), len(underTest))
	}
	return deps, gowork, nil
}

// run is a command in dir, its trimmed stdout, and its stderr in the error.
func run(dir, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s in %s: %w: %s", name, strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
