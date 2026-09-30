//go:build darwin || windows

package conformance

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/crgimenes/native/clipboard"
	"github.com/crgimenes/native/mmap"
	"github.com/crgimenes/native/power"
	"github.com/crgimenes/native/singleinstance"
)

// The headless capabilities: no window, no run loop. They are what `-short`
// keeps, and what app:test runs under the guest agent in session 0 — the
// strictest place they can be checked, since there is no desktop there at all.

// TestClipboard writes text and reads it back. The clipboard the user had is
// put back afterwards, when there was text on it to read.
func TestClipboard(t *testing.T) {
	if orig, err := clipboard.ReadText(); err == nil {
		t.Cleanup(func() {
			if err := clipboard.WriteText(orig); err != nil {
				t.Errorf("restoring the clipboard: %v", err)
			}
		})
	}
	// Distinct per run, so a stale value from an earlier run cannot pass.
	canary := fmt.Sprintf("irgo-conformance-%d-%d", os.Getpid(), time.Now().UnixNano())
	require(t, "clipboard", clipboard.WriteText(canary), "clipboard.WriteText")
	got, err := clipboard.ReadText()
	require(t, "clipboard", err, "clipboard.ReadText")
	if got != canary {
		t.Fatalf("clipboard.ReadText = %q, want %q (what WriteText just wrote)", got, canary)
	}
}

// TestPowerPreventSleep takes a sleep assertion and releases it. Whether the
// OS honoured it cannot be seen from inside a process; the call reaching the
// backend and succeeding can.
func TestPowerPreventSleep(t *testing.T) {
	tok, err := power.PreventSleep("irgo conformance test")
	require(t, "power", err, "power.PreventSleep")
	if tok == nil {
		t.Fatal("power.PreventSleep returned no token and no error")
	}
	tok.Release()
}

// TestSingleInstance is the lock and the handoff: a second Acquire is refused
// while the first holds it, Send reaches the holder, and Release frees it.
func TestSingleInstance(t *testing.T) {
	// Per process, so a glaze-all left running, or a concurrent run, cannot
	// hold it.
	id := fmt.Sprintf("irgo-conformance-%d", os.Getpid())
	got := make(chan []string, 1)
	first, err := singleinstance.Acquire(id, singleinstance.Options{
		OnMessage: func(args []string) {
			select {
			case got <- args:
			default:
			}
		},
	})
	require(t, "singleinstance", err, "the first Acquire")
	released := false
	t.Cleanup(func() {
		if !released {
			first.Release()
		}
	})

	t.Run("second_acquire_refused", func(t *testing.T) {
		second, err := singleinstance.Acquire(id, singleinstance.Options{})
		if err == nil {
			second.Release()
			t.Fatal("a second Acquire succeeded while the first still held the lock")
		}
		if !errors.Is(err, singleinstance.ErrAlreadyRunning) {
			t.Fatalf("second Acquire refused, but not with ErrAlreadyRunning: %v", err)
		}
	})

	t.Run("send_reaches_holder", func(t *testing.T) {
		want := []string{"hello", "from a second launch"}
		if err := singleinstance.Send(id, want); err != nil {
			t.Fatalf("Send: %v", err)
		}
		select {
		case args := <-got:
			if !slices.Equal(args, want) {
				t.Fatalf("the holder received %q, want %q", args, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Send returned nil and the holder's OnMessage was never called")
		}
	})

	t.Run("release_frees_it", func(t *testing.T) {
		first.Release()
		released = true
		again, err := singleinstance.Acquire(id, singleinstance.Options{})
		if err != nil {
			t.Fatalf("Acquire after Release: %v", err)
		}
		again.Release()
	})
}

// TestMmap maps a file, writes through the mapping, and reads the file back
// the ordinary way: a mapping that accepted the write and never reached the
// file is the failure worth finding.
func TestMmap(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "mapped"
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }() // read-write, but every write goes through the mapping

	m, err := mmap.Map(f)
	require(t, "mmap", err, "mmap.Map")
	if len(m) != 10 {
		_ = m.Unmap()
		t.Fatalf("mapped %d bytes of a 10-byte file", len(m))
	}
	if string(m) != "0123456789" {
		_ = m.Unmap()
		t.Fatalf("the mapping reads %q, want the file's contents", m)
	}
	m[0] = 'X'
	if err := m.Unmap(); err != nil {
		t.Fatalf("Unmap: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "X123456789" {
		t.Fatalf("after writing through the mapping the file holds %q, want %q", b, "X123456789")
	}
}
