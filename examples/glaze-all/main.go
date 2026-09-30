// Command glaze-all is a runnable example that exercises every capability a
// desktop app needs a WINDOW for, in one program.
//
// It exists because no such program existed anywhere: not in this repo, not in
// glaze's examples, and not in crgimenes/native, all of which test one
// capability per binary. So nobody had ever seen the windowed surface run
// together on Windows — which is exactly the thing a desktop app depends on.
//
// It covers:
//
//   - crgimenes/native — open-url, tray, no-capture
//   - glaze's own      — native file dialogs, the menu bar, and the app icon
//
// Everything here needs a desktop session and a run loop, which is why this is
// a glaze app rather than a console tool, and why it must be launched with
// `irgo-winvm app-create -gui`. Under the QEMU guest agent alone it runs as
// SYSTEM in session 0, where there is no window station and every windowed call
// fails.
//
// The headless half — clipboard, power, single-instance, mmap — is examples/probe/, and
// is not repeated here. It was, once, and the copy is the reason this file is
// named for the window rather than for native: a program called `nativeall`
// that duplicated four of native's capabilities and added glaze on top told you
// nothing about which of the two it was really testing.
//
// Run it with no flags and it opens the window and waits: every capability is a
// button, and you drive them by hand. That is the default because it is an
// example first — a tray icon you can click is the capability, and a table
// saying the call returned nil is the summary of it.
//
// `-probe` is the other half: it runs each one unattended, prints a status line
// per capability, and exits non-zero if any FAILED. That is what
// `irgo-winvm app-create -gui ... -probe` runs in the VM.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/glaze/menu"
	"github.com/crgimenes/native/nocapture"
	"github.com/crgimenes/native/openurl"
	"github.com/crgimenes/native/tray"
)

type result struct {
	name, status, detail string
}

var (
	mu      sync.Mutex
	results []result
)

// unsupportedErrs are the "no backend on this platform" sentinels.
//
// STANDING IN FOR AN UPSTREAM FIX — see docs/UPSTREAM.md §2. Delete this list, and
// isUnsupported with it, once the wrapping lands in a released glaze and
// native; the whole thing collapses back to one errors.Is against the standard
// sentinel.
//
// Every package defines its own instead of wrapping the standard
// errors.ErrUnsupported, so a check against that one alone matches none of
// them — and a platform behaving exactly as documented is reported as a
// failure. That is not academic here: glaze.SetAppIcon is unsupported on
// Windows by design, which is the platform this example exists to test, and
// nocapture is unsupported on macOS. Both would fail a wholly correct run and
// send a non-zero exit code back to whoever ran it unattended.
//
// errors.ErrUnsupported stays first: it is what this list becomes.
//
// Only the packages this program reports on: clipboard, power, singleinstance
// and mmap are examples/probe/'s to answer for, and their sentinels belong in examples/probe/'s
// copy of this list, not here.
var unsupportedErrs = []error{
	errors.ErrUnsupported,
	openurl.ErrUnsupported,
	nocapture.ErrUnsupported,
	tray.ErrUnsupported,
	menu.ErrUnsupported,
	glaze.ErrIconUnsupported,
}

