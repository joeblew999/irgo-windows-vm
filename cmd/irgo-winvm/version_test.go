package main

import (
	"runtime/debug"
	"testing"
)

// TestModuleVersion: `go install ...@v0.5.0` reports v0.5.0; a GoReleaser
// stamp wins; a local build's pseudo-version, dirty or not, stays dev.
//
// Negative control, run by hand: drop the releaseTag check and the
// pseudo-version cases report themselves as releases.
func TestModuleVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	for _, tc := range []struct {
		stamped, module, want string
	}{
		{"dev", "v0.5.0", "v0.5.0"},
		{"v0.5.0", "(devel)", "v0.5.0"},
		{"v0.5.0", "v0.4.1", "v0.5.0"},
		{"dev", "(devel)", "dev"},
		{"dev", "v0.4.2-0.20261001101500-d6b79a9c1f2e", "dev"},
		{"dev", "v0.4.2-0.20261001101500-d6b79a9c1f2e+dirty", "dev"},
		{"dev", "v0.5.0+dirty", "dev"},
		{"dev", "", "dev"},
	} {
		if got := moduleVersion(tc.stamped, info(tc.module)); got != tc.want {
			t.Errorf("stamped %q, module %q: got %q, want %q", tc.stamped, tc.module, got, tc.want)
		}
	}
	if got := moduleVersion("dev", func() (*debug.BuildInfo, bool) { return nil, false }); got != "dev" {
		t.Errorf("no build info: got %q", got)
	}
}
