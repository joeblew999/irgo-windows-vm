package utmvm

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// zipOne must produce a zip that holds exactly the source bytes, under the
// guest's file name — tar in the guest expands whatever name is in the zip, so
// a wrong name puts the binary somewhere app-create will not look.
//
// Negative control, run by hand: writing name+"x" as the entry name in zipOne
// fails the name check; copying from a bytes.Reader of half the file fails the
// content check.
func TestZipOneRoundTrip(t *testing.T) {
	want := bytes.Repeat([]byte("MZ irgo push test "), 50_000) // ~900 KB, over pushZipMin
	src := filepath.Join(t.TempDir(), "local-name.exe")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	z, err := zipOne(src, "guest-name.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(z) }()

	r, err := zip.OpenReader(z)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if len(r.File) != 1 || r.File[0].Name != "guest-name.exe" {
		t.Fatalf("zip entries = %v, want exactly guest-name.exe", r.File)
	}
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("zip holds %d bytes, want the %d written", len(got), len(want))
	}
	if int64(len(want)) < pushZipMin {
		t.Fatalf("test file is under pushZipMin, so Push would not zip it")
	}
}
