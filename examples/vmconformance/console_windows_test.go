package vmconformance

import (
	"html"
	"image"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/native/screen"
)

// showInConsole runs cmds — cmd.exe command lines that only read — as dev,
// shows each one with what it printed in a window drawn like a console, and
// photographs that window; Cleanup closes it. It returns everything they
// printed.
//
// A glaze window rather than a real console: a classic console started with
// conhost.exe from the suite came up in about half the attempts on build
// 26100 (1 Oct 2026), while a window this process owns is found,
// photographed and closed every time. The commands are run here, exactly as
// typed, and the window shows their output unchanged.
func showInConsole(t *testing.T, cmds ...string) string {
	t.Helper()
	var all, page strings.Builder
	for _, c := range cmds {
		// The command line as typed: exec's own quoting writes \" for a quote,
		// which cmd.exe does not read, and reg query then reports a key that
		// is there as missing.
		x := exec.Command("cmd.exe")
		x.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd.exe /c " + c}
		out, err := x.CombinedOutput()
		text := strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
		if err != nil {
			text += "\n[" + err.Error() + "]"
		}
		all.WriteString("> " + c + "\n" + text + "\n\n")
		page.WriteString(`<div class="c">C:\&gt; ` + html.EscapeString(c) + "</div>" + html.EscapeString(text) + "\n\n")
	}
	drawn := make(chan struct{}, 1)
	w := openWindowSized(t, 790, 520, func(w glaze.WebView) {
		_ = w.Bind("drawn", func() {
			select {
			case drawn <- struct{}{}:
			default:
			}
		})
		w.SetHtml(`<!doctype html><html><body style="margin:0;background:#0c0c0c;color:#cccccc">` +
			`<pre style="font:13px Consolas,monospace;margin:10px;white-space:pre-wrap">` +
			`<style>.c{color:#f9f1a5}</style>` + page.String() + `</pre><script>drawn()</script></body></html>`)
	})
	select {
	case <-drawn:
	case <-time.After(30 * time.Second):
		t.Fatal("the window showing the commands' output did not draw within 30s")
	}
	shoot(t, func() (image.Image, error) { return screen.CaptureWindow(uint32(uintptr(w.Window()))) })
	return all.String()
}
