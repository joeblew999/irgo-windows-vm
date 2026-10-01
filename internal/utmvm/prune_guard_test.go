package utmvm

import (
	"os"
	"path/filepath"
	"testing"
)

// Prune must never delete outside its root, whatever path a plan produces.
// Negative control, run by hand: make insideRoot return nil and the outside
// file, the root itself and the symlinked folder's file are all deleted.
//
// Every path here lives under t.TempDir(). Never add a real path ("/", the
// home directory): the negative control disables the guard, and a real path
// would then really be removed. One "/" case here ran os.RemoveAll("/") for
// ten minutes on 1 Oct 2026 before the test timeout stopped it.
func TestRemoveCheckedStaysInsideRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := appRoot()
	if err := os.MkdirAll(filepath.Join(root, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "precious.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	victim := filepath.Join(elsewhere, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "shots", "old.png")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		outside,
		root,
		filepath.Join(root, "..", "precious.txt"),
		filepath.Join(root, "link", "victim.txt"),
	} {
		if err := removeChecked(p); err == nil {
			t.Errorf("removeChecked(%s) succeeded; it is not inside the root", p)
		}
	}
	for _, keep := range []string{outside, victim, root} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was deleted: %v", keep, err)
		}
	}
	if err := removeChecked(inside); err != nil {
		t.Errorf("a file inside the root was refused: %v", err)
	}
	if err := removeChecked(filepath.Join(root, "link")); err != nil {
		t.Errorf("the symlink itself (inside the root) was refused: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("removing the symlink followed it: %v", err)
	}
}
