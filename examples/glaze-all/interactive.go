// Interactive mode: the same capabilities as the report, driven by hand.
//
// The report answers "does it work". This answers "what is it like" — the
// question a table cannot. A tray icon that is raised and torn down 2 seconds
// later is proof of an API call; one that stays up until you click Quit on its
// menu is the feature. Same for a file dialog you actually pick a file in, a
// menu bar whose items report back, a Dock icon that visibly changes, and a
// second copy of the program handing its arguments to the first.
//
// Everything is reached through glaze's own bridges rather than a side channel:
// buttons call Go through Bind, and Go pushes results back through the Events
// bridge. So this exercises those two on Windows as well, which is what
// glaze-probes tests in isolation.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/glaze/menu"
	"github.com/crgimenes/native/clipboard"
	"github.com/crgimenes/native/mmap"
	"github.com/crgimenes/native/nocapture"
	"github.com/crgimenes/native/openurl"
	"github.com/crgimenes/native/power"
	"github.com/crgimenes/native/singleinstance"
	"github.com/crgimenes/native/tray"
)

// instanceID is the single-instance lock's name. Interactive mode holds it for
// as long as the window is open, which is what makes the second-copy demo work.
const instanceID = "irgo-glaze-all-interactive"

type ui struct {
	w  glaze.WebView
	ev *glaze.Events

	mu       sync.Mutex
	powerTok *power.Token
	trayUp   bool
	menuBar  *menu.Menu
	iconHue  int
}

func runInteractive() {
	// Acquire before the window, so a second copy hands its arguments to the
	// first and exits rather than opening a window nobody asked for. That IS
	// the capability: a single-instance app is one that redirects a second
	// launch, not one that merely refuses it.
	u := &ui{}
	inst, err := singleinstance.Acquire(instanceID, singleinstance.Options{
		OnMessage: func(args []string) {
			u.logf("singleinstance", "a second copy launched and sent: %v", args)
		},
	})
	if err != nil {
		if sendErr := singleinstance.Send(instanceID, os.Args[1:]); sendErr == nil {
			fmt.Println("already running — sent this launch's arguments to the first copy and exited.")
			fmt.Println("look at its window: the message is in the log.")
			return
		}
		// Not "already running", or the primary is not listening. Carry on
		// without the lock rather than refusing to start; every other
		// capability is still worth having.
		fmt.Fprintf(os.Stderr, "singleinstance unavailable (%v) — continuing without it\n", err)
	}
	if inst != nil {
		defer inst.Release()
	}

	w, err := glaze.New(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "glaze.New:", err)
		os.Exit(1)
	}
	defer w.Destroy()
	u.w = w

	// Before Run, per the Events doc: NewEvents installs the bridge with
	// Init+Bind, and Init only affects pages loaded after it.
	u.ev, err = glaze.NewEvents(w)
	if err != nil {
		fmt.Fprintln(os.Stderr, "events bridge:", err)
		os.Exit(1)
	}
	u.bind()

	w.SetTitle("irgo — every native capability, by hand")
	w.SetSize(940, 760, glaze.HintNone)
	w.SetHtml(interactiveHTML)

	go func() {
		time.Sleep(600 * time.Millisecond)
		u.logf("ready", "%s/%s — click anything. Nothing here is destructive.", runtime.GOOS, runtime.GOARCH)
		if inst == nil {
			u.logf("singleinstance", "NOT held — the second-copy demo will not work this run")
		}
	}()

	w.Run()
}

// logf pushes a line into the page's log, and to stdout as well.
//
// Both, not either: in the VM this is launched by `irgo-winvm app-create -gui`, which
// hands back the process's output and nothing else. Without the stdout copy,
// everything asynchronous — a tray click, a menu choice, a second copy's
// arguments — would be visible only to whoever is looking at the Windows
// screen, which on a headless run is nobody.
//
// Safe from any goroutine: Emit re-enters the UI thread itself.
func (u *ui) logf(topic, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("[%s] %s\n", topic, msg)
	if u.ev != nil {
		_ = u.ev.Emit("log", topic, msg)
	}
}

