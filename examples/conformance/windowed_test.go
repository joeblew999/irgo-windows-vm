//go:build darwin || windows

package conformance

import (
	"errors"
	"image"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/glaze/menu"
	"github.com/crgimenes/native/nocapture"
	"github.com/crgimenes/native/openurl"
	"github.com/crgimenes/native/tray"
)

// The capabilities a desktop app needs a window for. Each opens its own window
// and tears it down, so one that wedges its window does not take the next
// test's with it.

// TestOpenURL is openurl's safety boundary, and deliberately nothing more.
//
// Open hands only http, https, mailto and file URLs to the OS, so a hostile
// string cannot reach an arbitrary protocol handler; Reveal refuses a path
// that does not exist. Both refusals happen before the OS is asked, so these
// cases have no side effect at all, and a silent acceptance is a
// vulnerability, so it fails rather than skips.
//
// What is NOT here, on purpose: Open and Reveal on a real target. Each hands
// the target to another process — Finder or Explorer, a browser — which opens
// a window this process does not own and cannot close. glaze-all -probe did
// exactly that on every run, and left a file-manager window on the guest's
// desktop each time. An automated suite that leaves windows behind is not
// repeatable, and "the call returned nil" is all it could have asserted
// anyway: whether the right window appeared is a thing to look at. That is
// what examples/glaze-all is for — `mise run glaze:hands`, and press the
// buttons.
func TestOpenURL(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"custom_protocol", "ms-settings:privacy"},
		{"javascript", "javascript:alert(1)"},
		{"app_scheme", "irgo-conformance://x"},
		{"no_scheme", "example.com/path"},
		{"empty", ""},
	} {
		t.Run("refuses_"+tc.name, func(t *testing.T) {
			err := openurl.Open(tc.url)
			switch {
			case err == nil:
				t.Fatalf("Open(%q) returned nil: the URL was handed to the OS", tc.url)
			case !errors.Is(err, openurl.ErrScheme):
				t.Fatalf("Open(%q) refused, but not with ErrScheme: %v", tc.url, err)
			}
		})
	}
	t.Run("reveal_refuses_missing_path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		err := openurl.Reveal(missing)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Reveal(%q) = %v, want an error wrapping fs.ErrNotExist", missing, err)
		}
	})
}

