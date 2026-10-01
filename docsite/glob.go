package docsite

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// globFiles is every file under root matching one of the patterns, as
// slash-separated paths from root, sorted. A pattern is path.Match syntax
// plus ** for any number of directories and {a,b} for alternatives. Each walk
// starts at the pattern's literal prefix, so cmd/**/*.go never reads
// node_modules.
func globFiles(root string, patterns []string) ([]string, error) {
	var expanded []string
	for _, p := range patterns {
		expanded = append(expanded, expandBraces(p)...)
	}
	found := map[string]bool{}
	for _, pat := range expanded {
		pat = strings.TrimPrefix(path.Clean(pat), "./")
		if !strings.ContainsAny(pat, "*?[") {
			if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(pat))); err == nil && !fi.IsDir() {
				found[pat] = true
			}
			continue
		}
		start := literalPrefix(pat)
		dir := filepath.Join(root, filepath.FromSlash(start))
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == dir && errors.Is(err, fs.ErrNotExist) {
					return filepath.SkipAll // a pattern for a directory this project lacks
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if matchGlob(pat, rel) {
				found[rel] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(found))
	for f := range found {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// literalPrefix is the directory part of a pattern before its first
// wildcard: "cmd/**/*.go" walks from "cmd", "README.md" from ".".
func literalPrefix(pat string) string {
	parts := strings.Split(pat, "/")
	var lit []string
	for _, p := range parts[:len(parts)-1] {
		if strings.ContainsAny(p, "*?[") {
			break
		}
		lit = append(lit, p)
	}
	if len(lit) == 0 {
		return "."
	}
	return strings.Join(lit, "/")
}

// matchGlob matches a slash path against a pattern with ** segments.
func matchGlob(pat, name string) bool {
	return matchParts(strings.Split(pat, "/"), strings.Split(name, "/"))
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchParts(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// expandBraces turns "a/*.{go,md}" into "a/*.go" and "a/*.md". No nesting.
func expandBraces(p string) []string {
	i := strings.Index(p, "{")
	j := strings.Index(p, "}")
	if i < 0 || j < i {
		return []string{p}
	}
	var out []string
	for _, alt := range strings.Split(p[i+1:j], ",") {
		out = append(out, expandBraces(p[:i]+alt+p[j+1:])...)
	}
	return out
}
