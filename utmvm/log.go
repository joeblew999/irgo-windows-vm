package utmvm

import (
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
		fmt.Printf("[%6.1fs] %s\n", time.Since(start).Seconds(), msg)
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
		fmt.Println(msg)
		log.Info(msg)
	}
}
