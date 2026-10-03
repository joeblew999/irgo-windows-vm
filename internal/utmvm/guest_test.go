package utmvm

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Which system is in a VM comes from its record, and has three answers: the
// record says, there is nothing to say otherwise (Windows, which is every VM
// made before the field existed), or it cannot be told, which refuses.
//
// Negative controls, run by hand 2 Oct 2026: with guestNamed returning
// windowsGuest for a name it does not know, "an unknown system" is accepted;
// with readRecord's error dropped in guestOf, "an unreadable record" is
// Windows; with names remembered like UUIDs, "again" is still Windows after
// it was made anew as Linux.
func TestGuestOfReadsTheRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	guestCache.Clear()
	t.Cleanup(guestCache.Clear)
	now := time.Now().UTC()
	// A name is read from its record every time, never remembered: a VM
	// deleted and made again under the same name may hold another system.
	if err := writeRecord(VMRecord{Name: "again", Created: now, LastUsed: now}); err != nil {
		t.Fatal(err)
	}
	if g, err := guestOf("again"); err != nil || g.name != GuestWindows {
		t.Fatalf("again, first: %q, %v", g.name, err)
	}
	if err := writeRecord(VMRecord{Name: "again", Created: now, LastUsed: now, OS: GuestLinux}); err != nil {
		t.Fatal(err)
	}
	if g, err := guestOf("again"); err != nil || g.name != GuestLinux {
		t.Fatalf("again, made anew as Linux: %q, %v; want linux", g.name, err)
	}
	for _, r := range []VMRecord{
		{Name: "old", Owner: "a", Created: now, LastUsed: now},
		{Name: "win", Owner: "a", Created: now, LastUsed: now, OS: "windows"},
		{Name: "other", Owner: "a", Created: now, LastUsed: now, OS: "plan9"},
	} {
		if err := writeRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(recordPath("broken"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, vm, want string // want "" is ErrGuestOS
	}{
		{"no record", "irgo-win11", "windows"},
		{"a record from before the field", "old", "windows"},
		{"a record that says windows", "WIN", "windows"},
		{"an unknown system", "other", ""},
		{"an unreadable record", "broken", ""},
	} {
		g, err := guestOf(c.vm)
		switch {
		case c.want == "" && !errors.Is(err, ErrGuestOS):
			t.Errorf("%s: got %q, %v; want ErrGuestOS", c.name, g.name, err)
		case c.want != "" && (err != nil || g.name != c.want):
			t.Errorf("%s: got %q, %v; want %s", c.name, g.name, err, c.want)
		}
	}
}

// What runs in a Windows guest is what ran before these values moved into the
// description: the same paths, the same argv for a script, a delete and the
// ssh script, the same address command. A change here is a change of
// behaviour in every guest, and is proven on one (docs/findings.md).
//
// Negative control, run by hand 2 Oct 2026: `del /q /f` for `del /q` in
// windowsGuest.remove fails it.
func TestWindowsGuestIsWhatTheCodeWrote(t *testing.T) {
	g := windowsGuest
	for _, c := range []struct {
		what      string
		got, want any
	}{
		{"a temp path", g.tempPath("irgox-1.bat"), `C:\Windows\Temp\irgox-1.bat`},
		{"a public path", g.publicPath(g.sshFile), `C:\Users\Public\irgo-vm-ssh.ps1`},
		{"GuestPublicPath", GuestPublicPath("x"), `C:\Users\Public\x`},
		{"running a script", g.runScript(`C:\a.bat`), []string{"cmd.exe", "/c", `C:\a.bat`}},
		{"removing files", g.remove(`C:\a`, `C:\b`, `C:\c`), []string{"cmd.exe", "/c", `del /q C:\a C:\b C:\c`}},
		{"the ssh script's argv", g.sshRun(`C:\s.ps1`, []string{"-Remove"}),
			[]string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", `C:\s.ps1`, "-Remove"}},
		{"the address command", g.addrCmd, []string{"ipconfig"}},
		{"a line ending", g.eol, "\r\n"},
		{"a script", g.script([][]string{{"prog.exe"}}, `C:\o.txt`, `C:\r.txt`), batchFile([]string{"prog.exe"}, `C:\o.txt`, `C:\r.txt`)},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: %q, want %q", c.what, c.got, c.want)
		}
	}
	if !strings.Contains(g.sshScript, "OpenSSH.Server") {
		t.Error("the Windows ssh script is not vm-ssh.ps1")
	}
}
