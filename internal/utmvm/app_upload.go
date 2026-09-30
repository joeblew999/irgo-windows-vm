package utmvm

// Staging a binary for app-create, from bytes rather than a host file path.
//
// app-create takes a path that exists on this machine; a remote agent has a
// binary it just cross-compiled and no shared filesystem. Upload is the bridge:
// chunks arrive over MCP, land under bin/, and the path app-create already
// accepts comes out the other end.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// uploadExt is the extension a staged binary carries, so the guest can run it.
// Content-addressed by digest plus a fixed extension: the digest alone would
// stage an extensionless file, and Windows needs the extension to run it.
const uploadExt = ".exe"

// Upload stages one chunk of a binary and, once the whole binary has arrived
// and its digest has been verified, commits it as bin/<sha256>.exe.
//
// Content-addressed: the path is the digest, so an unchanged binary transfers
// nothing — a second upload of the same bytes finds the committed file and
// returns it. Chunks land in bin/<sha256>.exe.part and are renamed into place
// only after the full SHA-256 matches; a truncated or corrupted upload is
// removed and refused rather than committed, because a truncated upload that
// runs anyway is this repository's oldest category of bug.
//
// It returns the committed path once complete (with n == total), or "" with the
// number of bytes staged so far when more chunks are needed.
func Upload(hash string, total, offset int64, data []byte) (staged string, n int64, err error) {
	want, err := hex.DecodeString(hash)
	if err != nil || len(want) != sha256.Size {
		return "", 0, fmt.Errorf("upload: hash must be 64 hex digits, got %q", hash)
	}
	if total <= 0 {
		return "", 0, fmt.Errorf("upload: total must be positive, got %d", total)
	}
	if offset < 0 || offset+int64(len(data)) > total {
		return "", 0, fmt.Errorf("upload: chunk at offset %d with %d bytes exceeds total %d", offset, len(data), total)
	}

	key := hex.EncodeToString(want)
	staged = filepath.Join(VMStageDir(), key+uploadExt)
	if _, sErr := os.Stat(staged); sErr == nil {
		// Already committed and verified; an unchanged binary transfers nothing.
		return staged, total, nil
	}

	if err := os.MkdirAll(VMStageDir(), 0o755); err != nil {
		return "", 0, fmt.Errorf("upload: creating %s: %w", VMStageDir(), err)
	}
	part := staged + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("upload: opening %s: %w", part, err)
	}
	defer func() { _ = f.Close() }()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, fmt.Errorf("upload: measuring %s: %w", part, err)
	}

	switch {
	case offset < size:
		// A retry of a chunk already written is idempotent only if the bytes
		// agree; anything else is two writers disagreeing about one file.
		existing := make([]byte, len(data))
		if _, err := f.ReadAt(existing, offset); err != nil {
			return "", 0, fmt.Errorf("upload: reading %s at %d: %w", part, offset, err)
		}
		if !bytes.Equal(existing, data) {
			return "", 0, fmt.Errorf("upload: chunk at offset %d disagrees with what is already staged", offset)
		}
	case offset == size:
		if _, err := f.WriteAt(data, offset); err != nil {
			return "", 0, fmt.Errorf("upload: writing %s at %d: %w", part, offset, err)
		}
		size += int64(len(data))
	default:
		return "", 0, fmt.Errorf("upload: chunk at offset %d, but only %d bytes are staged; chunks must arrive in order", offset, size)
	}

	if size < total {
		return "", size, nil
	}
	if size > total {
		_ = os.Remove(part)
		return "", 0, fmt.Errorf("upload: %s grew past the declared total %d", part, total)
	}

	// Complete. The hash is verified before the committed file exists.
	if err := f.Sync(); err != nil {
		return "", 0, fmt.Errorf("upload: syncing %s: %w", part, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, fmt.Errorf("upload: seeking %s: %w", part, err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, fmt.Errorf("upload: hashing %s: %w", part, err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		_ = os.Remove(part)
		return "", 0, fmt.Errorf("upload: hash mismatch; %s was rejected and not committed", part)
	}
	if err := os.Rename(part, staged); err != nil {
		return "", 0, fmt.Errorf("upload: committing %s: %w", staged, err)
	}
	return staged, total, nil
}

// ClearStage removes every staged binary under bin/, so app-delete undoes
// app-upload. Deleting nothing is success: an empty or missing directory is the
// undo having already happened, so it can be run twice.
func ClearStage() error {
	dir := VMStageDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clearing staged binaries: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clearing staged binaries: removing %s: %w", e.Name(), err)
		}
	}
	return nil
}