func isUnsupported(err error) bool {
	for _, sentinel := range unsupportedErrs {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

func record(name string, err error, detail string) {
	mu.Lock()
	defer mu.Unlock()
	switch {
	case err == nil:
		results = append(results, result{name, "OK", detail})
	case isUnsupported(err):
		// A package reporting its own ErrUnsupported is behaving correctly on a
		// platform it does not cover; that is not a failure of this run.
		results = append(results, result{name, "UNSUPPORTED", err.Error()})
	default:
		results = append(results, result{name, "FAILED", err.Error()})
	}
}

// onMain runs fn on the UI thread and waits for it to finish.
//
// Every windowed capability below has to touch AppKit or Win32 from the thread
// that owns the window, and the probes run on a background goroutine. Dispatch
// posts and returns immediately, so waiting is the point: each caller reads a
// variable fn assigned, and without the wait it reads it before fn has run.
func onMain(w glaze.WebView, fn func()) {
	done := make(chan struct{})
	w.Dispatch(func() {
		defer close(done)
		fn()
	})
	<-done
}

// A 1x1 transparent PNG. Enough to prove the icon paths accept and decode an
// image without shipping an asset.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0d, 0x0a, 0x2d, 0xb4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// --- headless capabilities: no window needed -------------------------------
//
// Only openurl is here, and only because it is the one headless call that still
// wants a desktop session — it hands a URL to the shell, which in session 0 has
// nothing to hand it to.
//
// clipboard, power, single-instance and mmap used to be probed here too, with
// the same canary, the same "second acquire must fail", the same temp-file
// write-through as examples/probe/. Two programs asserting the same four things is two
// places to update when native changes and two reports to reconcile when they
// disagree. examples/probe/ owns them now: it is headless all the way down, so it runs
// under the guest agent with no window at all, which is the strictest place
// they can be checked.
//
// They still appear in interactive mode, where a human drives them through the window. That
// is not the same test: interactive mode exercises the second-instance handoff
// (singleinstance.Send) and clipboard interop with the host, neither of which a
// console probe can reach.

// fileURL turns an absolute path into a file:// URL that Open accepts on both
// platforms.
//
// The Windows shape is the reason this is not string concatenation: a path is
// `C:\dir`, and a URL wants `file:///C:/dir` — forward slashes, and a leading
// slash before the drive letter. Without that leading slash net/url writes
// `file:C:/dir` (no authority component, since the path does not start with
// "/"), which ShellExecuteW rejects.
func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func probeOpenURL() {
	// Actually called, unlike the old headless probe which only checked that
	// the symbol was non-nil — a function value that is never nil, so it
	// checked nothing at all and `go vet` said so.
	//
	// A file:// URL is what makes calling it honest: it goes through the real
	// backend — NSWorkspace on macOS, ShellExecuteW on Windows — without
	// launching a browser or touching the network. A directory is used rather
	// than a file so the handler is the file manager on both platforms.
	//
	// Each window it opens is closed again before the next step, and checked
	// gone (cleanup.go): this runs unattended on the owner's own Mac, and
	// every run used to leave windows there. The directory is removed only
	// after that — removing it while Explorer was still navigating to it is
	// what put a "Location is not available" box on the VM's desktop.
	dir, err := os.MkdirTemp("", "irgo-openurl-*")
	if err != nil {
		record("openurl.Open", err, "")
		record("openurl.Reveal", err, "")
		return
	}
	defer os.RemoveAll(dir)

	recordOpened("openurl.Open", "opened a file:// URL in the file manager",
		func() error { return openurl.Open(fileURL(dir)) }, dir, dir)

	// The package's stated safety boundary: Open hands only http, https, mailto
	// and file URLs to the OS, so a hostile string cannot reach an arbitrary
	// protocol handler. Worth asserting rather than trusting, and it costs
	// nothing — a refusal has no side effect. A silent acceptance here is a
	// vulnerability, so it is recorded as a failure, not as an unsupported.
	switch err := openurl.Open("ms-settings:privacy"); {
	case errors.Is(err, openurl.ErrScheme):
		record("openurl.Open/refused", nil, "custom protocol scheme correctly refused")
	case err == nil:
		record("openurl.Open/refused", errors.New("a custom protocol scheme was NOT refused"), "")
	default:
		record("openurl.Open/refused", fmt.Errorf("refused, but not with ErrScheme: %w", err), "")
	}

	// Reveal shows the directory selected in its parent folder, so that is
	// the window it opens.
	recordOpened("openurl.Reveal", "revealed a directory in the file manager",
		func() error { return openurl.Reveal(dir) }, dir, filepath.Dir(dir), dir)
}

// recordOpened runs a call that opens a file-manager window, closes what it
// opened, and records one row for both: the capability is only OK if the call
// worked AND nothing was left on the screen.
func recordOpened(name, detail string, open func() error, target string, places ...string) {
	openErr, did, cleanErr := openAndClose(open, target, places...)
	switch {
	case openErr != nil:
		record(name, openErr, "")
	case cleanErr != nil:
		record(name, fmt.Errorf("window left behind: %w", cleanErr), "")
	case did.reused:
		record(name, nil, detail+"; shown in a window already open, so none to close")
	default:
		record(name, nil, fmt.Sprintf("%s; its window closed again (%d)", detail, did.closed))
	}
}

// --- windowed capabilities: need a desktop session -------------------------

// trayVisibleFor is how long the icon stays up. Long enough to appear and be
// caught in a screenshot, short enough that an unattended run is not held up.
const trayVisibleFor = 2 * time.Second

// probeTray raises the tray icon and takes it down again.
//
// Two things here are not interchangeable with the obvious alternative, and
// both cost a hang when got wrong.
//
// It is posted to the UI thread and NOT waited on. tray.Run blocks, driving the
// OS event loop until Stop is called, so onMain would sit on it forever. Stop
// is documented safe from any goroutine, which is what unwinds it.
//
// And it happens AFTER the window, not before. On macOS glaze's New runs a
// temporary [NSApp run] that ends only when applicationDidFinishLaunching
// fires — once per process. A tray started first consumes that, and glaze.New
// then blocks forever with no window and no error to say why. That is an
// upstream bug, fixed in glaze rather than ordered around here (docs/UPSTREAM.md
// §1); this order is what a released glaze still requires.
func probeTray(w glaze.WebView) {
	done := make(chan error, 1)
	w.Dispatch(func() {
		done <- tray.Run(tray.Config{
			Title:   "irgo",
			Tooltip: "irgo native example",
			Icon:    tinyPNG,
			Items: []tray.Item{
				{Title: "Example running"},
				{Separator: true},
				{Title: "Quit", OnClick: tray.Stop},
			},
		})
	})

	select {
	case err := <-done:
		// Returned without being asked to: no backend on this platform, or a
		// tray is already up.
		record("tray.Run", err, "")
		return
	case <-time.After(trayVisibleFor):
	}

	tray.Stop()
	select {
	case err := <-done:
		record("tray.Run", err, "icon raised and removed")
	case <-time.After(5 * time.Second):
		record("tray.Run", errors.New("Stop did not unwind Run within 5s"), "")
	}
}

func probeMenu(w glaze.WebView) {
	// Window is required on Windows — Set returns an error naming it if the
	// HWND is missing — and ignored on macOS, where the menu bar is global.
	//
	// Dispatch is passed because this runs on a background goroutine while the
	// run loop is already going, which is the one case the package says to pass
	// it: Set blocks until its UI work has run, and without a dispatcher that
	// work would be attempted off the UI thread. Passing it BEFORE Run starts
	// would be the hang the package doc warns about; here the loop is draining.
	_, err := menu.Set([]menu.Item{
		{Title: "Example", Submenu: []menu.Item{
			{Title: "About"},
			{Separator: true},
			{Title: "Quit", Shortcut: "cmd+q"},
		}},
	}, menu.Options{Window: w.Window(), Dispatch: w.Dispatch})
	record("menu.Set", err, "native menu bar installed")
}

func probeNoCapture(w glaze.WebView) {
	var err error
	onMain(w, func() { err = nocapture.Protect(w.Window()) })
	record("nocapture.Protect", err, "window excluded from screen capture")
}

func probeAppIcon(w glaze.WebView) {
	var err error
	onMain(w, func() { err = glaze.SetAppIcon(tinyPNG) })
	record("glaze.SetAppIcon", err, "")
}

// fileDialogTitle is also how the dialog is found again on Windows to close it.
const fileDialogTitle = "irgo probe (will close itself)"

func probeFileDialog(w glaze.WebView) {
	// Dialogs are modal and block until dismissed, so this cannot wait for a
	// result without a human. What is worth testing is whether presenting one
	// crashes or errors — the COM and WinRT plumbing behind it is the fragile
	// part, not the user's eventual click.
	//
	// Then it is dismissed, the way a user cancelling it would, and OpenFile
	// must return. It used to be "left open, process exits": the process took
	// the dialog with it, but it was a window on the owner's screen until
	// then, and whether the panel really went away was never checked.
	done := make(chan error, 1)
	go func() {
		_, err := w.OpenFile(glaze.FileDialogOptions{Title: fileDialogTitle})
		done <- err
	}()
	select {
	case err := <-done:
		// Dismissed or refused straight away; a cancelled dialog is not an error.
		record("glaze.OpenFile", err, "dialog presented and returned")
		return
	case <-time.After(3 * time.Second):
		// Still open means it was presented successfully, which is the result
		// we are after. Now close it.
	}
	if err := dismissFileDialog(w, fileDialogTitle); err != nil {
		record("glaze.OpenFile", fmt.Errorf("presented, then could not be dismissed: %w", err), "")
		return
	}
	select {
	case err := <-done:
		record("glaze.OpenFile", err, "dialog presented, then dismissed as cancelled")
	case <-time.After(5 * time.Second):
		record("glaze.OpenFile", errors.New("presented and asked to close, but OpenFile had not returned 5s later: the dialog is still up"), "")
	}
}

func report() int {
	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("\nwindowed capability probe — %s/%s\n(the headless four are examples/probe/)\n\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("%-24s %-12s %s\n", "CAPABILITY", "STATUS", "DETAIL")
	fmt.Println(strings.Repeat("-", 78))
	failed := 0
	for _, r := range results {
		fmt.Printf("%-24s %-12s %s\n", r.name, r.status, r.detail)
		if r.status == "FAILED" {
			failed++
		}
	}
	fmt.Println()
	if failed == 0 {
		fmt.Println("all capabilities OK or cleanly unsupported")
		return 0
	}
	fmt.Printf("%d capability/capabilities FAILED\n", failed)
	return 1
}

func main() {
	// Interactive is the default, because this is an example before it is a
	// test. Someone who builds it and runs it wants to see a tray icon they can
	// click and a file dialog they can pick a file in — that is what the
	// capabilities ARE. A table saying they worked is the summary of the thing,
	// not the thing.
	//
	// It was the other way round, and the default suited the automated caller
	// rather than the person: running the example printed a report and exited
	// before you could look at it, and the interesting half was behind a flag
	// nobody discovers by running it.
	//
	// So the report is the flag now. `irgo-winvm app-create -gui` passes it,
	// which is right — an unattended run wants an exit code and no windows left
	// open, and it should have to say so.
	report := flag.Bool("probe", false, "run the capability report and exit, instead of opening the window")
	flag.Parse()

	if *report {
		runReport()
		return
	}
	runInteractive()
}

func runReport() {
	// glaze.New first, before anything else on the main goroutine. openurl
	// used to run here ahead of it, so that a windowed half that died still
	// left a partial report; now that it waits for the file manager's windows
	// and closes them (seconds, and several osascript runs), the main
	// goroutine was moved to another OS thread in the meantime, and New then
	// ran AppKit off the main thread: SIGTRAP inside [NSApp run], twice in
	// fourteen runs on 30 Sep 2026, the stack showing goroutine 1 on m=4.
	// glaze pins the thread when New is called rather than in an init, so
	// whatever runs first decides. That is glaze's to fix (docs/UPSTREAM.md
	// §4); this order is what a released glaze requires.
	w, err := glaze.New(false)
	if err != nil {
		record("glaze.New", err, "no window: the rest cannot be tested")
		probeOpenURL()
		os.Exit(report())
	}
	w.SetTitle("irgo windowed probe")
	w.SetSize(520, 320, glaze.HintNone)
	w.SetHtml(`<!doctype html><meta charset="utf-8">
<body style="font:14px system-ui;padding:2rem">
<h2>irgo windowed probe</h2><p>Exercising every capability that needs a window, then exiting.</p>
<p style="color:#666">Run with no flags to try them by hand instead.</p></body>`)

	code := make(chan int, 1)
	go func() {
		// Give the window a moment to exist before touching anything that needs
		// a handle.
		time.Sleep(2 * time.Second)
		// openurl needs no window, only the desktop session; it is here
		// because of the ordering above, not because it touches w.
		probeOpenURL()
		probeAppIcon(w)
		probeNoCapture(w)
		probeMenu(w)
		probeTray(w)
		probeFileDialog(w)
		code <- report()
		w.Terminate()
	}()

	w.Run()
	// The exit code is taken from the report here, on the main goroutine,
	// after Run has returned. It used to be os.Exit(code) in the goroutine
	// above, half a second after Terminate — but Terminate makes Run return,
	// runReport returned, main returned, and the process exited 0 before that
	// half second was up. Measured 30 Sep 2026: "1 capability/capabilities
	// FAILED", exit status 0. glaze-check reads the exit status, so a FAILED
	// row on the Mac was recorded as PASS.
	var c int
	select {
	case c = <-code:
	case <-time.After(30 * time.Second):
		// Run returned without being asked to (the window was closed), and the
		// probes, which need the loop, cannot finish.
		fmt.Println("the run loop ended before the probes finished")
		c = 1
	}
	w.Destroy()
	os.Exit(c)
}
