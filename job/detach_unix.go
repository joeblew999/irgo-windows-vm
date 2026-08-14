//go:build unix

package job

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own process group.
//
// Without it the job dies with whatever started it: an MCP client closing the
// connection kills the server, and the 45-minute install goes with it — which
// is the entire failure this package prevents.
//
// Split by platform because SysProcAttr is a different struct on every one, and
// Setpgid does not exist on Windows. `go:check` cross-compiles for Windows and
// caught this; nothing on a Mac would have.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
