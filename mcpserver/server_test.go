package mcpserver

// These drive a real server over a real transport, in memory.
//
// mcp.NewInMemoryTransports gives a connected client and server with no
// subprocess and no network, so the whole protocol path — registration,
// listing, calling, results — runs under `go test` on a machine that has never
// seen UTM.

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestTheFailureIsMachineReadable is the difference between a tool an agent can
// act on and one it has to guess about.
//
// The wording of "the VM is there, the guest agent is not answering" will be
// reworded one day. `"code": 4, "retryable": true` will not. An agent matching
// on the sentence breaks silently; one matching on the field does not.
//
// Negative control, run by hand: return CodeFailed for every error and the
// no-agent case below reports retryable:false, which is the mistake that costs
// a 45-minute install.
func TestTheFailureIsMachineReadable(t *testing.T) {
	for _, tc := range []struct {
		name          string
		code          command.Code
		wantStatus    string
		wantRetryable bool
	}{
		{"no such VM", command.CodeNoVM, "no-vm", false},
		{"agent busy", command.CodeNoAgent, "no-agent", true},
		{"the guest program failed", command.CodeFailed, "failed", false},
		{"refused without -force", command.CodeNeedForce, "need-force", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			server := New(Deps{
				Version:  "test",
				Run:      func(context.Context, string, []string) (string, error) { return "", errors.New("it went wrong") },
				Classify: func(error) command.Code { return tc.code },
			})
			ct, st := mcp.NewInMemoryTransports()
			done := make(chan error, 1)
			go func() { done <- server.Run(ctx, st) }()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cs.Close(); <-done }()

			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm-screen", Arguments: map[string]any{"args": []string{}}})
			if err != nil {
				t.Fatalf("became a protocol error: %v", err)
			}
			if !res.IsError {
				t.Fatal("a failure was not marked IsError")
			}

			// Read it back the way a client would: off the wire, not out of a
			// Go value the server still holds.
			raw, mErr := json.Marshal(res.StructuredContent)
			if mErr != nil {
				t.Fatalf("structured content did not marshal: %v", mErr)
			}
			var got outcome
			if uErr := json.Unmarshal(raw, &got); uErr != nil {
				t.Fatalf("structured content is not the documented shape: %v (%s)", uErr, raw)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Code != int(tc.code) {
				t.Errorf("code = %d, want %d", got.Code, tc.code)
			}
			if got.Retryable != tc.wantRetryable {
				t.Errorf("retryable = %v, want %v — this is the field that decides "+
					"whether an agent waits or gives up", got.Retryable, tc.wantRetryable)
			}
			if got.Command != "vm-screen" {
				t.Errorf("command = %q", got.Command)
			}
			if tc.wantRetryable && !strings.Contains(text(res), "worth retrying") {
				t.Error("the retryable case does not say so in the text a model reads")
			}
		})
	}
}

// TestExactlyOneOutcomeIsRetryable.
//
// Retryable is advice an agent acts on: it will wait and call again. Marking a
// second code retryable — "no such VM", say — would have it retry forever
// against a VM that will never exist.
func TestExactlyOneOutcomeIsRetryable(t *testing.T) {
	var retryable []string
	for _, o := range command.Outcomes {
		if o.Retryable {
			retryable = append(retryable, o.Name)
		}
	}
	if len(retryable) != 1 || retryable[0] != "no-agent" {
		t.Errorf("retryable outcomes are %v; only no-agent should be, because it is the "+
			"only one where waiting changes the answer", retryable)
	}
}

