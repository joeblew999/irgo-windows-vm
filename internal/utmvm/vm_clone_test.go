//go:build darwin

package utmvm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCloneOrCopyDropsTheImmutableFlag: the media is immutable, a clone copies
// BSD flags, and UTM must later be able to delete the bundle's copy when the
// medium is ejected. So the copy must be a different inode with the same bytes
// and no uchg.
//
// Negative control, run by hand: return nil straight after cloneFile succeeds
// and the flag check fails.
func TestCloneOrCopyDropsTheImmutableFlag(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "media.iso"), filepath.Join(dir, "install.iso")
	want := bytes.Repeat([]byte("windows"), 4096)
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := isoProtect(src); err != nil {
		t.Fatalf("protecting the test media: %v", err)
	}
	t.Cleanup(func() { _ = ISOUnprotect(src); _ = ISOUnprotect(dst) })

	if err := cloneOrCopy(src, dst); err != nil {
		t.Fatalf("cloneOrCopy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("the copy's bytes differ (err %v)", err)
	}
	if flags, ok := fileFlags(dst); !ok || flags&uchgFlag != 0 {
		t.Errorf("the copy is immutable (flags %#x); UTM could not delete it on eject", flags)
	}
	if flags, ok := fileFlags(src); !ok || flags&uchgFlag == 0 {
		t.Error("the media lost its protection")
	}
	si, _, _ := inodeInfo(src)
	di, _, _ := inodeInfo(dst)
	if si == di {
		t.Error("the copy is a hardlink; its flag is the media's flag")
	}
}
