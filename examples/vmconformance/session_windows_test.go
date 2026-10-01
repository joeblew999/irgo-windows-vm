package vmconformance

import (
	"fmt"
	"image"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/native/screen"
)

// The checks made in dev's desktop session, through app-create -gui: what
// only a logged-in user has (a desktop, dev's own policies, a window that
// renders), and a picture of each setting as Windows itself reports it, so a
// reader sees what the record says rather than taking it on trust. Each
// window a test opens it closes; vm-check looks for anything left on the
// desktop afterwards.

// TestSessionDesktop: this is dev's interactive session, with the shell
// running in it. The picture is the whole desktop as the suite found it; the
// console on it is the suite's own.
func TestSessionDesktop(t *testing.T) {
	inSession(t)
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	id := sessionID()
	shell := command(t, "tasklist.exe", "/fi", "imagename eq explorer.exe", "/fi", "session eq "+strconv.Itoa(int(id)), "/fo", "csv", "/nh")
	running := strings.Contains(strings.ToLower(shell), `"explorer.exe"`)
	evidence(t, "%s in session %d; explorer.exe in it: %t", u.Username, id, running)
	if !strings.HasSuffix(strings.ToLower(u.Username), `\`+strings.ToLower(*devUser)) {
		t.Errorf("the session is %s's, not %s's", u.Username, *devUser)
	}
	if !running {
		t.Errorf("explorer.exe is not running in session %d: there is no shell, so no taskbar and no desktop", id)
	}
	shoot(t, func() (image.Image, error) {
		w, h, err := screen.Size()
		if err != nil {
			return nil, err
		}
		return screen.Capture(image.Rect(0, 0, w, h))
	})
}

// TestSessionNotificationsOff: toasts are off for dev — OneDrive's "Turn On
// Windows Backup" arrived as one — and OneDrive is not running.
func TestSessionNotificationsOff(t *testing.T) {
	inSession(t)
	wantDWORD(t, syscall.HKEY_CURRENT_USER, `Software\Policies\Microsoft\Windows\CurrentVersion\PushNotifications`,
		"NoToastApplicationNotification", 1, "a per-user policy: the answer file sets it for dev; irgo-winvm vm-repair sets it while dev is logged on")
	out := showInConsole(t,
		`reg query "HKCU\Software\Policies\Microsoft\Windows\CurrentVersion\PushNotifications"`,
		`reg query "HKLM\SOFTWARE\Policies\Microsoft\Windows\OneDrive"`,
		`tasklist /fi "imagename eq OneDrive.exe"`)
	if strings.Contains(strings.ToLower(out), "onedrive.exe ") {
		t.Errorf("OneDrive is running in dev's session, policy or not:\n%s", out)
	}
}

// TestSessionWebView2Renders: a glaze window opens, its page runs, and
// WebView2 names its version — the runtime is not just registered but works
// in this session. The picture is that window.
func TestSessionWebView2Renders(t *testing.T) {
	inSession(t)
	got := make(chan string, 1)
	w := openWindow(t, func(w glaze.WebView) {
		if err := w.Bind("ready", func(ua string) {
			select {
			case got <- ua:
			default:
			}
		}); err != nil {
			t.Errorf("Bind: %v", err)
		}
		w.SetHtml(`<!doctype html><html><body style="font:15px 'Segoe UI',sans-serif;margin:20px;background:#f4f7fb">` +
			`<h2 style="margin:0 0 6px;color:#0b5394">WebView2 renders</h2>` +
			`<p style="margin:0 0 6px">irgo VM conformance: this page was drawn by WebView2 in dev's session.</p>` +
			`<p id="ua" style="font:12px Consolas,monospace;color:#333"></p>` +
			`<script>document.getElementById('ua').textContent = navigator.userAgent; ready(navigator.userAgent);</script>` +
			`</body></html>`)
	})
	var ua string
	select {
	case ua = <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("the page's script did not run within 30s: WebView2 is not rendering in this session")
	}
	version := regexp.MustCompile(`Edg/([0-9.]+)`).FindStringSubmatch(ua)
	evidence(t, "%s", ua)
	if version == nil {
		t.Errorf("the page ran, and its user agent names no WebView2 (Edg/) version: %q", ua)
	}
	shoot(t, func() (image.Image, error) { return screen.CaptureWindow(uint32(uintptr(w.Window()))) })
}

// TestSessionEvidence shows each setting the SYSTEM tests read, as dev can
// read it — the commands are the ones anyone would type — and photographs
// it. It fails only when it cannot show them: whether the setting is right is
// the SYSTEM test's verdict, and counting it twice would make one missing
// setting two failures. What each picture shows of what the SYSTEM test
// wants is recorded beside it.
func TestSessionEvidence(t *testing.T) {
	inSession(t)
	for _, c := range []struct {
		name string
		cmds []string
		want []string // what the SYSTEM test wants, looked for in what they printed
	}{
		{"WindowsBuild", []string{`ver`, `reg query "HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion" /v DisplayVersion`}, []string{"Microsoft Windows"}},
		{"WindowsUpdatePolicy", []string{`reg query "HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate" /s`},
			[]string{"NoAutoRebootWithLoggedOnUsers    REG_DWORD    0x1", "AUOptions    REG_DWORD    0x2",
				"SetUpdateNotificationLevel    REG_DWORD    0x1", "UpdateNotificationLevel    REG_DWORD    0x2",
				"SetAutoRestartNotificationDisable    REG_DWORD    0x1"}},
		{"WindowsKeys", []string{`reg query "HKLM\SYSTEM\CurrentControlSet\Control\Keyboard Layout" /v "Scancode Map"`},
			[]string{"5BE000005CE0"}},
		{"DeviceEncryption", []string{
			`reg query "HKLM\SYSTEM\CurrentControlSet\Control\BitLocker" /v PreventDeviceEncryption`,
			`powershell -NoProfile -Command "$p = (New-Object -ComObject Shell.Application).NameSpace('C:').Self.ExtendedProperty('System.Volume.BitLockerProtection'); 'C: BitLocker protection: ' + $(switch ($p) { 1 {'on'} 2 {'off'} 3 {'encrypting'} 4 {'decrypting'} 5 {'suspended'} 6 {'on, locked'} default {'none (' + $p + ')'} })"`},
			[]string{"PreventDeviceEncryption    REG_DWORD    0x1"}},
		{"Hibernation", []string{`powercfg /a`}, []string{"Hibernation has not been enabled"}},
		{"NeverSleeps", []string{`powercfg /query SCHEME_CURRENT SUB_SLEEP STANDBYIDLE`, `powercfg /query SCHEME_CURRENT SUB_VIDEO VIDEOIDLE`},
			[]string{"Current AC Power Setting Index: 0x00000000"}},
		{"FileShare", []string{`net view \\localhost`, `netsh advfirewall firewall show rule name="irgo-winvm: SMB from the host"`},
			[]string{"irgo-drop", "LocalSubnet"}},
		{"WebView2", []string{`reg query "HKLM\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" /v pv /reg:32`},
			[]string{"pv    REG_SZ"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := strings.Join(strings.Fields(showInConsole(t, c.cmds...)), " ")
			var shows []string
			for _, w := range c.want {
				w = strings.Join(strings.Fields(w), " ")
				shows = append(shows, fmt.Sprintf("%q %t", w, strings.Contains(out, w)))
			}
			evidence(t, "shows %s", strings.Join(shows, ", "))
		})
	}
}
