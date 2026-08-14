package utmvm

// Redirecting command output, which is what makes an MCP server possible.
//
// Over stdio, stdout is the JSON-RPC channel. Every command here announces its
// progress — deliberately, because fifty silent seconds and a hang look
// identical — so a tool handler that runs a command in-process would put that
// text in the middle of the protocol stream.

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestCaptureCollectsOutputInsteadOfPrinting is the property the server needs.
//
// It checks both halves: the text arrives in the returned string, and it does
// not arrive on the real stdout. The second half is the one that matters and
// the one a naive test omits — a capture that also printed would pass a
// contains-check and still corrupt every session.
//
// Negative control, run by hand: make Printer write with fmt.Printf again and
// this fails on the stdout half while still passing the contains-check, which
// is exactly the point of measuring both.
func TestCaptureCollectsOutputInsteadOfPrinting(t *testing.T) {
	// Swap the process's real stdout for a pipe, so "did anything reach the
	// terminal" is an answerable question rather than an assumption.
	realStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = realStdout }()

	// out was resolved to the old os.Stdout at package init, so point it at the
	// pipe too — otherwise this test proves nothing about the current default.
	outMu.Lock()
	prevOut := out
	out = w
	outMu.Unlock()
	defer func() { outMu.Lock(); out = prevOut; outMu.Unlock() }()

	got, err := Capture(func() error {
		say := Printer("test-capture")
		say("a line of progress")
		report := Reporter("test-report")
		report("a row of a table")
		return nil
	})
	if err != nil {
		t.Fatalf("Capture returned %v", err)
	}

	_ = w.Close()
	var leaked bytes.Buffer
	_, _ = leaked.ReadFrom(r)

	if !strings.Contains(got, "a line of progress") {
		t.Errorf("Printer output missing from the capture: %q", got)
	}
	if !strings.Contains(got, "a row of a table") {
		t.Errorf("Reporter output missing from the capture: %q", got)
	}
	if leaked.Len() != 0 {
		t.Errorf("%d bytes reached stdout during a capture: %q — over stdio this is "+
			"written into the JSON-RPC stream", leaked.Len(), leaked.String())
	}
}

// TestCaptureRestoresTheDestination covers the case that would be found weeks
// later: after a capture, printing must work again.
//
// Including after a panic. A tool handler that panics mid-command and left the
// destination pointing at a dead buffer would silence every command after it,
// on a server that stays up for days.
func TestCaptureRestoresTheDestination(t *testing.T) {
	var terminal bytes.Buffer
	outMu.Lock()
	prev := out
	out = &terminal
	outMu.Unlock()
	defer func() { outMu.Lock(); out = prev; outMu.Unlock() }()

	if _, err := Capture(func() error { Reporter("inner")("captured"); return nil }); err != nil {
		t.Fatal(err)
	}
	Reporter("after")("printed")
	if !strings.Contains(terminal.String(), "printed") {
		t.Errorf("after a capture, output no longer reaches the destination: %q", terminal.String())
	}
	if strings.Contains(terminal.String(), "captured") {
		t.Errorf("captured text leaked to the destination: %q", terminal.String())
	}

	func() {
		defer func() { _ = recover() }()
		_, _ = Capture(func() error { panic("a handler blew up") })
	}()
	Reporter("after-panic")("still printing")
	if !strings.Contains(terminal.String(), "still printing") {
		t.Error("a panic inside Capture left the destination redirected; every later command would be silent")
	}
}

// TestCaptureReturnsTheError — a command that fails must still report why, and
// the output it produced before failing is the most useful part of the answer.
func TestCaptureReturnsTheError(t *testing.T) {
	want := errors.New("the VM is not there")
	got, err := Capture(func() error { Reporter("failing")("got this far"); return want })
	if !errors.Is(err, want) {
		t.Errorf("Capture returned %v, want %v", err, want)
	}
	if !strings.Contains(got, "got this far") {
		t.Errorf("output produced before the failure was lost: %q", got)
	}
}

// TestConcurrentCapturesDoNotMix is why captureMu exists.
//
// Two captures at once and the second restores the first's buffer as "the
// previous destination", so one command's remaining output lands in another
// command's result — a wrong answer rather than a crash, delivered to an agent
// that has no way to tell.
//
// Run with -race, which go:check does.
func TestConcurrentCapturesDoNotMix(t *testing.T) {
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mine := strings.Repeat("x", i+1)
			got, _ := Capture(func() error { Reporter("concurrent")(mine); return nil })
			results[i] = strings.TrimSpace(got)
		}()
	}
	wg.Wait()
	for i, got := range results {
		if want := strings.Repeat("x", i+1); got != want {
			t.Errorf("capture %d got %q, want %q — output crossed between captures", i, got, want)
		}
	}
}

// TestCaptureStillWritesTheLog is the half of the design that is easy to lose.
//
// Redirecting output must not redirect the log. A run driven over MCP has to
// land in the same logs/ as a run driven from a terminal, or there are two
// histories and the log stops being the record of what this tool did.
//
// It would be very easy to "fix" the stdout problem by pointing everything at
// the buffer, pass every other test here, and silently stop logging the runs an
// agent starts — which are exactly the ones nobody watched.
//
// Negative control, run by hand: drop the log.Info call from Printer and this
// fails while every other test in this file still passes.
func TestCaptureStillWritesTheLog(t *testing.T) {
	// Swap the package logger for one writing to a buffer, so this does not
	// depend on — or write to — the real log under Application Support.
	var logged bytes.Buffer
	logOnce.Do(func() {}) // stop Logger() from opening the real file
	prev := logger
	logger = slog.New(slog.NewTextHandler(&logged, nil))
	defer func() { logger = prev }()

	var terminal bytes.Buffer
	outMu.Lock()
	prevOut := out
	out = &terminal
	outMu.Unlock()
	defer func() { outMu.Lock(); out = prevOut; outMu.Unlock() }()

	got, err := Capture(func() error {
		Printer("logged-command")("something happened")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "something happened") {
		t.Fatalf("the capture missed the line: %q", got)
	}
	if !strings.Contains(logged.String(), "something happened") {
		t.Errorf("the log did not receive the line during a capture: %q\n"+
			"an MCP-driven run would be missing from logs/", logged.String())
	}
	if !strings.Contains(logged.String(), "logged-command") {
		t.Errorf("the log lost the command name: %q", logged.String())
	}
}
