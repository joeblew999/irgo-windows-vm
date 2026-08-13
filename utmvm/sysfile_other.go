//go:build !darwin

package utmvm

// Off macOS, none of this package's VM machinery can run — UTM is macOS-only —
// but the package must still COMPILE, because `irgo-winvm doctor` exists
// precisely to tell a developer on Windows or Linux what their machine can and
// cannot do. A tool that cannot build for the person it is trying to inform is
// no use to them.
//
// Everything here reports "unknown" rather than approximating. A wrong answer
// about whether two files share blocks, or whether one is protected, is worse
// than no answer: it would let a destructive operation proceed on the grounds
// that nothing said otherwise.

import (
	"errors"
	"os"
	"runtime"
)

// uchgFlag has no meaning here; it exists so shared code compiles.
const uchgFlag = 0

// inodeInfo cannot be answered portably. Reporting ok=false makes callers treat
// every file as potentially shared, which is the safe direction.
func inodeInfo(_ string) (ino uint64, nlink uint64, ok bool) { return 0, 0, false }

// diskUsage falls back to apparent size. Sparse files therefore over-report,
// which only affects the inventory's totals — and inventories on a machine that
// cannot host a VM are informational anyway.
func diskUsage(path string) (int64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

func fileFlags(_ string) (uint32, bool) { return 0, false }

func setFileFlags(_ string, _ uint32) error {
	return errors.New("utmvm: making a file immutable is macOS-only (host is " + runtime.GOOS + ")")
}

// statfsAvailable is unimplemented rather than approximated: the callers use it
// to refuse work that would run out of space, and a made-up number would let
// exactly that happen.
func statfsAvailable(_ string) (int64, error) {
	return 0, errors.New("utmvm: free-space reporting is macOS-only (host is " + runtime.GOOS + ")")
}

// sameDevice cannot be answered portably. False means "assume a copy is
// needed", which over-estimates the space required — the safe direction.
func sameDevice(_, _ string) bool { return false }

const immutableSupported = false
