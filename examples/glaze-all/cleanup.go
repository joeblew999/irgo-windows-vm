package main

// Leaving nothing behind.
//
// -probe runs on the owner's own Mac (glaze-check, mise run glaze:mac) and on
// the VM's desktop, unattended. Until 30 Sep 2026 every run left windows
// there: openurl.Open and openurl.Reveal each open a file-manager window and
// nothing closed them, so Finder windows onto $TMPDIR piled up on the Mac (five
// were counted) and explorer.exe windows on the VM (six processes). Worse on
// Windows: the temp directory was deleted while Explorer was still navigating
// to it, which added a "Location is not available" error box per run. And the
// file dialog was left presented while the process exited under it.
//
// So each call that opens a window here now closes that window again, and
// checks that it is gone — a close that was only requested is not a close. The
// windows are found by difference: the file manager's windows before the call,
// against the ones after it that show the directory the call was given. Only
// those are closed, so a Finder window the owner had open is never touched.
//
// The platform halves are cleanup_darwin.go (Finder, through osascript) and
// cleanup_windows.go (Explorer, through user32). This file is the logic both
// share, which is also the part that can be tested off the platform.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// fmWindow is one file-manager window: an id the platform can close it by, and
// where it is — the folder's path from Finder, the title from Explorer.
type fmWindow struct {
	ID    uint64
	Place string
}

const (
	// openWait bounds how long a window is waited for after Open or Reveal
	// returned. Finder answers in well under a second; Explorer on the VM
	// has been seen to take several.
	openWait = 10 * time.Second
	// openSettle is how long to keep looking once the first window appeared,
	// in case the call opened more than one.
	openSettle = 1500 * time.Millisecond
	// closeWait bounds how long a closed window may take to go.
	closeWait = 5 * time.Second
	pollEvery = 250 * time.Millisecond
)

// errNoWindow is Open or Reveal returning nil with no window to show for it.
// That is recorded as a failure: the call claimed to have handed the path to
// the file manager, and nothing on the screen says it did.
var errNoWindow = errors.New("returned nil, but no file-manager window showing the directory appeared")

// newWindows returns the windows in after that were not in before and whose
// place is one the call was expected to open.
func newWindows(before, after []fmWindow, want func(place string) bool) []fmWindow {
	had := make(map[uint64]bool, len(before))
	for _, w := range before {
		had[w.ID] = true
	}
	var out []fmWindow
	for _, w := range after {
		if !had[w.ID] && want(w.Place) {
			out = append(out, w)
		}
	}
	return out
}

// samePath reports whether a path Finder gave back names dir. Finder answers
// with symlinks resolved and a trailing slash — /private/var/folders/.../ for
// a $TMPDIR of /var/folders/... — so both sides are cleaned and resolved
// before they are compared.
func samePath(place, dir string, resolve func(string) (string, error)) bool {
	norm := func(p string) string {
		p = filepath.Clean(p)
		if r, err := resolve(p); err == nil {
			p = r
		}
		return p
	}
	return norm(place) == norm(dir)
}

// titleShows reports whether an Explorer window title is the folder dir.
// Explorer titles a window "<folder> - File Explorer", or just "<folder>"
// with the title-bar setting off; either way it starts with the folder's name.
// A prefix of the name alone is not enough: irgo-openurl-1 must not match a
// window onto irgo-openurl-12.
func titleShows(title, dir string) bool {
	name := filepath.Base(strings.ReplaceAll(dir, `\`, "/"))
	if name == "" || name == "." || name == "/" {
		return false
	}
	return strings.EqualFold(title, name) || strings.HasPrefix(strings.ToLower(title), strings.ToLower(name)+" - ")
}

// existingShows reports whether the front window was already open before the
// call, shows one of the call's places, and has target selected — the file
// manager answering in a window it had rather than opening one.
func existingShows(before []fmWindow, front uint64, selected []string, target string, match func(place, dir string) bool) bool {
	var frontPlace string
	found := false
	for _, w := range before {
		if w.ID == front {
			frontPlace, found = w.Place, true
		}
	}
	if !found || !match(frontPlace, filepath.Dir(target)) {
		return false
	}
	for _, s := range selected {
		if match(strings.TrimSpace(s), target) {
			return true
		}
	}
	return false
}

// opened is what openAndClose did about the windows.
type opened struct {
	closed int  // windows it opened, closed again and checked gone
	reused bool // the file manager used a window that was already open
}

// reuseAfter is how long to wait for a new window before asking whether the
// file manager used an existing one instead.
const reuseAfter = 2 * time.Second

// openAndClose runs open, waits for the windows it opened onto one of places,
// closes them and checks they are gone. It returns open's error, and
// separately what went wrong with the cleanup. target is the path the call was
// given, which is what a reused window has selected.
func openAndClose(open func() error, target string, places ...string) (openErr error, did opened, cleanErr error) {
	want := func(place string) bool {
		for _, p := range places {
			if placeMatches(place, p) {
				return true
			}
		}
		return false
	}
	before, err := listFileManagerWindows()
	if err != nil {
		// Not opened, since nothing could be closed afterwards: calling open
		// here would be exactly the window this file exists to stop leaving.
		return nil, did, fmt.Errorf("listing the file manager's windows before the call, so it was not made: %w", err)
	}
	if openErr = open(); openErr != nil {
		return openErr, did, nil
	}

	var opened []fmWindow
	start := time.Now()
	for len(opened) == 0 {
		if time.Since(start) > openWait {
			return nil, did, fmt.Errorf("%w within %s", errNoWindow, openWait)
		}
		time.Sleep(pollEvery)
		after, lErr := listFileManagerWindows()
		if lErr != nil {
			return nil, did, fmt.Errorf("listing the file manager's windows after the call: %w", lErr)
		}
		opened = newWindows(before, after, want)
		if len(opened) == 0 && time.Since(start) > reuseAfter {
			reused, rErr := shownInExisting(before, target)
			if rErr != nil {
				return nil, did, rErr
			}
			if reused {
				did.reused = true
				return nil, did, nil
			}
		}
	}
	time.Sleep(openSettle)
	if after, lErr := listFileManagerWindows(); lErr == nil {
		opened = newWindows(before, after, want)
	}

	// Checked, not assumed: a close is a request the file manager may ignore.
	// A window that turns up late is closed too, rather than left for the
	// next run to find.
	asked := map[uint64]bool{}
	deadline := time.Now().Add(closeWait)
	for {
		for _, w := range opened {
			if asked[w.ID] {
				continue
			}
			asked[w.ID] = true
			if cErr := closeFileManagerWindow(w.ID); cErr != nil {
				return nil, did, fmt.Errorf("closing the window onto %s: %w", w.Place, cErr)
			}
		}
		time.Sleep(pollEvery)
		after, lErr := listFileManagerWindows()
		if lErr != nil {
			return nil, did, fmt.Errorf("listing the file manager's windows after closing: %w", lErr)
		}
		if opened = newWindows(before, after, want); len(opened) == 0 {
			did.closed = len(asked)
			return nil, did, nil
		}
		if time.Now().After(deadline) {
			return nil, did, fmt.Errorf("asked %d window(s) to close and %d still open after %s (first: %q)",
				len(asked), len(opened), closeWait, opened[0].Place)
		}
	}
}
