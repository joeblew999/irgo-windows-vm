package utmvm

// Downloading install media, with the two properties the GUI route does not
// give you: it says what the bytes should hash to, and it cannot destroy the
// media you already have.
//
// The second is not hypothetical. The working ISO is hardlinked into UTM's
// bundle, so the obvious implementation — open the destination, write to it —
// truncates the file the VM boots from. Everything here therefore writes to a
// staging path and only ever links or renames into place once the hash matches,
// and refuses outright if the destination is in use.

import (
	"crypto/sha1" //nolint:gosec // the catalog publishes SHA-1; the choice is Microsoft's
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Download fetches url to dest, resuming a partial file and verifying sha1.
//
// dest must not exist. The staging file is dest+".part", which is resumable
// across runs: a 4 GB download that dies at 90% costs the last 10%, not the
// whole thing, and the servers support ranged requests.
//
// progress, if non-nil, is called about once a second with bytes so far and the
// total. A 4 GB download with no output looks identical to a hung one.
func Download(url, dest, wantSHA1 string, progress func(done, total int64)) error {
	if err := refuseUnsafeDest(dest); err != nil {
		return err
	}
	part := dest + ".part"

	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}

	// No client timeout: this is measured in gigabytes, and a deadline that
	// makes sense for an API call fails a download at exactly the point the
	// work is nearly done. The transport's per-read timeouts still catch a
	// stalled connection.
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("utmvm: downloading: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// The server ignored the range, so anything already downloaded is not a
		// prefix of what is arriving now. Start again rather than concatenate.
		have = 0
	case http.StatusPartialContent:
	default:
		return fmt.Errorf("utmvm: downloading: server returned %s", resp.Status)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if have > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644) //nolint:gosec // path is the caller's chosen destination
	if err != nil {
		return err
	}

	total := have + resp.ContentLength
	done := have
	last := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				f.Close()
				return wErr
			}
			done += int64(n)
			if progress != nil && time.Since(last) > time.Second {
				progress(done, total)
				last = time.Now()
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			f.Close()
			return fmt.Errorf("utmvm: downloading: %w", rErr)
		}
	}
	// Flushed before it counts as written. Without this a crash can leave the
	// final name at full length with an unflushed tail — the wrong bytes, under
	// a name that says the download finished.
	//
	// Note what is NOT here: a done == ContentLength check. net/http already
	// fails a body shorter than a declared Content-Length, so such a check is
	// unreachable — verified by disabling it and watching the test still pass.
	// The genuine gap is a chunked response (ContentLength -1) truncated
	// mid-stream, which is indistinguishable from a clean end at this layer and
	// is caught only by the SHA-1, when the caller supplies one.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if progress != nil {
		progress(done, total)
	}

	if wantSHA1 != "" {
		got, hErr := FileSHA1(part)
		if hErr != nil {
			return hErr
		}
		if !strings.EqualFold(got, wantSHA1) {
			// Kept, not deleted: 4 GB is expensive to re-fetch, and a mismatch
			// is more often a truncated resume than a corrupt server.
			return fmt.Errorf("utmvm: sha1 mismatch\n  want %s\n  got  %s\n  kept %s — delete it to start over",
				wantSHA1, got, part)
		}
	}
	return os.Rename(part, dest)
}

// FileSHA1 hashes a file, which for a 4 GB ESD takes a few seconds.
func FileSHA1(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied path
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New() //nolint:gosec // matching the catalog's published digest
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// refuseUnsafeDest stops a download from destroying media already in use.
//
// Three separate refusals, because each is a different mistake: writing over a
// file that exists, writing over a file some VM is booting from, and writing
// over one that was deliberately made immutable.
func refuseUnsafeDest(dest string) error {
	fi, err := os.Stat(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return os.MkdirAll(filepath.Dir(dest), 0o755)
	}

	msg := fmt.Sprintf("utmvm: %s already exists (%s)", dest, HumanBytes(fi.Size()))

	if _, nlink, ok := inodeInfo(dest); ok && nlink > 1 {
		st, sErr := ISOLinks(dest, ISOSearchDirs())
		if sErr == nil {
			// Not len(Found)-1: dest may not be among Found at all, because the
			// search covers ~/Downloads and UTM's bundles, and dest is usually
			// .cache — which is neither. Count what is actually about to be
			// listed rather than assuming dest is in the list.
			var others []string
			for _, p := range st.Found {
				if abs, aErr := filepath.Abs(dest); aErr != nil || p != abs {
					others = append(others, p)
				}
			}
			if len(others) > 0 {
				msg += fmt.Sprintf("\n  and it is the SAME FILE as %d other name(s), including media a VM boots from:", len(others))
				for _, p := range others {
					msg += "\n    " + Home(p)
				}
				msg += "\n  Writing here would empty all of them."
			}
		}
	}
	return fmt.Errorf("%s\n  Move it aside, or choose another -o path. This will not overwrite it.", msg)
}
