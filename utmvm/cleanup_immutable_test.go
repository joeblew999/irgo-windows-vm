//go:build darwin

package utmvm

import (
	"os"
	"path/filepath"
	"testing"
)

// The bug this covers, measured before it was fixed:
//
//	$ ln orig bundle/inside && isoChflags uchg orig
//	$ rm -rf bundle
//	rm: bundle/inside: Operation not permitted
//	rm: bundle: Directory not empty
//
// uchg is a per-INODE flag, and Create hardlinks the protected ISO into the
// bundle, so the flag applies inside the bundle too and unlink returns EPERM.
// Every VM built from a protected ISO was therefore undeletable.
//
// The second half matters as much: clearing the flag unprotects the ORIGINAL,
// because it is the same inode. Releasing it to delete a bundle must not leave
// the user's 5 GB download unprotected afterwards.
func TestReleaseImmutableUnblocksRemovalAndReprotects(t *testing.T) {
	dir := t.TempDir()

	orig := filepath.Join(dir, "win11.iso")
	if err := os.WriteFile(orig, []byte("pretend media"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "vm.utm", "Data")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bundle, "install.iso")
	if err := os.Link(orig, link); err != nil {
		t.Fatal(err)
	}
	if err := ISOProtect(orig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ISOUnprotect(orig) })

	// Negative control: without the fix, removal fails. If this ever stops
	// failing, the rest of the test proves nothing and must be revisited.
	if err := os.RemoveAll(filepath.Dir(bundle)); err == nil {
		t.Fatal("expected EPERM removing a bundle holding an immutable hardlink; " +
			"the premise of releaseImmutable no longer holds")
	}

	_, immutable := walkBundle(filepath.Dir(bundle))
	survivors := releaseImmutable(immutable, []string{dir})

	if err := os.RemoveAll(filepath.Dir(bundle)); err != nil {
		t.Fatalf("after releaseImmutable the bundle should delete: %v", err)
	}

	// The original must be findable as a survivor and still protected after.
	found := false
	for _, s := range survivors {
		if s == orig {
			found = true
		}
	}
	if !found {
		t.Errorf("original %s not reported as a surviving name; it would be left unprotected", orig)
	}
	for _, s := range survivors {
		_ = ISOProtect(s) // what Delete's deferred re-protect does
	}
	st, err := ISOLinks(orig, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Protected {
		t.Error("the original ISO was left unprotected after deleting a VM that shared its inode")
	}
}
