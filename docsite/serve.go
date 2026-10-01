package docsite

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Serve serves s.Out on localhost until it fails. Call it straight after
// Build, so what is served is what was just built: a separate server pointed
// at a stale directory shows a site that no longer matches the markdown.
func Serve(s *Site, port int, log io.Writer) error {
	freePort(port, log)
	addr := fmt.Sprintf("localhost:%d", port)
	_, _ = fmt.Fprintf(log, "\n  http://%s  — ctrl-c to stop\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.FileServer(http.Dir(s.Out)),
		ReadHeaderTimeout: 5 * time.Second, // a bare ListenAndServe has no timeouts at all
	}
	return srv.ListenAndServe()
}

// freePort stops whatever is already listening on the port, and says what it
// stopped.
//
// A previous run left in the background is the normal case, and then the new
// server dies with "address already in use" while the old build goes on
// answering: the site looks stale rather than broken, and the fix is
// invisible. It names the process before stopping it, because one day it will
// match something that is not a previous copy of this server.
//
// Never fatal: without lsof, or if the kill is refused, ListenAndServe
// reports the real problem a moment later.
func freePort(port int, log io.Writer) {
	out, err := exec.Command("lsof", "-nP", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return // nothing listening, or no lsof
	}
	for _, pid := range strings.Fields(string(out)) {
		name := "?"
		if b, err := exec.Command("ps", "-p", pid, "-o", "comm=").Output(); err == nil {
			name = strings.TrimSpace(string(b))
		}
		_, _ = fmt.Fprintf(log, "  port %d was held by %s (pid %s) — stopping it\n", port, filepath.Base(name), pid)
		if err := exec.Command("kill", pid).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  could not stop pid %s: %v\n", pid, err)
		}
	}
	// The socket is not free the instant the process is signalled.
	time.Sleep(300 * time.Millisecond)
}
