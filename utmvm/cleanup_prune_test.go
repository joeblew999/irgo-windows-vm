package utmvm

import (
	"os"
	"path/filepath"
	"testing"
)

// Prune runs against the system temp directory, which on a developer's machine
// is full of other people's work. It used to delete any *.img or *.dmg it
// found, which is somebody else's half-built VM or downloaded installer.
//
// This test is the guard on that: everything this package creates must be
// removed, and everything else must survive, including files whose names look
// exactly like the ones that used to be swept up.
func TestPruneOnlyRemovesOurOwn(t *testing.T) {
	dir := t.TempDir()

	ours := []string{
		"irgo-winvm-payload-123",
		"irgo-catalog-abc",
		"irgo-script-i-xyz.bat",
		"irgo-script-xyz.bat",
		"irgo-utm-9876",
		"utmvm-windowid-42.swift",
	}
	// Names that used to be deleted and must not be. A disk image in /tmp is
	// somebody's work in progress; a .dmg is very often a download.
	theirs := []string{
		"disk.img",
		"ubuntu-24.04.img",
		"SomeApp-1.2.3.dmg",
		"Xcode_16.dmg",
		"scratch.img",
		"notes.txt",
	}

	for _, n := range append(append([]string{}, ours...), theirs...) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, removed, err := Prune(dir); err != nil {
		t.Fatal(err)
	} else if len(removed) != len(ours) {
		t.Errorf("removed %d files, want %d: %v", len(removed), len(ours), removed)
	}

	for _, n := range ours {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s is ours and should have been pruned", n)
		}
	}
	for _, n := range theirs {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s is NOT ours and must survive, but: %v", n, err)
		}
	}
}

func TestIsOurArtefact(t *testing.T) {
	for name, want := range map[string]bool{
		"irgo-winvm-payload-1":   true,
		"irgo-catalog-x":         true,
		"utmvm-windowid-1.swift": true,
		"disk.img":               false,
		"anything.dmg":           false,
		"":                       false,
		// Close but not ours: a prefix match must be at the start.
		"my-irgo-thing": false,
	} {
		if got := isOurArtefact(name); got != want {
			t.Errorf("isOurArtefact(%q) = %v, want %v", name, got, want)
		}
	}
}