// TestTray raises a tray icon, proves it is up by changing its menu, takes it
// down, and proves it is gone.
//
// tray.Run blocks, driving the event loop until Stop, so it is posted to the
// UI thread and not waited on. And it comes after the window: on macOS glaze's
// New runs a temporary [NSApp run] that ends only when
// applicationDidFinishLaunching fires, once per process, and a tray started
// first consumes it (docs/UPSTREAM.md §1). openWindow has already made a
// window by the time this runs, and earlier tests will have too.
func TestTray(t *testing.T) {
	w := openWindow(t, glaze.Options{}, nil)

	ran := make(chan error, 1)
	w.Dispatch(func() {
		ran <- tray.Run(tray.Config{
			Title:   "irgo",
			Tooltip: "irgo conformance test",
			Icon:    tinyPNG,
			Items:   []tray.Item{{Title: "conformance test"}},
		})
	})
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			tray.Stop() // never leave an icon in someone's menu bar
			select {
			case <-ran:
			case <-time.After(uiTimeout):
				t.Errorf("tray.Run did not return within %s of Stop", uiTimeout)
			}
		}
	})

	t.Run("running", func(t *testing.T) {
		// SetItems answers ErrNotRunning until Run has the icon up, so it is
		// the question "is it up" asked of the package itself.
		deadline := time.Now().Add(uiTimeout)
		for {
			select {
			case err := <-ran:
				stopped = true
				require(t, "tray", err, "tray.Run returned without being stopped")
				t.Fatal("tray.Run returned nil without being stopped")
			default:
			}
			err := tray.SetItems([]tray.Item{{Title: "conformance test"}, {Separator: true}, {Title: "still here"}})
			if err == nil {
				shootOr(t, func() (image.Image, error) { return grabTray(w) }, // the icon is up
					func() (image.Image, error) { return grabWindow(w) }, "the window the tray was started beside")
				return
			}
			if !errors.Is(err, tray.ErrNotRunning) || time.Now().After(deadline) {
				require(t, "tray", err, "tray.SetItems while Run should be up")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	if t.Failed() {
		return
	}

	t.Run("stop_removes_it", func(t *testing.T) {
		tray.Stop()
		select {
		case err := <-ran:
			stopped = true
			if err != nil {
				t.Fatalf("tray.Run returned %v after Stop, want nil", err)
			}
		case <-time.After(uiTimeout):
			t.Fatalf("tray.Run did not return within %s of Stop", uiTimeout)
		}
		if err := tray.SetItems([]tray.Item{{Title: "gone"}}); !errors.Is(err, tray.ErrNotRunning) {
			t.Fatalf("SetItems after Stop = %v, want ErrNotRunning", err)
		}
	})
}

// TestMenu installs a menu bar and releases it.
//
// Window is required on Windows, where the menu belongs to the HWND, and
// ignored on macOS, where the menu bar is global. Dispatch is passed because
// Set is called here with the run loop already draining, which is the one case
// the package says to pass it; before Run it would be the hang its doc warns
// about.
func TestMenu(t *testing.T) {
	w := openWindow(t, glaze.Options{}, nil)
	m, err := menu.Set([]menu.Item{
		{Title: "Conformance", Submenu: []menu.Item{
			{Title: "About"},
			{Separator: true},
			{Title: "Quit", Shortcut: "cmd+q"},
		}},
	}, menu.Options{Window: w.Window(), Dispatch: w.Dispatch})
	require(t, "menu", err, "menu.Set")
	if m == nil {
		t.Fatal("menu.Set returned no menu and no error")
	}
	shootOr(t, func() (image.Image, error) { return grabMenu(w) }, // installed, not yet released
		func() (image.Image, error) { return grabWindow(w) }, "the window the menu was installed for")
	onUI(t, w, m.Release)
}

// TestNoCapture excludes the window from screen capture, and on Windows reads
// the window's display affinity back from the OS rather than trusting nil.
func TestNoCapture(t *testing.T) {
	w := openWindow(t, glaze.Options{}, labelled(t))
	var err error
	onUI(t, w, func() { err = nocapture.Protect(w.Window()) })
	// Before require, which skips on macOS: there the picture is the window
	// the OS would not hide. On Windows a black one is the proof.
	shootExpecting(t, "black: the window is excluded from capture, which is what Protect is for",
		func() (image.Image, error) { return grabWindow(w) })
	require(t, "nocapture", err, "nocapture.Protect")
	checkCaptureExcluded(t, w)
}

// TestAppIcon sets the application's icon. Unsupported on Windows by design:
// there a process's icon is the executable's, decided before it runs.
func TestAppIcon(t *testing.T) {
	w := openWindow(t, glaze.Options{}, labelled(t))
	var err error
	onUI(t, w, func() { err = glaze.SetAppIcon(tinyPNG) })
	shoot(t, func() (image.Image, error) { return grabWindow(w) }) // before require, which skips on Windows
	require(t, "appicon", err, "glaze.SetAppIcon")
}

// TestFileDialog presents a native open-file dialog, confirms from outside
// glaze that it is on screen, dismisses it the way a user's Cancel would, and
// checks OpenFile reports a cancel: "", nil.
//
// The COM and WinRT plumbing on Windows, and the modal session on macOS, are
// the fragile part, not the user's eventual click. glaze-all -probe could only
// present the dialog and leave it open until the process exited; dismissing it
// is what lets this run in a suite, with other tests after it.
func TestFileDialog(t *testing.T) {
	w := openWindow(t, glaze.Options{}, labelled(t))
	type result struct {
		path string
		err  error
	}
	got := make(chan result, 1)
	dir := t.TempDir()
	go func() {
		// An empty directory of its own, so the screenshot of the dialog shows
		// nothing of whoever ran the suite.
		p, err := w.OpenFile(glaze.FileDialogOptions{Title: "irgo conformance (closes itself)", Directory: dir})
		got <- result{p, err}
	}()

	whileUp := func(dialog uintptr) {
		shoot(t, func() (image.Image, error) { return grabDialog(dialog) })
	}
	if err := dismissFileDialog(w, uiTimeout, whileUp); err != nil {
		t.Errorf("%v", err)
	}
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("OpenFile: %v", r.err)
		}
		if r.path != "" {
			t.Fatalf("OpenFile = %q after the dialog was cancelled, want \"\"", r.path)
		}
	case <-time.After(uiTimeout):
		t.Fatalf("OpenFile had not returned %s after its dialog was dismissed; the dialog may still be open", uiTimeout)
	}
}
