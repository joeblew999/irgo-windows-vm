package mcpserver

// The HTTP transport, and the guard in front of it.
//
// This is remote code execution by design — see docs/THREAT-MODEL.md — so the
// refusals matter more than the happy path, and there are more of them here.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestResolveAddr.
//
// A bare ":8129" reads like a local default but would bind every interface, so
// it is resolved to 127.0.0.1:8129 rather than left to mean every interface.
// Everything else is classified loopback or not, so ServeHTTP can decide
// whether consent and authentication are required.
//
// Negative control, run by hand: resolving ":8129" to "" or to 0.0.0.0 makes
// the bare-port case below fail.
func TestResolveAddr(t *testing.T) {
	for _, tc := range []struct {
		addr         string
		wantBind     string
		wantLoopback bool
		wantErr      bool
	}{
		{"127.0.0.1:8129", "127.0.0.1:8129", true, false},
		{"localhost:8129", "localhost:8129", true, false},
		{"[::1]:8129", "[::1]:8129", true, false},
		{":8129", "127.0.0.1:8129", true, false},
		{"0.0.0.0:8129", "0.0.0.0:8129", false, false},
		{"192.168.1.50:8129", "192.168.1.50:8129", false, false},
		{"8129", "", false, true},
		{"", "", false, true},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			bind, loopback, err := resolveAddr(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveAddr(%q) = %q, %v, nil; want an error", tc.addr, bind, loopback)
				}
				if !errors.Is(err, ErrNotLoopback) {
					t.Errorf("resolveAddr(%q) failed for the wrong reason: %v", tc.addr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAddr(%q): %v", tc.addr, err)
			}
			if bind != tc.wantBind {
				t.Errorf("bind = %q, want %q", bind, tc.wantBind)
			}
			if loopback != tc.wantLoopback {
				t.Errorf("loopback = %v, want %v", loopback, tc.wantLoopback)
			}
		})
	}
}

// TestTheRefusalSaysWhatToDoInstead — a guard that refuses without saying what
// would work gets worked around rather than obeyed.
func TestTheRefusalSaysWhatToDoInstead(t *testing.T) {
	err := ServeHTTP(context.Background(), ServeHTTPOptions{Addr: "192.168.1.50:8129"}, Deps{Version: "test"})
	if err == nil {
		t.Fatal("a non-loopback bind without consent was allowed")
	}
	if !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if !strings.Contains(err.Error(), "-allow-remote") {
		t.Errorf("the refusal does not say to pass -allow-remote: %v", err)
	}
	if !strings.Contains(err.Error(), "IRGO_WINVM_TOKEN") {
		t.Errorf("the refusal does not name the token: %v", err)
	}
	if !strings.Contains(err.Error(), "THREAT-MODEL") {
		t.Errorf("the refusal does not point at the threat model: %v", err)
	}
}

// TestServeHTTPRefusesBeforeListening is the property that matters more than
// the message: a refused address must not have been bound first.
func TestServeHTTPRefusesBeforeListening(t *testing.T) {
	for _, o := range []ServeHTTPOptions{
		{Addr: "0.0.0.0:8131"},                    // no consent
		{Addr: "0.0.0.0:8131", AllowRemote: true}, // consent, but no token
	} {
		err := ServeHTTP(context.Background(), o, Deps{Version: "test"})
		if err == nil {
			t.Fatalf("ServeHTTP(%+v) accepted an address that exposes the machine", o)
		}
		if !errors.Is(err, ErrNotLoopback) {
			t.Fatalf("ServeHTTP(%+v) refused for the wrong reason: %v", o, err)
		}
	}
	c, dErr := net.DialTimeout("tcp", "127.0.0.1:8131", 200*time.Millisecond)
	if dErr == nil {
		_ = c.Close()
		t.Error("the port was listening even though the address was refused")
	}
}

// TestTheServerAnswersOverHTTP — the transport works, end to end, over a real
// socket with a real client.
func TestTheServerAnswersOverHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Port 0 would be ideal, but the address has to be known to connect to it,
	// and ServeHTTP owns the listener. A fixed high port with a skip if it is
	// busy is honest: a flake here would be somebody else's server, not ours.
	const addr = "127.0.0.1:8132"
	if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		_ = c.Close()
		t.Skipf("%s is already in use", addr)
	}

	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, ServeHTTPOptions{Addr: addr}, Deps{Version: "test"}) }()

	waitListening(t, addr)

	// A GET must be refused: stateless mode answers 405, which is how a caller
	// can tell it is talking to a stateless server at all.
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %d, want 405 — stateless mode should refuse it", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("ServeHTTP returned %v after the context was cancelled", err)
	}

	// And the port must be free afterwards, or a restart fails with "address
	// already in use" and the cause is invisible.
	time.Sleep(100 * time.Millisecond)
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Error("the port is still held after the context was cancelled")
	}
}

// waitListening polls until the server accepts a connection, so a test does not
// sleep a guessed amount.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server never started listening")
}

// TestTheServerWithASecretRequiresTheToken — authentication, once set, is not
// optional: a request without the token, or with a wrong one, is refused with
// 401. A GET with the right token reaches the handler and gets the stateless
// 405, which is how we know the token check passed and the request was let
// through.
func TestTheServerWithASecretRequiresTheToken(t *testing.T) {
	const addr = "127.0.0.1:8134"
	if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		_ = c.Close()
		t.Skipf("%s is already in use", addr)
	}
	const secret = "test-secret"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeHTTP(ctx, ServeHTTPOptions{Addr: addr, Secret: secret}, Deps{Version: "test"})
	}()
	waitListening(t, addr)

	for _, tc := range []struct {
		name string
		auth string
		want int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"right token", "Bearer " + secret, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://"+addr, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("GET with %s returned %d, want %d", tc.name, resp.StatusCode, tc.want)
			}
		})
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("ServeHTTP returned %v after the context was cancelled", err)
	}
}

// bearerTransport puts a fixed Authorization header on every request, which is
// how a bearer-authenticated HTTP client reaches the server.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// TestAnHTTPClientWithTheTokenSpeaksToTheServer is the one that proves the
// whole thing: the SDK's own HTTP client transport, against the real socket,
// with authentication. A client without the token must not connect; with it, a
// tool call must round-trip.
func TestAnHTTPClientWithTheTokenSpeaksToTheServer(t *testing.T) {
	const addr = "127.0.0.1:8135"
	if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		_ = c.Close()
		t.Skipf("%s is already in use", addr)
	}
	const secret = "test-secret"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeHTTP(ctx, ServeHTTPOptions{Addr: addr, Secret: secret}, Deps{
			Version: "test",
			Run: func(context.Context, string, []string) (string, error) {
				return "hello from the guest", nil
			},
		})
	}()
	waitListening(t, addr)

	// Without the token, connecting must fail.
	_, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             "http://" + addr,
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
		}, nil)
	if err == nil {
		t.Fatal("connected without a token; authentication was not required")
	}

	// With the token, it connects and a tool call round-trips.
	client, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             "http://" + addr,
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
			HTTPClient:           &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: secret}},
		}, nil)
	if err != nil {
		t.Fatalf("connecting with the token: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "doctor", Arguments: map[string]any{"args": []string{}}})
	if err != nil {
		t.Fatalf("tool call over HTTP: %v", err)
	}
	if res.IsError {
		t.Errorf("tool call returned an error result: %v", res)
	}
	if !strings.Contains(text(res), "hello from the guest") {
		t.Errorf("tool output did not round-trip: %v", res)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("ServeHTTP returned %v after the context was cancelled", err)
	}
}
