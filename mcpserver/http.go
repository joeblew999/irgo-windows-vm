package mcpserver

// The server over HTTP.
//
// Read docs/THREAT-MODEL.md before enabling this. The product is "run this
// arbitrary binary on my machine", so anything that can reach this port can
// execute code of its choosing in the Windows guest and lever on the host
// through everything the three steps already touch.
//
// Loopback is the default and needs no token. A non-loopback bind is a
// deliberate act: it takes AllowRemote, and it requires a bearer token. A
// server that starts unauthenticated because a token was missing is a server
// that will be run that way, so it is refused outright.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrNotLoopback is a refusal to expose the port to anything but this machine
// without authentication and explicit consent.
var ErrNotLoopback = errors.New("refusing to bind a non-loopback address")

// httpReadHeaderTimeout stops a client holding a connection open sending
// headers slowly. A bare http.Server has no timeouts at all.
const httpReadHeaderTimeout = 10 * time.Second

// ServeHTTPOptions is how to run the server over HTTP.
type ServeHTTPOptions struct {
	// Addr is what -http was given: "127.0.0.1:8129", or a bare ":8129" for
	// loopback on that port.
	Addr string

	// Secret is the bearer token required when Addr is not loopback. Empty
	// means no authentication, which is only permitted on loopback.
	Secret string

	// AllowRemote consents to a non-loopback bind. Without it a non-loopback
	// Addr is refused even with a Secret: the wider bind must be a deliberate
	// act, not a default.
	AllowRemote bool
}

// resolveAddr turns what -http was given into the address to bind and whether
// it is loopback.
//
// A bare ":8129" is the case worth care: it reads like a local default but
// would bind every interface, so it is resolved to 127.0.0.1:8129. The flag
// says "port", and a port means this machine. An unresolvable host is refused:
// "cannot tell" is not "safe", and this guard stands in front of arbitrary
// code execution.
func resolveAddr(addr string) (bind string, loopback bool, err error) {
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return "", false, fmt.Errorf("%w: %q is not host:port: %v", ErrNotLoopback, addr, splitErr)
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port), true, nil
	}
	if host == "localhost" {
		return addr, true, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", false, fmt.Errorf("%w: cannot resolve %q, so it cannot be shown to be local: %v",
			ErrNotLoopback, host, err)
	}
	if len(ips) == 0 {
		return "", false, fmt.Errorf("%w: %q resolves to nothing", ErrNotLoopback, host)
	}
	loopback = true
	for _, ip := range ips {
		if !ip.IsLoopback() {
			loopback = false
			break
		}
	}
	return addr, loopback, nil
}

// bearerVerifier checks a bearer token against the secret in constant time.
//
// The SDK rejects a TokenInfo with a zero Expiration by default, so a valid
// token returns one in the future — the token is a shared secret with no
// expiry of its own, and the middleware's strictness is not a fact about it.
func bearerVerifier(secret string) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
			return &auth.TokenInfo{Expiration: time.Now().Add(time.Hour)}, nil
		}
		return nil, fmt.Errorf("%w: bad token", auth.ErrInvalidToken)
	}
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
func ServeHTTP(ctx context.Context, o ServeHTTPOptions, d Deps) error {
	bind, loopback, err := resolveAddr(o.Addr)
	if err != nil {
		return err
	}
	if !loopback {
		if !o.AllowRemote {
			return fmt.Errorf("%w: %s is not loopback. Pass -allow-remote and set IRGO_WINVM_TOKEN to bind it — see docs/THREAT-MODEL.md",
				ErrNotLoopback, o.Addr)
		}
		if o.Secret == "" {
			return fmt.Errorf("%w: %s is reachable from outside this machine and IRGO_WINVM_TOKEN is not set. "+
				"Authentication is mandatory off loopback, not optional — see docs/THREAT-MODEL.md",
				ErrNotLoopback, o.Addr)
		}
	}

	// Announced only after the address has been accepted. The first version
	// printed this first, so a refused bind advertised a server that never
	// started — the message and the truth disagreeing by two lines.
	//
	// On stderr, never stdout: over stdio that is the protocol channel, and a
	// server writing its address into a JSON-RPC stream is the bug utmvm.Out
	// exists to prevent.
	state := "no authentication"
	if o.Secret != "" {
		state = "bearer token required"
	}
	fmt.Fprintf(os.Stderr, "irgo-winvm mcp on http://%s — %s. Read docs/THREAT-MODEL.md\n", bind, state)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return New(d) },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	// Cross-origin protection goes in middleware. The SDK's option field for it
	// is deprecated, and wrapping the handler is what replaces it.
	protected := http.NewCrossOriginProtection().Handler(handler)
	if o.Secret != "" {
		// Authentication wraps the cross-origin-protected handler, so a
		// request is authenticated before anything else sees it.
		protected = auth.RequireBearerToken(bearerVerifier(o.Secret), nil)(protected)
	}

	srv := &http.Server{
		Addr:              bind,
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
