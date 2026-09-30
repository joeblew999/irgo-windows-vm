package main

// The repository must not contain compiled binaries. A bare `go build` drops
// one in the source tree, and `git add -A` commits it; two were once tracked
// at once (irgo-winvm at the root and site/site).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// executableMagic is the first few bytes of the formats a Go build can produce
// on the platforms this repository targets, read directly so the test needs no
// file(1).
var executableMagic = []struct {
	name  string
	bytes []byte
}{
	{"Mach-O 64-bit", []byte{0xCF, 0xFA, 0xED, 0xFE}},
	{"Mach-O 32-bit", []byte{0xCE, 0xFA, 0xED, 0xFE}},
	{"Mach-O universal", []byte{0xCA, 0xFE, 0xBA, 0xBE}},
	{"ELF", []byte{0x7F, 'E', 'L', 'F'}},
	{"PE (Windows .exe)", []byte{'M', 'Z'}},
}

// TestNoTrackedBinaries asserts that nothing git tracks is a compiled
// executable. It asks git rather than walking the tree, which is full of
// untracked build output (.bin/, dist/, site/dist/).
//
// Negative control: `go build ./cmd/irgo-winvm && git add -f irgo-winvm` fails
// this and names the file. `git reset` restores.
func TestNoTrackedBinaries(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing tracked files: %v", err)
	}

	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) < 2 {
		t.Fatalf("git listed %d tracked files; this test would pass vacuously", len(files))
	}

	checked := 0
	for _, rel := range files {
		if rel == "" {
			continue
		}
		f, oErr := os.Open(filepath.Join(root, rel))
		if oErr != nil {
			// A tracked file that is not on disk is somebody else's problem —
			// a partial checkout, a submodule. Not this test's business.
			continue
		}
		var head [4]byte
		n, _ := f.Read(head[:])
		_ = f.Close()
		checked++

		for _, m := range executableMagic {
			if n >= len(m.bytes) && bytes.Equal(head[:len(m.bytes)], m.bytes) {
				t.Errorf("%s is a tracked %s executable — build output does not belong in the repository; add it to .gitignore",
					rel, m.name)
				break
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tracked files could be read; this test would pass vacuously")
	}
	t.Logf("%d tracked files checked, none is a compiled binary", checked)
}