// pngHeader is the first bytes of a real PNG, so the test asserts on something
// a decoder would accept rather than on any old bytes.
var pngHeader = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// TestVMScreenReturnsThePicture is the tool that justifies the server.
//
// From the host a stuck boot and a working one look identical — that is why
// vm-screen exists — and an agent cannot open a file path. A result that says
// "written to ~/Library/.../shot.png" is useless to the caller that most needs
// it, and over a remote transport the path is on a machine it cannot reach.
//
// Negative control, run by hand: return the path as text instead of the bytes
// and this fails on the missing ImageContent.
func TestVMScreenReturnsThePicture(t *testing.T) {
	ctx := context.Background()
	want := append(append([]byte{}, pngHeader...), []byte("pretend pixels")...)

	server := New(Deps{
		Version: "test",
		Run:     func(context.Context, string, []string) (string, error) { return "", nil },
		Screenshot: func(_ context.Context, args []string) ([]byte, string, error) {
			return want, "shot: /tmp/x.png\n", nil
		},
	})
	ct, st := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close(); <-done }()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm-screen", Arguments: map[string]any{"args": []string{}}})
	if err != nil {
		t.Fatalf("calling vm-screen: %v", err)
	}
	if res.IsError {
		t.Fatalf("vm-screen reported an error: %s", text(res))
	}

	var img *mcp.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			img = ic
		}
	}
	if img == nil {
		t.Fatalf("no image in the result; the agent got %q and nothing to look at", text(res))
	}
	if img.MIMEType != "image/png" {
		t.Errorf("mime type is %q", img.MIMEType)
	}
	// Compared after a round trip through the wire, where the SDK base64-encodes
	// and decodes it — the encoding is exactly where an image gets corrupted.
	if !bytes.Equal(img.Data, want) {
		t.Errorf("the image came back as %d bytes, sent %d", len(img.Data), len(want))
	}
	if !bytes.HasPrefix(img.Data, pngHeader) {
		t.Error("what arrived is not a PNG")
	}
	if !strings.Contains(text(res), "shot:") {
		t.Error("the command's own output was dropped; it names where the file went on the host")
	}
}

// TestPromoteReturnsNoPicture — -promote publishes shots already taken and
// photographs nothing. A result claiming to show the screen when it shows a
// copy operation is a lie the caller cannot detect.
func TestPromoteReturnsNoPicture(t *testing.T) {
	ctx := context.Background()
	server := New(Deps{
		Version: "test",
		Run:     func(context.Context, string, []string) (string, error) { return "", nil },
		Screenshot: func(_ context.Context, args []string) ([]byte, string, error) {
			return nil, "2 stage(s) published\n", nil
		},
	})
	ct, st := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close(); <-done }()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "vm-screen", Arguments: map[string]any{"args": []string{"-promote", "docs/screens/vm"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Content {
		if _, ok := c.(*mcp.ImageContent); ok {
			t.Error("-promote returned an image; it takes no picture")
		}
	}
	if !strings.Contains(text(res), "published") {
		t.Errorf("the output was lost: %q", text(res))
	}
}

// TestALongCallStartsAJobInsteadOfBlocking.
//
// vm-create -install is about 45 minutes. Every MCP client times out long
// before that, so blocking means the call is abandoned while the install
// carries on and the agent has nothing left to ask about it.
//
// Negative controls, run by hand: clear Detach on vm-create and this blocks
// through Run instead; drop the DetachedBy check and the plain vm-create case
// below starts a job it should have run inline.
func TestALongCallStartsAJobInsteadOfBlocking(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantJob  bool
		wantRuns bool
	}{
		{"install detaches", []string{"-install"}, true, false},
		{"install with a value detaches", []string{"-install=true"}, true, false},
		{"plain vm-create runs inline", []string{}, false, true},
		{"an unrelated flag runs inline", []string{"-vm", "other"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ran, startedJob bool
			ctx := context.Background()
			server := New(Deps{
				Version: "test",
				Run: func(context.Context, string, []string) (string, error) {
					ran = true
					return "finished inline", nil
				},
				StartJob: func(string, []string) (string, error) {
					startedJob = true
					return "vm-create-20260814-150000", nil
				},
			})
			ct, st := mcp.NewInMemoryTransports()
			done := make(chan error, 1)
			go func() { done <- server.Run(ctx, st) }()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cs.Close(); <-done }()

			res, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name: "vm-create", Arguments: map[string]any{"args": tc.args}})
			if err != nil {
				t.Fatalf("calling: %v", err)
			}
			if res.IsError {
				t.Fatalf("reported an error: %s", text(res))
			}
			if startedJob != tc.wantJob {
				t.Errorf("started a job = %v, want %v", startedJob, tc.wantJob)
			}
			if ran != tc.wantRuns {
				t.Errorf("ran inline = %v, want %v", ran, tc.wantRuns)
			}
			if tc.wantJob {
				var got started
				raw, _ := json.Marshal(res.StructuredContent)
				if uErr := json.Unmarshal(raw, &got); uErr != nil {
					t.Fatalf("structured content is not the documented shape: %v", uErr)
				}
				if got.Job == "" || !got.Running {
					t.Errorf("the result does not name a running job: %+v", got)
				}
				if !strings.Contains(text(res), "status") {
					t.Error("the result does not tell the agent how to ask about the job")
				}
			}
		})
	}
}
