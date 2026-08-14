package mcpserver

// These drive a real server over a real transport, in memory.
//
// mcp.NewInMemoryTransports gives a connected client and server with no
// subprocess and no network, so the whole protocol path — registration,
// listing, calling, results — runs under `go test` on a machine that has never
// seen UTM.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joeblew999/irgo-windows-vm/command"
)

// connect starts a server with the given runner and returns a connected client
// session.
func connect(t *testing.T, run func(context.Context, string, []string) (string, error)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	server := New(Deps{Version: "test", Run: run})
	ct, st := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, st) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		<-serverDone
	})
	return cs
}

// TestToolsAreExactlyTheDeclaredCommands is the test that keeps the CLI and the
// server from drifting apart.
//
// The tool list is generated from command.All, so this cannot fail today. It
// can fail tomorrow, which is the point: a command added with OverMCP set and
// no thought, or a hand-written tool added here, breaks it. Nothing else would
// notice — an MCP client sees whatever it is told, and there is nothing to
// compare against unless something compares.
//
// Negative control, run by hand: add a tool in New that is not in command.All
// and this names it; clear OverMCP on vm-create and it names that too.
func TestToolsAreExactlyTheDeclaredCommands(t *testing.T) {
	cs := connect(t, func(context.Context, string, []string) (string, error) { return "", nil })

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	want := map[string]bool{}
	for _, c := range command.All {
		if c.OverMCP {
			want[c.Name] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("no command is exposed over MCP; this test would pass vacuously")
	}

	for name := range want {
		if !got[name] {
			t.Errorf("%s is a declared command marked OverMCP but the server registered no tool for it", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("the server registered a tool %q that is not a declared command — "+
				"it is a second list, and this is what stops there being one", name)
		}
	}
	t.Logf("%d tools, exactly the commands marked OverMCP", len(got))
}

// TestAnnotationsSayWhatIsSafeToCall.
//
// The SDK serializes ReadOnlyHint and IdempotentHint unconditionally, because
// they are bare bools — so there is no "unset", and a tool that says nothing
// still ships readOnly:false. That makes a wrong value a claim rather than a
// silence, and the claim an agent acts on: told a delete is read-only, it will
// call it to see what is there.
//
// Negative control, run by hand: mark vm-delete ReadOnly in command.All and
// this fails.
func TestAnnotationsSayWhatIsSafeToCall(t *testing.T) {
	cs := connect(t, func(context.Context, string, []string) (string, error) { return "", nil })
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		c, ok := command.Find(tool.Name)
		if !ok {
			continue // covered by the test above
		}
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations, so it ships the zero value of every hint", tool.Name)
			continue
		}
		if tool.Annotations.ReadOnlyHint != c.ReadOnly {
			t.Errorf("%s: readOnlyHint is %v, the command says %v", tool.Name, tool.Annotations.ReadOnlyHint, c.ReadOnly)
		}
		if c.Destructive && (tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Errorf("%s destroys something and is not annotated destructive", tool.Name)
		}
		if c.ReadOnly && c.Destructive {
			t.Errorf("%s is declared both read-only and destructive", tool.Name)
		}
	}
}

// TestCallPassesTheArgumentsThrough — the tool takes the command line a person
// would have typed, so what arrives at the runner must be exactly that.
func TestCallPassesTheArgumentsThrough(t *testing.T) {
	var gotName string
	var gotArgs []string
	cs := connect(t, func(_ context.Context, name string, args []string) (string, error) {
		gotName, gotArgs = name, args
		return "it ran", nil
	})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "app-create",
		Arguments: map[string]any{"args": []string{"-vm", "irgo-win11", "prog.exe"}},
	})
	if err != nil {
		t.Fatalf("calling: %v", err)
	}
	if res.IsError {
		t.Fatalf("call reported an error: %s", text(res))
	}
	if gotName != "app-create" {
		t.Errorf("runner got name %q", gotName)
	}
	if strings.Join(gotArgs, " ") != "-vm irgo-win11 prog.exe" {
		t.Errorf("runner got args %q", gotArgs)
	}
	if !strings.Contains(text(res), "it ran") {
		t.Errorf("the command's output did not reach the result: %q", text(res))
	}
}

// TestAFailingCommandIsAResultNotAProtocolError.
//
// The SDK's own documentation is explicit: an error originating in a tool
// belongs inside the result, or the model cannot see it and correct itself.
// Raised as a protocol error instead, "that VM does not exist" reaches the
// client as a transport failure — and an agent that could have fixed it by
// running vm-create is told the connection is broken.
//
// It also checks that the output produced before the failure survives, which is
// usually the answer to why it failed.
//
// Negative control, run by hand: return the error from the handler instead of
// wrapping it in a result, and CallTool returns a non-nil error here.
func TestAFailingCommandIsAResultNotAProtocolError(t *testing.T) {
	cs := connect(t, func(context.Context, string, []string) (string, error) {
		return "looked for the VM\n", errors.New("no such VM: irgo-win11")
	})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "vm-screen",
		Arguments: map[string]any{"args": []string{}},
	})
	if err != nil {
		t.Fatalf("a failing command produced a protocol error, which the model never sees: %v", err)
	}
	if !res.IsError {
		t.Error("a failing command produced a result not marked IsError")
	}
	if !strings.Contains(text(res), "no such VM") {
		t.Errorf("the failure is not in the result: %q", text(res))
	}
	if !strings.Contains(text(res), "looked for the VM") {
		t.Errorf("output printed before the failure was dropped: %q", text(res))
	}
}

// TestMalformedArgumentsAreReportedToTheModel.
//
// Server.AddTool does not validate against the schema — the generic AddTool
// does, and it infers the schema from a Go type rather than from the command
// list. So the unmarshal is the validation, and it must not take the process
// with it.
func TestMalformedArgumentsAreReportedToTheModel(t *testing.T) {
	cs := connect(t, func(context.Context, string, []string) (string, error) {
		t.Error("the command ran despite unreadable arguments")
		return "", nil
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "doctor",
		Arguments: map[string]any{"args": "not an array"},
	})
	if err != nil {
		t.Fatalf("bad arguments became a protocol error: %v", err)
	}
	if !res.IsError {
		t.Error("bad arguments produced a success result")
	}
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
