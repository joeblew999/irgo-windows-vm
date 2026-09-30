package utmvm

import (
	"strings"
	"testing"
)

func TestHasDesktopSession(t *testing.T) {
	loggedIn := `"explorer.exe","5212","Console","1","98,440 K","Running","IRGO-WIN11\dev","0:00:03","N/A"` + "\r\n"
	other := `"explorer.exe","5212","Console","1","98,440 K","Running","IRGO-WIN11\developer","0:00:03","N/A"` + "\r\n"
	for _, c := range []struct {
		name, out, user string
		want            bool
	}{
		{"dev logged in", loggedIn, "dev", true},
		{"case differs", loggedIn, "DEV", true},
		{"a longer name is not dev", other, "dev", false},
		{"nobody logged in", "INFO: No tasks are running which match the specified criteria.\r\n", "dev", false},
		{"empty", "", "dev", false},
	} {
		if got := hasDesktopSession(c.out, c.user); got != c.want {
			t.Errorf("%s: hasDesktopSession = %v, want %v", c.name, got, c.want)
		}
	}
}

// The repair script is embedded; a build that lost it would push an empty file
// and report success.
func TestRepairScriptIsEmbedded(t *testing.T) {
	for _, want := range []string{"maxpwage:unlimited", "PasswordNeverExpires", "EBWebView", "--msedgewebview --system-level"} {
		if !strings.Contains(vmRepairScript, want) {
			t.Errorf("vm-repair.ps1 does not contain %q", want)
		}
	}
}
