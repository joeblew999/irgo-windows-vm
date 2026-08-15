package utmvm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func testHash(t *testing.T, b []byte) string {
	t.Helper()
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestUploadStagesChunksAndCommits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blob := []byte("hello windows arm64, from a remote agent")
	hash := testHash(t, blob)
	const half = 10

	staged, n, err := Upload(hash, int64(len(blob)), 0, blob[:half])
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if staged != "" {
		t.Fatalf("first chunk committed early: %q", staged)
	}
	if n != half {
		t.Fatalf("staged %d bytes, want %d", n, half)
	}

	staged, n, err = Upload(hash, int64(len(blob)), half, blob[half:])
	if err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if staged == "" {
		t.Fatal("second chunk did not commit")
	}
	if n != int64(len(blob)) {
		t.Fatalf("staged %d bytes, want %d", n, len(blob))
	}

	got, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(blob) {
		t.Fatalf("committed bytes = %q, want %q", got, blob)
	}
}

// TestUploadUnchangedBinaryTransfersNothing is the property the inner loop
// depends on: a 10.8-second push must not be dominated by re-sending bytes that
// are already staged.
func TestUploadUnchangedBinaryTransfersNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blob := []byte("same bytes")
	hash := testHash(t, blob)

	staged, _, err := Upload(hash, int64(len(blob)), 0, blob)
	if err != nil {
		t.Fatal(err)
	}
	if staged == "" {
		t.Fatal("upload did not commit")
	}

	// A second upload of the same hash returns the path without needing chunks.
	again, n, err := Upload(hash, int64(len(blob)), 0, nil)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if again != staged {
		t.Fatalf("second upload returned %q, want %q", again, staged)
	}
	if n != int64(len(blob)) {
		t.Fatalf("second upload reported %d bytes, want %d", n, len(blob))
	}
}

// TestUploadRefusesAHashMismatch is the negative control for the whole stage:
// the hash is checked before the committed file exists, and a mismatch leaves
// nothing behind. Break the comparison and this fails.
func TestUploadRefusesAHashMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blob := []byte("real bytes")
	wrong := testHash(t, []byte("different bytes"))

	staged, _, err := Upload(wrong, int64(len(blob)), 0, blob)
	if err == nil {
		t.Fatal("a mismatched upload committed; want an error")
	}
	if staged != "" {
		t.Fatalf("mismatched upload returned a path: %q", staged)
	}
	if _, err := os.Stat(filepath.Join(VMStageDir(), wrong+".exe")); !os.IsNotExist(err) {
		t.Errorf("the rejected upload was committed anyway: %v", err)
	}
	if _, err := os.Stat(filepath.Join(VMStageDir(), wrong+".exe.part")); !os.IsNotExist(err) {
		t.Errorf("the rejected part file was left behind: %v", err)
	}
}

func TestUploadTruncatedNeverCommits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blob := []byte("not the whole thing")
	hash := testHash(t, blob)

	staged, n, err := Upload(hash, int64(len(blob)), 0, blob[:5])
	if err != nil {
		t.Fatal(err)
	}
	if staged != "" {
		t.Fatalf("truncated upload committed: %q", staged)
	}
	if n != 5 {
		t.Fatalf("staged %d bytes, want 5", n)
	}
	if _, err := os.Stat(filepath.Join(VMStageDir(), hash+".exe")); !os.IsNotExist(err) {
		t.Error("a truncated upload was committed")
	}
}

func TestUploadRefusesOutOfOrderChunks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blob := []byte("0123456789")
	hash := testHash(t, blob)

	if _, _, err := Upload(hash, int64(len(blob)), 5, blob[:5]); err == nil {
		t.Fatal("a chunk at offset 5 with nothing staged was accepted")
	}
}

func TestUploadRetryOfAChunkIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blob := []byte("0123456789")
	hash := testHash(t, blob)

	if _, _, err := Upload(hash, int64(len(blob)), 0, blob[:5]); err != nil {
		t.Fatal(err)
	}
	// Re-sending the same chunk is accepted and does not grow the file.
	_, n, err := Upload(hash, int64(len(blob)), 0, blob[:5])
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n != 5 {
		t.Fatalf("after retry staged %d bytes, want 5", n)
	}
	// Different bytes at an already-written offset are refused.
	if _, _, err := Upload(hash, int64(len(blob)), 0, []byte("XXXXX")); err == nil {
		t.Fatal("conflicting bytes at an already-written offset were accepted")
	}
}

func TestClearStage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A missing stage is already cleared, and clearing it twice is success.
	if err := ClearStage(); err != nil {
		t.Fatalf("clearing a missing stage: %v", err)
	}

	blob := []byte("staged binary")
	hash := testHash(t, blob)
	staged, _, err := Upload(hash, int64(len(blob)), 0, blob)
	if err != nil {
		t.Fatal(err)
	}

	if err := ClearStage(); err != nil {
		t.Fatalf("clearing the stage: %v", err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged binary survived ClearStage: %v", err)
	}
	if err := ClearStage(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}
