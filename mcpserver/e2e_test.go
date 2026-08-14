package mcpserver_test

// The one test that spawns the real binary.
//
// Everything else here drives an in-memory server, which proves the protocol
// path but not that `irgo-winvm mcp` is wired to it. This builds the tool and
// talks to it over a pipe, the way a client does.
//
// It is what keeps the CLI and the server honest with each other: the tools a
// client sees must be the commands the binary reports.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTheRealBinaryServesTheCommandsItReports(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "irgo-winvm")
	build := exec.Command("go", "build", "-o", bin, "./cmd/irgo-winvm")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the CLI: %v: %s", err, out)
	}

	// What the binary says its commands are.
	listed, err := exec.Command(bin, "commands").Output()
	if err != nil {
		t.Fatalf("irgo-winvm commands: %v", err)
	}
	reported := map[string]bool{}
	for _, n := range strings.Fields(string(listed)) {
		reported[n] = true
	}
	if len(reported) == 0 {
		t.Fatal("the binary reported no commands; this test would pass vacuously")
	}

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "test"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}, nil)
	if err != nil {
		t.Fatalf("connecting to `irgo-winvm mcp`: %v", err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("listing tools from the real binary: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("the server offered no tools")
	}
	for _, tool := range tools.Tools {
		if !reported[tool.Name] {
			t.Errorf("the server offers a tool %q that `irgo-winvm commands` does not list", tool.Name)
		}
	}
	t.Logf("%d tools served by the real binary, all of them declared commands", len(tools.Tools))
}
