package vmconformance

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crgimenes/native/screen"
)

// showInConsole runs cmds, cmd.exe command lines that only read, in a console
// window of their own on this desktop, photographs the window once they have
// all finished — each command echoed above what it printed, as anyone would
// see it typing them — closes the window, and returns everything they printed.
//
// A classic console (conhost.exe named outright), not whatever the default
// terminal is, so the window is one this test can find by its title,
// photograph with PrintWindow and close with WM_CLOSE. Cleanup makes sure it
// is gone, and fails the test if it is not: a check leaves nothing on the
// screen (docs/DEVELOPMENT.md, desktop hygiene).
func showInConsole(t *testing.T, cmds ...string) string {
	t.Helper()
	dir := t.TempDir()
	id := fmt.Sprintf("irgo-vm-check-%d", time.Now().UnixNano())
	all, done := filepath.Join(dir, "all.txt"), filepath.Join(dir, "done.txt")
	var b strings.Builder
	b.WriteString("@echo off\r\nmode con: cols=118 lines=34 >nul\r\ntitle " + id + "\r\n")
	for i, c := range cmds {
		part := filepath.Join(dir, fmt.Sprintf("%d.txt", i))
		b.WriteString("echo ^> " + echoSafe(c) + "\r\n")
		b.WriteString(c + " > \"" + part + "\" 2>&1\r\n")
		b.WriteString("type \"" + part + "\"\r\n")
		b.WriteString("echo ^> " + echoSafe(c) + " >> \"" + all + "\"\r\n")
		b.WriteString("type \"" + part + "\" >> \"" + all + "\"\r\n")
		b.WriteString("echo.\r\n")
	}
	b.WriteString("echo done> \"" + done + "\"\r\n")
	bat := filepath.Join(dir, "show.cmd")
	if err := os.WriteFile(bat, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe"), "cmd.exe", "/k", bat)
	if err := c.Start(); err != nil {
		t.Fatalf("starting a console: %v", err)
	}
	var win window
	t.Cleanup(func() {
		if win.hwnd != 0 {
			if err := closeWindow(win.hwnd, win.pid); err != nil {
				t.Errorf("the console this test opened would not close: %v", err)
			}
		}
		_ = c.Process.Kill() // gone already when the window closed
		_ = c.Wait()
	})

	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			// Photographed anyway, so the record shows what it was stuck on.
			if w, ok := waitWindow(func(w window) bool { return strings.Contains(w.title, id) }, time.Second); ok {
				win = w
				shoot(t, func() (image.Image, error) { return screen.CaptureWindow(uint32(win.hwnd)) })
				t.Fatalf("the commands had not finished in their console after 60s (window %q, %s): %s", w.title, w.class, strings.Join(cmds, "; "))
			}
			var consoles []string
			for _, w := range topWindows() {
				if w.class == "ConsoleWindowClass" || w.class == "CASCADIA_HOSTING_WINDOW_CLASS" {
					consoles = append(consoles, fmt.Sprintf("%q (%s)", w.title, w.class))
				}
			}
			t.Fatalf("the commands had not finished after 60s, and no window is titled %s; consoles on the desktop: %s; commands: %s",
				id, strings.Join(consoles, ", "), strings.Join(cmds, "; "))
		}
		time.Sleep(200 * time.Millisecond)
	}
	var ok bool
	if win, ok = waitWindow(func(w window) bool { return strings.Contains(w.title, id) }, 10*time.Second); !ok {
		t.Fatalf("no window titled %s appeared for the console", id)
	}
	shoot(t, func() (image.Image, error) { return screen.CaptureWindow(uint32(win.hwnd)) })
	out, err := os.ReadFile(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// echoSafe escapes a command line for echo, so it is shown as typed.
func echoSafe(s string) string {
	return strings.NewReplacer("^", "^^", "&", "^&", "|", "^|", "<", "^<", ">", "^>", "%", "%%").Replace(s)
}
