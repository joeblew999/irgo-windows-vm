package utmvm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Answering "is this ARM64 media" used to read all 5.27 GB of the ISO on every
// single command — 77 seconds, measured, before anything else could happen.
// The verdict is cached beside the file and keyed by size and mtime.
//
// The risk in caching it is worse than the slowness it fixes: a stale yes would
// hand Windows Setup an x86-64 ISO, which boots to a black screen on Apple
// Silicon with no diagnostic at all. So the invalidation is what these tests
// are really about.

func writeISO(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "win11-arm64.iso")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerdictRoundTrips(t *testing.T) {
	p := writeISO(t, "anything")
	if _, ok := cachedVerdict(p); ok {
		t.Fatal("a verdict was returned for an ISO that has never been scanned")
	}
	storeVerdict(p, ISOInfo{IsARM64: true})
	got, ok := cachedVerdict(p)
	if !ok {
		t.Fatal("the verdict just stored was not read back")
	}
	if !got.IsARM64 {
		t.Error("IsARM64 did not survive the round trip")
	}
}

// Size changing means a different file, whatever the name says.
func TestVerdictInvalidatedBySize(t *testing.T) {
	p := writeISO(t, "small")
	storeVerdict(p, ISOInfo{IsARM64: true})

	// mtime is restored afterwards, so ONLY the size differs. Without this the
	// mtime check catches it and the size check is never exercised — which is
	// exactly what a mutation run showed.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("much longer contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedVerdict(p); ok {
		t.Error("the cached verdict survived the file changing size — a rebuilt or " +
			"re-downloaded ISO would be trusted on the old answer")
	}
}

// Same size, different contents: only mtime distinguishes them. This is the
// case that catches an ISO rebuilt in place.
func TestVerdictInvalidatedByMtime(t *testing.T) {
	p := writeISO(t, "aaaa")
	storeVerdict(p, ISOInfo{IsARM64: true})
	if err := os.WriteFile(p, []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedVerdict(p); ok {
		t.Error("the cached verdict survived a same-size rewrite; mtime is the only " +
			"thing separating these two files and it was not checked")
	}
}

// A corrupt or truncated sidecar must read as "no verdict", never as a yes.
func TestVerdictRejectsGarbage(t *testing.T) {
	p := writeISO(t, "iso")
	for _, junk := range []string{"", "not numbers", "1 2", "1 2 3 4"} {
		if err := os.WriteFile(p+".scan", []byte(junk), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := cachedVerdict(p); ok {
			t.Errorf("garbage sidecar %q was accepted as a verdict", junk)
		}
	}
}

// The point of the cache: the second look does not re-read the file. Proven by
// making the file unreadable — a real scan would fail, a cache hit will not.
func TestInspectISOUsesTheCache(t *testing.T) {
	p := writeISO(t, "not really an iso")
	storeVerdict(p, ISOInfo{IsARM64: true})

	if err := os.Chmod(p, 0o000); err != nil {
		t.Skip("cannot make the file unreadable here")
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	info, err := InspectISO(p)
	if err != nil {
		t.Fatalf("InspectISO re-read the file instead of using the cached verdict: %v", err)
	}
	if !info.IsARM64 {
		t.Error("the cached verdict was not returned")
	}
}
