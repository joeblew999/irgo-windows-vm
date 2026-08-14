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
)

// TestOnlyLoopbackIsBound.
//
// Anything that reaches this port can run a binary of its choosing in the
// guest. Authentication is not built yet, so the only defence is that nothing
// off this machine can reach it.
//
// ":8129" is the case worth having a test for: it reads like a local default
// and binds every interface. It is refused rather than quietly rewritten,
// because rewriting would make the flag do something other than what it says.
//
// Negative control, run by hand: return nil from checkLoopback and every
// refusing case below passes an address that exposes the machine.
func TestOnlyLoopbackIsBound(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		allow  bool
		reason string
	}{
		{"127.0.0.1:8129", true, "the loopback address"},
		{"localhost:8129", true, "the loopback name"},
		{"[::1]:8129", true, "loopback over IPv6"},
		{":8129", false, "a bare port binds every interface"},
		{"0.0.0.0:8129", false, "all interfaces, said explicitly"},
		{"192.168.1.50:8129", false, "a LAN address"},
		{"8129", false, "not host:port at all"},
		{"", false, "empty"},
	} {
		t.Run(tc.addr+" "+tc.reason, func(t *testing.T) {
			err := checkLoopback(tc.addr)
			if tc.allow && err != nil {
				t.Errorf("checkLoopback(%q) refused a local address: %v", tc.addr, err)
			}
			if !tc.allow {
				if err == nil {
					t.Errorf("checkLoopback(%q) allowed %s — anything reaching it can run code here", tc.addr, tc.reason)
					return
				}
				if !errors.Is(err, ErrNotLoopback) {
					t.Errorf("checkLoopback(%q) failed for the wrong reason: %v", tc.addr, err)
				}
			}
		})
	}
}

// TestTheRefusalSaysWhatToWriteInstead — a guard that refuses without saying
// what would work gets worked around rather than obeyed.
func TestTheRefusalSaysWhatToWriteInstead(t *testing.T) {
	err := checkLoopback(":8129")
	if err == nil {
		t.Fatal("a bare port was allowed")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:8129") {
		t.Errorf("the refusal does not say what to write instead: %v", err)
	}
	err = checkLoopback("192.168.1.50:8129")
	if !strings.Contains(err.Error(), "THREAT-MODEL") {
		t.Errorf("refusing a LAN bind does not point at the threat model: %v", err)
	}
}

// TestServeHTTPRefusesBeforeListening is the property that matters more than
// the message: a refused address must not have been bound first.
func TestServeHTTPRefusesBeforeListening(t *testing.T) {
	// A port nothing else is using, so if ServeHTTP bound it despite refusing,
	// this dial would succeed.
	const addr = "0.0.0.0:8131"
	err := ServeHTTP(context.Background(), addr, Deps{Version: "test"})
	if err == nil {
		t.Fatal("ServeHTTP accepted an address that exposes the machine")
	}
	if !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("refused for the wrong reason: %v", err)
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
	go func() { done <- ServeHTTP(ctx, addr, Deps{Version: "test"}) }()

	// Wait for it rather than sleeping a guessed amount.
	var up bool
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			up = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		t.Fatal("the server never started listening")
	}

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