// say formats a value for the button's own result line. Bind callbacks return
// to JavaScript, so an error is surfaced as a rejected promise; returning the
// error is enough and the page prints it.
func say(format string, args ...any) (string, error) { return fmt.Sprintf(format, args...), nil }

func (u *ui) bind() {
	// Every one of these runs on its own goroutine (glaze dispatches bindings
	// with `go func()`), which is exactly what the file dialogs require — they
	// block until dismissed and deadlock if called on the UI thread. The ones
	// that touch AppKit or Win32 directly go the other way, through onMain.
	must := func(name string, fn any) {
		if err := u.w.Bind(name, fn); err != nil {
			fmt.Fprintf(os.Stderr, "bind %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	// --- clipboard ---
	must("clipWrite", func(text string) (string, error) {
		if err := clipboard.WriteText(text); err != nil {
			return "", err
		}
		return say("wrote %d bytes — paste somewhere to check", len(text))
	})
	must("clipRead", func() (string, error) {
		got, err := clipboard.ReadText()
		if err != nil {
			return "", err
		}
		if got == "" {
			return "(the clipboard is empty)", nil
		}
		return got, nil
	})

	// --- power ---
	must("powerHold", func(reason string) (string, error) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.powerTok != nil {
			return "already holding — release it first", nil
		}
		tok, err := power.PreventSleep(reason)
		if err != nil {
			return "", err
		}
		u.powerTok = tok
		return say("held: %q. The display can still sleep; the SYSTEM will not.", reason)
	})
	must("powerRelease", func() (string, error) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.powerTok == nil {
			return "not holding anything", nil
		}
		err := u.powerTok.Release()
		u.powerTok = nil
		if err != nil {
			return "", err
		}
		return "released — the machine may sleep again", nil
	})

	// --- mmap ---
	must("mmapDemo", func(text string) (string, error) {
		f, err := os.CreateTemp("", "irgo-mmap-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if text == "" {
			text = "mapped memory"
		}
		if _, err := f.WriteString(text); err != nil {
			return "", err
		}
		m, err := mmap.Map(f)
		if err != nil {
			return "", err
		}
		defer m.Unmap()
		// Upper-case it through the mapping, then read the FILE back: that is
		// the whole point of mmap, and the only way to see it is to go around
		// the mapping to the bytes on disk.
		for i := range m {
			if m[i] >= 'a' && m[i] <= 'z' {
				m[i] -= 32
			}
		}
		onDisk, err := os.ReadFile(f.Name())
		if err != nil {
			return "", err
		}
		return say("wrote %q, upper-cased it through the mapping, and the file now reads %q", text, string(onDisk))
	})

	// --- openurl ---
	must("openURL", func(raw string) (string, error) {
		if err := openurl.Open(raw); err != nil {
			return "", err
		}
		return say("handed %q to the default handler", raw)
	})
	must("revealPath", func(p string) (string, error) {
		if p == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			p = home
		}
		if err := openurl.Reveal(p); err != nil {
			return "", err
		}
		return say("revealed %s in the file manager", p)
	})

	// --- tray ---
	// Raised and left up, unlike the report's two seconds. Run blocks driving
	// the event loop, so it is posted to the UI thread and never waited on;
	// Stop is safe from any goroutine. The UI stays responsive meanwhile
	// because the tray's loop drains the same queue glaze dispatches through.
	must("trayShow", func() (string, error) {
		u.mu.Lock()
		if u.trayUp {
			u.mu.Unlock()
			return "already up", nil
		}
		u.trayUp = true
		u.mu.Unlock()

		u.w.Dispatch(func() {
			err := tray.Run(tray.Config{
				Title:   "irgo",
				Tooltip: "irgo — click me",
				Icon:    coloredIcon(210),
				Items: []tray.Item{
					{Title: "Say hello", OnClick: func() {
						u.logf("tray", "you clicked \"Say hello\" — this ran on the UI thread")
					}},
					{Title: "Read the clipboard", OnClick: func() {
						// The click handler runs on the UI thread, so hand the
						// work off rather than blocking the tray's own loop.
						go func() {
							got, err := clipboard.ReadText()
							if err != nil {
								u.logf("tray", "clipboard read failed: %v", err)
								return
							}
							u.logf("tray", "the clipboard says: %q", got)
						}()
					}},
					{Separator: true},
					{Title: "Disabled on purpose", Disabled: true},
					{Separator: true},
					{Title: "Hide the tray", OnClick: tray.Stop},
				},
			})
			u.mu.Lock()
			u.trayUp = false
			u.mu.Unlock()
			if err != nil {
				u.logf("tray", "Run returned: %v", err)
				return
			}
			u.logf("tray", "taken down")
		})
		return "raised — look in the menu bar / notification area and click it", nil
	})
	must("trayHide", func() (string, error) {
		tray.Stop()
		return "asked it to stop", nil
	})

	// --- menu bar ---
	must("menuInstall", func() (string, error) {
		items := []menu.Item{
			{Title: "Example", Submenu: []menu.Item{
				{Title: "Say hello", Shortcut: "cmd+h", OnClick: func() {
					u.logf("menu", "you chose Example → Say hello")
				}},
				{Separator: true},
				{Title: "Greyed out", Disabled: true},
			}},
			{Title: "Edit", Submenu: []menu.Item{
				// Selector items wire straight to the responder chain on macOS,
				// which is the only way Cmd+C/V reach the web view's editing
				// commands. Skipped on Windows, where the focused control
				// already handles them.
				{Title: "Cut", Shortcut: "cmd+x", Selector: "cut:"},
				{Title: "Copy", Shortcut: "cmd+c", Selector: "copy:"},
				{Title: "Paste", Shortcut: "cmd+v", Selector: "paste:"},
				{Separator: true},
				{Title: "Select All", Shortcut: "cmd+a", Selector: "selectAll:"},
			}},
		}
		// Window is required on Windows (the HWND) and ignored on macOS.
		// Dispatch is required here and only here: Set blocks until its UI work
		// has run, and this is a background goroutine while the loop is going.
		m, err := menu.Set(items, menu.Options{Window: u.w.Window(), Dispatch: u.w.Dispatch})
		if err != nil {
			return "", err
		}
		u.mu.Lock()
		old := u.menuBar
		u.menuBar = m
		u.mu.Unlock()
		if old != nil {
			old.Release()
		}
		return "installed — try Example → Say hello, and the Edit shortcuts in the text boxes", nil
	})
	must("menuRemove", func() (string, error) {
		u.mu.Lock()
		m := u.menuBar
		u.menuBar = nil
		u.mu.Unlock()
		if m == nil {
			return "no menu installed", nil
		}
		m.Release()
		return "released — the previous menu is back", nil
	})

	// --- app icon ---
	must("iconSet", func() (string, error) {
		u.mu.Lock()
		u.iconHue = (u.iconHue + 60) % 360
		hue := u.iconHue
		u.mu.Unlock()

		var err error
		onMain(u.w, func() { err = glaze.SetAppIcon(coloredIcon(hue)) })
		if err != nil {
			return "", err
		}
		return say("set a %d° icon — look at the Dock", hue)
	})

	// --- no-capture ---
	must("noCapture", func() (string, error) {
		var err error
		onMain(u.w, func() { err = nocapture.Protect(u.w.Window()) })
		if err != nil {
			return "", err
		}
		return "protected — take a screenshot: this window should be black in it. There is no way to undo it.", nil
	})

	// --- file dialogs ---
	// All four, where the report only presents one. These BLOCK until
	// dismissed, which is fine and required here: a binding runs on its own
	// goroutine, and the run loop keeps going.
	must("dlgOpen", func() (string, error) {
		p, err := u.w.OpenFile(glaze.FileDialogOptions{
			Title:   "Pick any file",
			Filters: []glaze.FileFilter{{Name: "Text", Extensions: []string{"txt", "md", "go"}}, {Name: "All", Extensions: []string{"*"}}},
		})
		if err != nil {
			return "", err
		}
		return chosen(p), nil
	})
	must("dlgOpenMulti", func() (string, error) {
		ps, err := u.w.OpenFiles(glaze.FileDialogOptions{Title: "Pick several files"})
		if err != nil {
			return "", err
		}
		if len(ps) == 0 {
			return "cancelled — which is not an error", nil
		}
		return fmt.Sprintf("%d chosen:\n%s", len(ps), strings.Join(ps, "\n")), nil
	})
	must("dlgSave", func() (string, error) {
		p, err := u.w.SaveFile(glaze.FileDialogOptions{Title: "Where would you save it?", Filename: "irgo-example.txt"})
		if err != nil {
			return "", err
		}
		// Deliberately not written to. The dialog returns a path; creating the
		// file is the application's job, and this one has no business leaving
		// files behind.
		return chosen(p) + " (nothing was written)", nil
	})
	must("dlgDir", func() (string, error) {
		p, err := u.w.OpenDirectory(glaze.FileDialogOptions{Title: "Pick a folder"})
		if err != nil {
			return "", err
		}
		return chosen(p), nil
	})

	// --- single instance ---
	must("instanceHint", func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			exe = "glaze-all"
		}
		return say("run this in another terminal and watch the log here:\n%s -i hello from a second copy", exe)
	})

	// The report, on demand, in the window it is describing.
	must("runReport", func() (string, error) {
		mu.Lock()
		results = nil
		mu.Unlock()

		// The windowed set only. The clipboard, power and mmap buttons above
		// still exercise those by hand; the automated four live in probe/.
		probeOpenURL()
		probeAppIcon(u.w)
		probeNoCapture(u.w)
		probeMenu(u.w)
		probeFileDialog(u.w)

		mu.Lock()
		defer mu.Unlock()
		var b strings.Builder
		for _, r := range results {
			fmt.Fprintf(&b, "%-24s %-12s %s\n", r.name, r.status, r.detail)
		}
		// singleinstance and tray are left out on purpose: the lock is already
		// held by this process, and the tray is yours to raise and dismiss.
		b.WriteString("\n(singleinstance and tray excluded — both are in use interactively)")
		return b.String(), nil
	})

	// A JS-side emit reaching Go, which is the half of the Events bridge the
	// buttons above do not exercise.
	u.ev.On("ui:ping", func(args ...json.RawMessage) {
		var from string
		if len(args) > 0 {
			_ = json.Unmarshal(args[0], &from)
		}
		u.logf("events", "the page emitted ui:ping from %q — round trip through Go", from)
	})
}

func chosen(p string) string {
	if p == "" {
		return "cancelled — which is not an error"
	}
	return p
}

// coloredIcon draws a filled square in the given hue, so a tray icon is visible
// and a Dock icon visibly changes. The report uses a 1x1 transparent pixel,
// which proves the bytes are decoded and shows you nothing.
func coloredIcon(hue int) []byte {
	const size = 128
	r, g, b := hsv(float64(hue), 0.75, 0.95)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			// Round the corners a little; a square blob reads as a bug.
			dx, dy := float64(x)-size/2, float64(y)-size/2
			if abs(dx) > size/2-10 && abs(dy) > size/2-10 &&
				(abs(dx)-(size/2-10))*(abs(dx)-(size/2-10))+(abs(dy)-(size/2-10))*(abs(dy)-(size/2-10)) > 100 {
				continue
			}
			img.Set(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return tinyPNG
	}
	return buf.Bytes()
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func hsv(h, s, v float64) (uint8, uint8, uint8) {
	c := v * s
	x := c * (1 - abs(float64(int(h/60)%2)+h/60-float64(int(h/60))-1))
	m := v - c
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255)
}
