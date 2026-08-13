//go:build darwin

package utmvm

import (
	"os"
	"path/filepath"
	"testing"
)

// Protection exists because the ISO is 5.27 GB and rate-limited at the source,
// so losing it costs a re-download that Microsoft may refuse. These cover the
// two ways deleting it can go wrong: silently succeeding when it should refuse,
// and leaving the flag off afterwards.

func TestProtectAndUnprotectRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "media.iso")
	if err := os.WriteFile(p, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = UnprotectISO(p) })

	st, err := ISOLinks(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Protected {
		t.Fatal("a freshly written file reported as protected")
	}
	if err := ProtectISO(p); err != nil {
		t.Fatal(err)
	}
	if st, _ = ISOLinks(p, nil); !st.Protected {
		t.Fatal("ProtectISO did not set the flag, or ISOLinks cannot see it")
	}
	if err := UnprotectISO(p); err != nil {
		t.Fatal(err)
	}
	if st, _ = ISOLinks(p, nil); st.Protected {
		t.Error("UnprotectISO did not clear the flag")
	}
}

// An immutable file cannot be removed. This is the property iso-delete relies
// on to refuse, and the one that made VMs undeletable when nothing cleared it.
func TestProtectedISORefusesRemoval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "media.iso")
	if err := os.WriteFile(p, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProtectISO(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = UnprotectISO(p) })

	if err := os.Remove(p); err == nil {
		t.Fatal("a protected ISO was removed; protection is doing nothing")
	}
	if err := UnprotectISO(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Errorf("after clearing the flag the ISO still could not be removed: %v", err)
	}
}

// ISOLinks reports every other name for the same bytes, which is what tells a
// caller that deleting this one frees nothing.
func TestISOLinksCountsSharedNames(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "media.iso")
	b := filepath.Join(dir, "other-name.iso")
	if err := os.WriteFile(a, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	st, err := ISOLinks(a, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if st.Links != 2 {
		t.Errorf("Links = %d, want 2 — a caller would think removing this frees the space", st.Links)
	}
	var foundOther bool
	for _, f := range st.Found {
		if f == b {
			foundOther = true
		}
	}
	if !foundOther {
		t.Errorf("the sibling name was not located; Found = %v", st.Found)
	}
}
