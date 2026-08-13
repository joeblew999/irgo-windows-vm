package utmvm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
	err := isoDownload(srv.URL, dest, "", nil)
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
	if err := isoDownload(srv.URL, dest, "", nil); err != nil {
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
