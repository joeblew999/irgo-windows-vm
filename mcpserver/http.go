package mcpserver

// The server over HTTP.
//
// Read docs/THREAT-MODEL.md before enabling this. The product is "run this
// arbitrary binary on my machine", so anything that can reach this port can
// execute code of its choosing in the Windows guest and lever on the host
// through everything the three steps already touch.
//
// This is the first half: loopback only, and a non-loopback bind is refused
// outright rather than warned about. Authentication is not built yet, and a
// server that starts unauthenticated because a token was missing is a server
// that will be run that way.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrNotLoopback is a refusal to expose the port to anything but this machine.
var ErrNotLoopback = errors.New("refusing to bind a non-loopback address")

// httpReadHeaderTimeout stops a client holding a connection open sending
// headers slowly. A bare http.Server has no timeouts at all.
const httpReadHeaderTimeout = 10 * time.Second

// checkLoopback reports whether an address is safe to bind with no
// authentication in front of it.
//
// A bare port is the dangerous case and the easy mistake: ":8129" binds every
// interface, which reads like a local default and is not one. It is refused,
// rather than silently rewritten to localhost — rewriting would mean the flag
// does something other than what it says.
//
// An unresolvable host is refused too. "Cannot tell" is not "safe", and this
// guard stands in front of arbitrary code execution.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %q is not host:port: %v", ErrNotLoopback, addr, err)
	}
	if host == "" {
		return fmt.Errorf("%w: %q binds every interface. Write 127.0.0.1%s to mean this machine only",
			ErrNotLoopback, addr, addr)
	}
	if host == "localhost" {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %q, so it cannot be shown to be local: %v",
			ErrNotLoopback, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: %q resolves to nothing", ErrNotLoopback, host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: %s resolves to %s, which is reachable from outside this machine. "+
				"Authentication is not built yet, so anything that reaches this port can run code here — "+
				"see docs/THREAT-MODEL.md", ErrNotLoopback, host, ip)
		}
	}
	return nil
}

// ServeHTTP serves MCP over HTTP until the context is cancelled.
//
// Stateless, which is what the 2026-07-28 revision of the protocol requires: a
// stateful server negotiates down to the older revision. It also means
// server-to-client requests are rejected outright, which is why a long job is
// keyed by an id in the tool arguments rather than by a session.
//
// DNS rebinding protection is deliberately left alone. The SDK rejects a
// localhost request carrying a non-localhost Host header with 403, which is the
// defence against a browser on this machine being tricked into driving the
// server. Setting DisableLocalhostProtection would turn that off, and the
// symptom it causes — a confusing 403 during development — is exactly the sort
// of thing somebody switches off to make a problem go away.
func ServeHTTP(ctx context.Context, addr string, d Deps) error {
	if err := checkLoopback(addr); err != nil {
		return err
	}

	// Announced only after the address has been accepted. The first version
	// printed this first, so a refused bind advertised a server that never
	// started — the message and the truth disagreeing by two lines.
	//
	// On stderr, never stdout: over stdio that is the protocol channel, and a
	// server writing its address into a JSON-RPC stream is the bug utmvm.Out
	// exists to prevent.
	fmt.Fprintf(os.Stderr, "irgo-winvm mcp on http://%s — read docs/THREAT-MODEL.md\n", addr)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return New(d) },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	// Cross-origin protection goes in middleware. The SDK's option field for it
	// is deprecated, and wrapping the handler is what replaces it.
	protected := http.NewCrossOriginProtection().Handler(handler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           protected,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	// Shut down when the context is cancelled, so ctrl-c does not leave the
	// port held by a process nobody can see.
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
