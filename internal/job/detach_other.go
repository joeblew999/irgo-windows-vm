//go:build !unix

package job

import "os/exec"

// detach does nothing here.
//
// This file exists so the tool compiles for Windows and any other non-unix
// target, which `go:check` requires — not because jobs work there. The whole
// tool drives UTM, which is macOS-only; a Windows build exists to prove that
// what it reports about its own platform stays honest.
func detach(*exec.Cmd) {}
