package utmvm

// Retention for everything the tool writes under Root that grows with use:
// runtime screenshots, glaze run logs, staged binaries, and what interrupted
// work leaves behind (golden-pull/.parts, vm/staging, media scratch). Each
// kind has an age and, where it accumulates, a size bound; `prune` lists what
// is past them and removes it with -force, and vm-create removes it on the
// way in, so nothing grows without limit between prunes.
//
// It never touches a VM (vm-reap), the media (iso-delete), the golden image
// or its pull (vm-golden-*), the VM records, the jobs (package job keeps 20)
// or the ledger spool (capped by package ledger). On 1 Oct 2026 shots/ held
// 239 MB, unbounded, and logs/ 5 MB.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PrunePolicy is the bound of each kind of file. Zero turns a bound off.
type PrunePolicy struct {
	ShotsAge   time.Duration
	ShotsBytes int64
	LogsAge    time.Duration
	LogsBytes  int64
	StageAge   time.Duration // staged binaries unused this long
	LeftAge    time.Duration // what interrupted work left behind
}

// DefaultPrunePolicy is the policy prune and vm-create use. Two weeks of
// screenshots is longer than any investigation has needed one, and 200 MiB is
// more than a fortnight of them (239 MB had built up over seven weeks); glaze
// logs are a month, because GLAZE-STATUS.md points at the last run's; a
// staged binary unused for a week is a build nobody will rerun; a part file
// or a staged bundle a day old belongs to work that died, since its owner
// holds the machine lock while it runs and prune takes it first.
var DefaultPrunePolicy = PrunePolicy{
	ShotsAge: 14 * 24 * time.Hour, ShotsBytes: 200 << 20,
	LogsAge: 30 * 24 * time.Hour, LogsBytes: 100 << 20,
	StageAge: 7 * 24 * time.Hour,
	LeftAge:  24 * time.Hour,
}

// PruneItem is one file or directory past its bound.
type PruneItem struct {
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"` // private bytes: what removing it frees
	Why   string `json:"why"`
	Done  bool   `json:"removed"`
	Err   string `json:"error,omitempty"`
	lock  Lock   // taken without waiting before it is removed; "" for none
}

// pruneFile is what the selection reads about one candidate.
type pruneFile struct {
	path  string
	group string // the newest of each group is always kept; "" is no group
	mod   time.Time
	bytes int64
}

// selectByBounds picks, from one kind's files, those older than age or past
// the size bound counting from the newest, never the newest of a group.
// Pure, so the rule is tested without a filesystem.
func selectByBounds(kind string, files []pruneFile, age time.Duration, limit int64, now time.Time) []PruneItem {
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	seen := map[string]bool{}
	var total int64
	var out []PruneItem
	for _, f := range files {
		total += f.bytes
		newest := f.group != "" && !seen[f.group]
		seen[f.group] = true
		var why string
		switch {
		case newest:
			continue
		case age > 0 && now.Sub(f.mod) > age:
			why = fmt.Sprintf("older than %s", humanAge(age))
		case limit > 0 && total > limit:
			why = fmt.Sprintf("past %s of %s, newest first", HumanBytes(limit), kind)
		default:
			continue
		}
		out = append(out, PruneItem{Kind: kind, Path: f.path, Bytes: f.bytes, Why: why})
	}
	return out
}

func humanAge(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return d.String()
}

