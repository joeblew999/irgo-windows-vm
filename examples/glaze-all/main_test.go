package main

import "testing"

// The Windows case is the reason this test exists and the reason fileURL is not
// string concatenation. It is also the half that cannot be caught by running
// the example on the machine it was written on: filepath.ToSlash is a no-op on
// macOS, so a Windows-shaped path only ever appears here.
//
// A wrong result is not a visible failure either — ShellExecuteW returns an
// error code, which surfaces as "openurl.Open FAILED (code 2)" in the VM and
// looks like a missing handler rather than a malformed URL.
func TestFileURL(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"unix absolute", "/tmp/irgo-openurl-123", "file:///tmp/irgo-openurl-123"},
		{"unix with space", "/tmp/irgo probe", "file:///tmp/irgo%20probe"},
		{"windows drive", `C:\Users\Public\probe`, "file:///C:/Users/Public/probe"},
		{"windows with space", `C:\Users\Public\irgo probe`, "file:///C:/Users/Public/irgo%20probe"},
		{"already slashed", "C:/Users/Public", "file:///C:/Users/Public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The backslash forms are converted by hand rather than by
			// filepath.ToSlash, which does nothing off Windows — so this test
			// asserts the Windows shape while running on macOS, which is the
			// only place it gets asserted at all.
			got := fileURL(toSlashAlways(tc.path))
			if got != tc.want {
				t.Errorf("fileURL(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// toSlashAlways is filepath.ToSlash without the build-tag dependence, so the
// Windows cases above are exercised on every platform.
func toSlashAlways(p string) string {
	out := []rune(p)
	for i, r := range out {
		if r == '\\' {
			out[i] = '/'
		}
	}
	return string(out)
}
