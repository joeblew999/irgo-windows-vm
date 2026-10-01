// Package drive drives a glaze app from a test the way Playwright drives a web
// page: find an element, click it, type, scroll, wait for the page to change,
// take a picture. The difference is that the input is real: every click, key
// and scroll is an OS event delivered to the app's process, so it goes through
// the window server, AppKit or Win32, the web view's hit-testing and focus,
// exactly as a user's would. Nothing is dispatched with a JavaScript click().
//
// # Two processes
//
// The app runs as a separate process, started by Launch. It calls Serve, which
// opens a glaze window that never activates (on macOS the app has the
// Prohibited activation policy and its window is ordered behind every other
// window, so whoever is using the machine keeps their frontmost app and their
// focus), installs a small bridge in the page, and writes one JSON object per
// line on stdout:
//
//	{"type":"start","pid":123,"window":456,"content":{"X":0,"Y":28}}
//	{"type":"ready"}
//	{"type":"click","target":"button#inc","x":60,"y":40,"button":0,"trusted":true}
//	{"type":"reply","id":3,"value":"count: 1"}
//
// The test reads that stream as a Session. Input goes to the pid through
// native/input's Target, captures to the window ID through native/screen, and
// questions about the page go to the app's stdin and come back as replies.
//
// Not in-process: a window opened by the test binary itself belongs to an app
// that glaze has made active (a test binary is not a bundle, so glaze sets the
// Regular policy and activates it), and a click there could bring it forward
// over the user's work. A separate process with its own policy cannot.
//
// # What is real input and what is the bridge
//
// Real OS input, through native/input (CGEventPostToPid on macOS):
// Click, ClickAt, Type, Press and Scroll. The page sees these as events with
// isTrusted true, which is how a test proves they were not synthesized in
// JavaScript; every event in the stream carries that flag.
//
// Bridge-assisted, through JavaScript in the page: locating an element (its
// bounding rectangle, and whether something else covers its centre), Eval,
// and every WaitFor. The bridge reads the page; it never changes it.
//
// Screenshot is native/screen's CaptureWindow (ScreenCaptureKit on macOS): the
// window's own pixels, even while it is behind other windows.
//
// # Permissions
//
// On macOS input needs the Accessibility permission and capture needs Screen
// Recording, both granted to the terminal or runner that starts the test.
// native returns input.ErrNotTrusted and screen.ErrNotAllowed without them,
// and never prompts.
package drive
