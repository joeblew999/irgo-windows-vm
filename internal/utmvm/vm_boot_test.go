package utmvm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// renderBootScript mirrors what typeBootCommand does, so the rendering can be
// asserted without driving a VM.
func renderBootScript(vmRef, fsn, path string) string {
	return fmt.Sprintf(bootScript, (90 * time.Millisecond).Seconds(), vmRef, fsn, path)
}

// The bug this guards, which cost an entire debugging session:
//
// An escaping helper doubled every backslash before Sprintf's %q, which escapes
// them again. Go and AppleScript share backslash escape syntax, so the guest
// received \\efi\\microsoft\\boot\\cdboot_noprompt.efi and the UEFI shell
// silently did nothing. Meanwhile hand-written osascript worked, which pointed
// suspicion at the firmware for hours.
//
// The rendered script must contain the path escaped exactly once.
func TestBootScriptEscapesBackslashesExactlyOnce(t *testing.T) {
	const path = `\efi\microsoft\boot\cdboot_noprompt.efi`
	got := renderBootScript("SOME-UUID", "fs0:", path)

	// %q on a Windows path yields single-escaped backslashes: \\efi\\microsoft…
	want := `"\\efi\\microsoft\\boot\\cdboot_noprompt.efi"`
	if !strings.Contains(got, want) {
		t.Errorf("path not escaped exactly once.\n got script fragment: %s\n want to contain:     %s",
			extractQuoted(got, "cdboot"), want)
	}

	// Four backslashes in the source would mean double-escaping: AppleScript
	// would collapse them to two and the guest would see a literal \\.
	if strings.Contains(got, `\\\\efi`) {
		t.Error("backslashes are doubled twice; the guest will receive \\\\efi\\\\... " +
			"and the shell will not find the file")
	}
}

// typeBootCommand supplies exactly four values. A template edit that adds or
// removes a verb ships %!q(MISSING) into an AppleScript, which fails at runtime
// inside a VM — the slowest possible place to discover it.
func TestBootScriptVerbArity(t *testing.T) {
	got := renderBootScript("UUID", "fs0:", `\efi\boot\bootaa64.efi`)
	for _, bad := range []string{"%!", "MISSING", "EXTRA"} {
		if strings.Contains(got, bad) {
			t.Errorf("rendered script contains %q — assets/boot.applescript's verb "+
				"count no longer matches typeBootCommand's arguments", bad)
		}
	}
	for _, want := range []string{"UUID", "fs0:", "bootaa64.efi"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered script is missing %q", want)
		}
	}
}

// The same arity trap applies to the plist, where a mismatch produces a config
// UTM rejects with its usual field-less "cannot import this VM".
func TestPlistHasNoFormattingErrors(t *testing.T) {
	got, err := testConfig().Plist()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"%!", "MISSING", "EXTRA"} {
		if strings.Contains(got, bad) {
			t.Errorf("plist contains %q — assets/config.plist.tmpl's verbs no longer "+
				"match Config.Plist's arguments", bad)
		}
	}
}

func extractQuoted(s, near string) string {
	i := strings.Index(s, near)
	if i < 0 {
		return "(not found)"
	}
	start := i
	for start > 0 && s[start] != '"' {
		start--
	}
	end := i
	for end < len(s) && s[end] != '\n' {
		end++
	}
	return s[start:end]
}
