package main

import (
	"errors"
	"strings"
	"testing"
)

// The windows a run closes are the difference between two listings, filtered
// to the places the call was given. Getting either half wrong closes a window
// the owner had open, so both halves are asserted, each by a case that fails
// if only that half is broken.
func TestNewWindows(t *testing.T) {
	before := []fmWindow{{1, "/Users/x/Downloads/"}, {2, "/tmp/T/"}}
	after := []fmWindow{{1, "/Users/x/Downloads/"}, {2, "/tmp/T/"}, {3, "/tmp/T/irgo-openurl-1/"}, {4, "/Users/x/Documents/"}}
	want := func(p string) bool { return strings.HasPrefix(p, "/tmp/T/") }

	got := newWindows(before, after, want)
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("newWindows = %v, want only window 3", got)
	}
	if got := newWindows(after, after, want); len(got) != 0 {
		t.Errorf("nothing new, yet newWindows = %v", got)
	}
}

// Finder answers with symlinks resolved and a trailing slash; the probe holds
// the path os.MkdirTemp gave it. $TMPDIR is under /var, which is a symlink to
// /private/var, so without resolving both sides no window would ever match and
// every run would report "no window appeared".
func TestSamePath(t *testing.T) {
	resolve := func(p string) (string, error) {
		if strings.HasPrefix(p, "/var/") {
			return "/private" + p, nil
		}
		if p == "/gone" {
			return "", errors.New("no such file")
		}
		return p, nil
	}
	for _, c := range []struct {
		place, dir string
		want       bool
	}{
		{"/private/var/folders/T/irgo-openurl-1/", "/var/folders/T/irgo-openurl-1", true},
		{"/private/var/folders/T/", "/var/folders/T", true},
		{"/private/var/folders/T/irgo-openurl-12/", "/var/folders/T/irgo-openurl-1", false},
		{"/private/var/folders/T/", "/var/folders/T/irgo-openurl-1", false},
		{"", "/var/folders/T", false}, // a window whose path could not be read
		{"/gone/", "/gone", true},     // unresolvable on both sides still compares cleaned
	} {
		if got := samePath(c.place, c.dir, resolve); got != c.want {
			t.Errorf("samePath(%q, %q) = %v, want %v", c.place, c.dir, got, c.want)
		}
	}
}

func TestTitleShows(t *testing.T) {
	for _, c := range []struct {
		title, dir string
		want       bool
	}{
		{"irgo-openurl-123 - File Explorer", `C:\Users\dev\AppData\Local\Temp\irgo-openurl-123`, true},
		{"Temp - File Explorer", `C:\Users\dev\AppData\Local\Temp`, true},
		{"irgo-openurl-123", `C:\Users\dev\AppData\Local\Temp\irgo-openurl-123`, true},
		{"TEMP - File Explorer", `C:\Users\dev\AppData\Local\Temp`, true},
		{"irgo-openurl-1234 - File Explorer", `C:\Users\dev\AppData\Local\Temp\irgo-openurl-123`, false},
		{"Desktop - File Explorer", `C:\Users\dev\AppData\Local\Temp`, false},
		{"Temporary - File Explorer", `C:\Users\dev\AppData\Local\Temp`, false},
		{"", `C:\Users\dev\AppData\Local\Temp`, false},
		{"anything", `C:\`, false},
	} {
		if got := titleShows(c.title, c.dir); got != c.want {
			t.Errorf("titleShows(%q, %q) = %v, want %v", c.title, c.dir, got, c.want)
		}
	}
}

// A reused window is only accepted as "shown" when it was open before, shows
// the parent, and has the target selected. Each case below breaks exactly one
// of the three.
func TestExistingShows(t *testing.T) {
	eq := func(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }
	before := []fmWindow{{7, "/tmp/T/"}, {8, "/Users/x/Downloads/"}}
	target := "/tmp/T/irgo-openurl-1"
	sel := []string{"/tmp/T/irgo-openurl-1/"}
	for _, c := range []struct {
		name     string
		front    uint64
		selected []string
		want     bool
	}{
		{"parent window, target selected", 7, sel, true},
		{"front window was not open before", 9, sel, false},
		{"front window shows another folder", 8, sel, false},
		{"target not selected", 7, []string{"/tmp/T/other/"}, false},
		{"nothing selected", 7, nil, false},
	} {
		if got := existingShows(before, c.front, c.selected, target, eq); got != c.want {
			t.Errorf("%s: existingShows = %v, want %v", c.name, got, c.want)
		}
	}
}