// PrunePlan is everything past its bound now, without removing anything.
func PrunePlan(p PrunePolicy, now time.Time) []PruneItem {
	var out []PruneItem

	// Screenshots: the newest of each stage is what vm-screen -promote
	// copies into docs/screens, so it is kept whatever its age.
	var shots []pruneFile
	for _, f := range listFiles(ShotDir(), false) {
		g := "?"
		if m := shotName.FindStringSubmatch(filepath.Base(f.path)); m != nil {
			g = m[1]
		}
		f.group = g
		shots = append(shots, f)
	}
	out = append(out, selectByBounds("screenshots", shots, p.ShotsAge, p.ShotsBytes, now)...)

	// glaze run logs and their test2json events, the newest of each target
	// and extension kept. The command log rotates itself.
	var logs []pruneFile
	for _, f := range listFiles(LogDir(), false) {
		b := filepath.Base(f.path)
		if !strings.HasPrefix(b, "glaze-") {
			continue
		}
		target, _, _ := strings.Cut(strings.TrimPrefix(b, "glaze-"), "-")
		f.group = target + filepath.Ext(b)
		logs = append(logs, f)
	}
	out = append(out, selectByBounds("glaze logs", logs, p.LogsAge, p.LogsBytes, now)...)

	// Staged binaries, per caller, each under that caller's stage lock.
	// Every file is its own group: none is kept for being the newest.
	if dirs, err := os.ReadDir(stageRoot()); err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue // files directly in bin/ predate per-caller staging; the owner clears them
			}
			// No group: none is kept for being the newest.
			files := listFiles(filepath.Join(stageRoot(), d.Name()), true)
			for _, it := range selectByBounds("staged binaries", files, p.StageAge, 0, now) {
				it.Why = "unused for " + strings.TrimPrefix(it.Why, "older than ")
				it.lock = Lock(stageLockPrefix + d.Name() + ".lock")
				out = append(out, it)
			}
		}
	}

	// What interrupted work left, each a directory under the machine lock.
	left := []struct{ kind, path string }{
		{"golden pull parts", filepath.Join(GoldenPullDir(), ".parts")},
		{"media scratch", ISOWorkDir()},
	}
	if es, err := os.ReadDir(stagingDir()); err == nil {
		for _, e := range es {
			left = append(left, struct{ kind, path string }{"staged bundles", filepath.Join(stagingDir(), e.Name())})
		}
	}
	for _, l := range left {
		fi, err := os.Stat(l.path)
		if err != nil || p.LeftAge <= 0 || now.Sub(fi.ModTime()) <= p.LeftAge {
			continue
		}
		out = append(out, PruneItem{Kind: l.kind, Path: l.path, Bytes: treeBytes(l.path),
			Why: "left by interrupted work, untouched for " + humanAge(p.LeftAge), lock: MachineLock})
	}
	return out
}

// Prune removes what PrunePlan selects when force is set, each item under
// its lock taken without waiting (a busy lock means its owner is using it,
// and the item is kept), and checks each is gone. It returns every item, and
// the bytes removing them freed.
func Prune(p PrunePolicy, force bool, now time.Time) ([]PruneItem, int64) {
	items := PrunePlan(p, now)
	if !force {
		return items, 0
	}
	var freed int64
	for i := range items {
		it := &items[i]
		if it.lock != "" {
			release, err := Acquire(it.lock)
			if err != nil {
				it.Err = "kept: " + err.Error()
				continue
			}
			err = removeChecked(it.Path)
			release()
			if err != nil {
				it.Err = err.Error()
				continue
			}
		} else if err := removeChecked(it.Path); err != nil {
			it.Err = err.Error()
			continue
		}
		it.Done = true
		freed += it.Bytes
	}
	return items, freed
}

// removeChecked removes path and reports success only if it is gone. It
// refuses anything that is not strictly inside the tool's own root, however
// the path was built: the root itself, a path that climbs out with "..", and
// one whose parent resolves through a symlink to somewhere else. Prune deletes
// in a shared home directory; a plan bug must not be able to reach past it.
func removeChecked(path string) error {
	if err := insideRoot(appRoot(), path); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is still there after removing it", path)
	}
	return nil
}

// listFiles is every regular file in dir (and below it with deep), with its
// modification time and private bytes.
func listFiles(dir string, deep bool) []pruneFile {
	var out []pruneFile
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a directory that is not there has nothing to prune
		}
		if d.IsDir() {
			if p != dir && !deep {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil //nolint:nilerr // gone since the listing: nothing to prune
		}
		out = append(out, pruneFile{path: p, mod: fi.ModTime(), bytes: privateBytes(p)})
		return nil
	})
	return out
}

// treeBytes is the private bytes of everything under path.
func treeBytes(path string) int64 {
	var n int64
	for _, f := range listFiles(path, true) {
		n += f.bytes
	}
	return n
}

// privateBytes is what removing path frees: its APFS private size, or off
// APFS its allocated size.
func privateBytes(path string) int64 {
	if _, priv, err := apfsUsage(path); err == nil {
		return priv
	}
	n, _ := diskUsage(path)
	return n
}

// insideRoot reports an error unless path is strictly below root, both
// resolved through symlinks (path's parent, so a symlink at path itself is
// removed as a link, never followed).
func insideRoot(root, path string) error {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("prune: cannot resolve the tool's root %s: %w", root, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return fmt.Errorf("prune: refusing %s: cannot resolve its folder: %w", path, err)
	}
	full := filepath.Join(parent, filepath.Base(filepath.Clean(path)))
	rel, err := filepath.Rel(r, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("prune: refusing %s: it is not inside %s", path, root)
	}
	return nil
}
