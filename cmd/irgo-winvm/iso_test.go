package main

import (
	"errors"
	"strings"
	"testing"
)

// TestMediaSidecarsAreNotCounted: one ISO and its .scan sidecar are one file.
//
// Negative control: making sidecar() return false reports 2 files and 3032 B.
func TestMediaSidecarsAreNotCounted(t *testing.T) {
	m := mediaFiles{{"/m/win11-arm64.iso", 3000}, {"/m/win11-arm64.iso.scan", 32}}
	n, size := m.total()
	if n != 1 || size != 3000 {
		t.Errorf("total() = %d files, %d bytes; want 1, 3000", n, size)
	}
	var lines []string
	m.list(func(f string, a ...any) { lines = append(lines, f) })
	if len(lines) != 1 {
		t.Errorf("list printed %d lines, want 1 (the sidecar is not listed)", len(lines))
	}
}

func TestISODeleteRefusal(t *testing.T) {
	m := mediaFiles{{"/m/win11-arm64.iso", 3000}, {"/m/win11-arm64.iso.scan", 32}}
	for _, tc := range []struct {
		name  string
		media mediaFiles
		tools int
		all   bool
		want  []string
	}{
		{"tools only", nil, 2, false, []string{"2 tool(s)\n  Pass -force"}},
		{"media kept esd", m, 2, false, []string{"1 file(s)", " and 2 tool(s)", "about 40s", "Add -all"}},
		{"media with esd", m, 0, true, []string{"1 file(s)", "Includes the .esd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := isoDeleteRefusal(tc.media, tc.tools, tc.all)
			if !errors.Is(err, errRefused) {
				t.Fatalf("refusal is not errRefused, so it would not exit 5: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal %q does not contain %q", err, w)
				}
			}
		})
	}
}
