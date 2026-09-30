package utmvm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The SHA-256 option: the right digest is renamed into place, a wrong one is
// refused and left as .part, and the error says which algorithm disagreed.
//
// Negative control, run by hand: make isoDownload skip the compare and the
// mismatch case renames the file into place.
func TestDownloadVerifiesSHA256(t *testing.T) {
	body := []byte("the golden image's chunk, stood in for by a sentence")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := isoDownload(srv.URL, good, sha256Digest(hex.EncodeToString(sum[:])), nil); err != nil {
		t.Fatalf("the right SHA-256 was refused: %v", err)
	}

	bad := filepath.Join(dir, "bad")
	err := isoDownload(srv.URL, bad, sha256Digest(strings.Repeat("0", 64)), nil)
	if !errors.Is(err, errDigestMismatch) || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("a wrong SHA-256 gave %v, want a sha256 mismatch", err)
	}
	if _, sErr := os.Stat(bad); sErr == nil {
		t.Error("a file with the wrong digest was renamed into place")
	}
	if _, sErr := os.Stat(bad + ".part"); sErr != nil {
		t.Errorf("the mismatched download was not kept as .part: %v", sErr)
	}
}

// A .part left by an interrupted run is resumed with a Range request, and the
// digest is over the whole file, not the tail.
//
// Negative control, run by hand: drop the Range header in isoDownload and the
// range check below fails.
func TestDownloadResumesAPart(t *testing.T) {
	body := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	sum := sha256.Sum256(body)
	var ranged string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranged = r.Header.Get("Range")
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "chunk")
	if err := os.WriteFile(dest+".part", body[:10], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := isoDownload(srv.URL, dest, sha256Digest(hex.EncodeToString(sum[:])), nil); err != nil {
		t.Fatalf("resuming failed: %v", err)
	}
	if ranged != "bytes=10-" {
		t.Errorf("the request asked for range %q, want bytes=10-", ranged)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("resumed file is %q (%v), want %q", got, err, body)
	}
}

// A truncated body must not be renamed into place as finished media.
//
// This passes because net/http rejects a body shorter than its declared
// Content-Length, NOT because of anything in isoDownload — verified by disabling
// isoDownload's own check and watching this still pass. It is kept as a guard on
// that behaviour, not as evidence isoDownload validates length.
func TestDownloadRejectsShortBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000") // claims 1000
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 400)) // delivers 400
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "media.iso")
	err := isoDownload(srv.URL, dest, digest{}, nil)
	if err == nil {
		t.Fatal("a 400-byte body against a declared 1000 must fail, not report success")
	}
	if _, sErr := os.Stat(dest); sErr == nil {
		t.Error("the truncated file was renamed into place; callers will treat it as complete media")
	}
}

// The complete case must still pass, or the check above is just breaking
// downloads rather than validating them.
func TestDownloadAcceptsCompleteBody(t *testing.T) {
	body := make([]byte, 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "media.iso")
	if err := isoDownload(srv.URL, dest, digest{}, nil); err != nil {
		t.Fatalf("a complete body must succeed: %v", err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("complete download left no file: %v", err)
	}
	if fi.Size() != int64(len(body)) {
		t.Errorf("got %d bytes, want %d", fi.Size(), len(body))
	}
}
