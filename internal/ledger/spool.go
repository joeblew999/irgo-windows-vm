package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// The spool is spool.jsonl, one event per line, appended by every process of
// the tool on this machine. A flush renames it to inflight.jsonl and sends
// that. spool.lock is held for each append and for the rename, so no event
// is written into a file a flush has already read: without it, a process that
// opened the spool just before the rename would append to inflight.jsonl
// after it was sent, and the event would be deleted with it.

func (c *Client) spoolPath() string { return filepath.Join(c.cfg.Dir, "spool.jsonl") }

func (c *Client) appendSpool(line []byte) error {
	if err := os.MkdirAll(c.cfg.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(c.cfg.Dir, "spool.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if st, err := os.Stat(c.spoolPath()); err == nil && st.Size()+int64(len(line)) > maxSpool {
		return fmt.Errorf("ledger: the spool is over %d bytes; dropping the event", maxSpool)
	}
	f, err := os.OpenFile(c.spoolPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// takeSpool moves the spool to dst, reporting false when there is none.
func (c *Client) takeSpool(dst string) (bool, error) {
	unlock, err := lock(filepath.Join(c.cfg.Dir, "spool.lock"))
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := os.Rename(c.spoolPath(), dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
