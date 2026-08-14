package utmvm

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logging, for humans watching and for whoever reads it afterwards.
//
// Both are needed and they want different things. A person waiting on a
// 45-minute install wants elapsed time and one line per thing; somebody working
// out why it failed yesterday wants a file, with timestamps, that nobody had to
// remember to capture.
//
// So every line goes to both: the terminal, prefixed with seconds since the
// command started, and a log file that keeps growing across runs.

const (
	// logDirName is where logs live, under the same root as everything else.
	logDirName = "logs"

	// logFileName is one file, appended to. Not one per run: the interesting
	// question is almost always "what happened before this", and hunting for
	// the previous file is friction at exactly the wrong moment.
	logFileName = "irgo-winvm.log"

	// logRotateBytes is when the file is moved aside. An unattended install
	// writes a few hundred lines, so this is months of use.
	logRotateBytes = 8 << 20
)

// LogDir is where logs are written.
func LogDir() string { return filepath.Join(appRoot(), logDirName) }

// LogPath is the log file itself.
func LogPath() string { return filepath.Join(LogDir(), logFileName) }

var (
	logOnce sync.Once
	logger  *slog.Logger
)

// Logger is the structured log, written to LogPath.
//
// Opened once, lazily, and never closed: the process exiting flushes it, and a
// command that fails early should still have written why.
func Logger() *slog.Logger {
	logOnce.Do(func() {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))

		if err := os.MkdirAll(LogDir(), 0o755); err != nil {
			return
		}
		rotateLog()
		f, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return // logging must never be the reason a command fails
		}
		logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return logger
}

// rotateLog moves the file aside once it is large, keeping exactly one previous
// generation. Two files is enough to answer "and what happened before that".
func rotateLog() {
	fi, err := os.Stat(LogPath())
	if err != nil || fi.Size() < logRotateBytes {
		return
	}
	_ = os.Rename(LogPath(), LogPath()+".1")
}

// Where a command's output goes.
//
// It went straight to os.Stdout, which is right for a terminal and wrong for
// every other caller. Over MCP, stdout *is* the JSON-RPC channel: a command
// announcing its progress — which this repository insists on, because fifty
// silent seconds and a hang look identical — lands in the middle of the
// protocol stream, and the client gets a parse error instead of a VM.
//
// So there is one destination, and it can be pointed somewhere else. Note what
// is deliberately *not* redirected: the log file. Printer and Reporter go on
// writing to logs/ whatever the destination is, so a run driven over MCP lands
// in the same log as a run driven from a terminal. Two histories and the log
// stops being the record.
var (
	outMu sync.Mutex
	out   io.Writer = os.Stdout
)

// Out is where command output goes. Write to it instead of os.Stdout.
//
// It resolves the destination on every write rather than capturing it, so a
// long-lived value — vm_create's install Log, held for 45 minutes — follows a
// redirect that happens after it was taken.
var Out io.Writer = redirectable{}

type redirectable struct{}

func (redirectable) Write(p []byte) (int, error) {
	outMu.Lock()
	defer outMu.Unlock()
	return out.Write(p)
}

// printf writes a line of command output to the current destination.
//
// The error is discarded, deliberately and exactly as fmt.Printf's always was —
// errcheck excludes Printf and not Fprintf, so switching destinations made four
// long-standing unchecked writes visible rather than creating them.
//
// Discarding is right here for the same reason the file already gives for the
// log: a terminal that has gone away is not a reason for iso-create to fail
// after forty minutes, and there is nowhere left to report it to.
func printf(format string, a ...any) { _, _ = fmt.Fprintf(Out, format, a...) }

// captureMu serializes captures. Two at once would have the second restore the
// first's buffer as "the previous destination", quietly sending the rest of one
// command's output into another command's result.
var captureMu sync.Mutex

// Capture runs fn with command output collected instead of printed, and returns
// what it printed.
//
// This is how an MCP tool handler gets a command's progress into its result:
// the text an operator would have read on the terminal is exactly what the
// agent needs, and it must not reach stdout.
//
// The log file is unaffected — see Out.
func Capture(fn func() error) (string, error) {
	captureMu.Lock()
	defer captureMu.Unlock()

	var buf bytes.Buffer
	outMu.Lock()
	prev := out
	out = &buf
	outMu.Unlock()

	// Restored even if fn panics. A panic in a tool handler that left the
	// destination pointing at a dead buffer would silence every later command.
	defer func() {
		outMu.Lock()
		out = prev
		outMu.Unlock()
	}()

	err := fn()
	return buf.String(), err
}

// Printer returns the line printer every command uses.
//
// It writes to the terminal with seconds elapsed since the command started, and
// the same text to the log file with a wall-clock timestamp and the command
// name — so a terminal line and a log line can be lined up afterwards.
func Printer(command string) func(string, ...any) {
	start := time.Now()
	log := Logger().With("cmd", command)
	log.Info("started")
	return func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		printf("[%6.1fs] %s\n", time.Since(start).Seconds(), msg)
		log.Info(msg, "elapsed", time.Since(start).Round(time.Millisecond).String())
	}
}

// Reporter is Printer for a command that prints a report rather than progress.
//
// Same log file, no elapsed-time prefix: a timestamp on every row of a table is
// noise, and doctor is a table. The log still gets every line, so "what did
// doctor say when it went wrong" is answerable afterwards.
func Reporter(command string) func(string, ...any) {
	log := Logger().With("cmd", command)
	log.Info("started")
	return func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		printf("%s\n", msg)
		log.Info(msg)
	}
}
