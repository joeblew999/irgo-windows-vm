package utmvm

import "testing"

// TestShotNameStage covers the stage names that broke the first parser.
//
// It counted dashes from the end of the filename, so any stage containing one
// was truncated to its last word: running-no-agent was published as agent.png
// and vm-screen as screen.png.
//
// Negative control: replacing the pattern with `-(\w+)\.png$` fails the
// running-no-agent, booting-1 and vm-screen cases.
func TestShotNameStage(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"irgo-win11-20260813-201225-booting-1.png", "booting-1"},
		{"irgo-win11-20260813-201233-booting-2.png", "booting-2"},
		{"irgo-win11-20260813-201241-ready.png", "ready"},
		{"irgo-win11-20260813-181410-running-no-agent.png", "running-no-agent"},
		{"irgo-win11-20260813-185441-vm-screen.png", "vm-screen"},
		{"irgo-win11-20260813-180527-stalled-1.png", "stalled-1"},
		// A VM whose own name carries dashes and digits must not confuse it.
		{"my-test-vm-2-20260813-180527-copying.png", "copying"},
	} {
		m := shotName.FindStringSubmatch(tc.file)
		if m == nil {
			t.Errorf("%s: no match", tc.file)
			continue
		}
		if m[1] != tc.want {
			t.Errorf("%s: stage = %q, want %q", tc.file, m[1], tc.want)
		}
	}
}
