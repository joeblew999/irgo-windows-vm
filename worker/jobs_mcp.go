package main

// The job queue as a remote MCP server, so an agent on any OS can drive the
// Mac with no binary of its own: POST /api/mcp, the Streamable HTTP
// transport in its stateless form (one JSON-RPC request per POST, one JSON
// answer, no session, no server-sent events), with the caller's own queue
// token as the bearer.
//
// It holds no logic. Every tool is one request to the HTTP API above,
// made in-process with the caller's token (call), so a tool cannot do what
// the token could not do by curl, and there is one implementation of each
// operation.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// mcpProtocol is the newest MCP revision this answers as; a client asking
// for another is told this one, as the specification allows.
const mcpProtocol = "2025-06-18"

// maxInlineBinary is the largest binary a tool call may carry in base64. It
// is decoded in the Worker's memory; anything larger is uploaded by PUT to
// the URL submit_job answers with.
const maxInlineBinary = 16 << 20

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

var mcpTools = []map[string]any{
	{
		"name": "submit_job",
		"description": "Run a Windows ARM64 binary (GOOS=windows GOARCH=arm64 CGO_ENABLED=0) on a fresh Windows 11 VM on a Mac, cloned for this job and deleted after. " +
			"kind \"test\" is a `go test -c` binary: it is run with -test.v=test2json and the results come back as test2json events. " +
			"Send the binary as data_base64 (up to 16 MiB), or omit it and PUT the bytes to the upload URL in the answer with the same bearer token. " +
			"Returns the job id; poll job_status, then job_result. In args, {out} is a directory in the guest whose files the test names in `screenshot: <path>` lines come back.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":        map[string]any{"type": "string", "enum": []string{"app", "test"}},
				"name":        map[string]any{"type": "string", "description": "the binary's file name, ending in .exe"},
				"gui":         map[string]any{"type": "boolean", "description": "run on the desktop, required for anything with a window"},
				"args":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"timeout_s":   map[string]any{"type": "integer", "description": "seconds the program may run (default 600, at most 3600)"},
				"data_base64": map[string]any{"type": "string", "description": "the binary, base64"},
				"size":        map[string]any{"type": "integer", "description": "without data_base64: the binary's size"},
				"sha256":      map[string]any{"type": "string", "description": "without data_base64: the binary's SHA-256"},
			},
			"required": []string{"kind", "name"},
		},
	},
	idTool("job_status", "A job's state (uploading, queued, running, finished, cancelled, expired, lost, timed-out), its place in the queue, and, once finished, the exit code on the tool's exit-code table and the result files."),
	{
		"name":        "job_log",
		"description": "What the Mac has said about the job so far, from byte offset onwards. The answer ends with the offset to ask from next.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
			},
			"required": []string{"id"},
		},
	},
	idTool("job_result", "The finished job: its result.json, the program's output (stdout.txt, or test2json.json for a test), and every screenshot as an image."),
	idTool("cancel_job", "Cancel a job: at once if it has not started, otherwise the Mac stops it and deletes its VM."),
}

func idTool(name, desc string) map[string]any {
	return map[string]any{
		"name": name, "description": desc,
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		},
	}
}

// mcp is POST /api/mcp.
func (env Env) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// No server-sent events stream: this server never starts a message.
		w.Header().Set("Allow", "POST")
		fail(w, http.StatusMethodNotAllowed, "POST a JSON-RPC message")
		return
	}
	_, role, ok := env.jobCaller(w, r)
	if !ok {
		return
	}
	if role == roleRunner {
		fail(w, http.StatusForbidden, "refused: the runner token only takes and reports jobs")
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxInlineBinary*2)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError(nil, -32700, "parse error: "+err.Error()))
		return
	}
	if len(req.ID) == 0 { // a notification: nothing to answer
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		writeJSON(w, http.StatusOK, rpcResult(req.ID, map[string]any{
			"protocolVersion": mcpProtocol,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "irgo-windows-vm", "version": "1"},
			"instructions":    "Run Windows ARM64 binaries on a real Windows 11 VM on a Mac. submit_job, then job_status until it is final, then job_result.",
		}))
	case "ping":
		writeJSON(w, http.StatusOK, rpcResult(req.ID, map[string]any{}))
	case "tools/list":
		writeJSON(w, http.StatusOK, rpcResult(req.ID, map[string]any{"tools": mcpTools}))
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeJSON(w, http.StatusOK, rpcError(req.ID, -32602, "invalid params: "+err.Error()))
			return
		}
		content, isErr := env.mcpCall(r.Header.Get("Authorization"), p.Name, p.Arguments)
		writeJSON(w, http.StatusOK, rpcResult(req.ID, map[string]any{"content": content, "isError": isErr}))
	default:
		writeJSON(w, http.StatusOK, rpcError(req.ID, -32601, "method not found: "+req.Method))
	}
}

func rpcResult(id json.RawMessage, v any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": v}
}

func rpcError(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

func text(s string) []mcpContent { return []mcpContent{{Type: "text", Text: s}} }

// mcpCall runs one tool as requests to the API, with the caller's token.
func (env Env) mcpCall(auth, name string, a map[string]any) ([]mcpContent, bool) {
	str := func(k string) string { s, _ := a[k].(string); return s }
	num := func(k string) int64 { f, _ := a[k].(float64); return int64(f) }
	id := str("id")
	if name != "submit_job" && !isJobID(id) {
		return text("id must be a job id: 32 hex digits"), true
	}
	switch name {
	case "submit_job":
		spec := JobSpec{Kind: str("kind"), Name: str("name"), TimeoutS: int(num("timeout_s"))}
		spec.GUI, _ = a["gui"].(bool)
		if list, ok := a["args"].([]any); ok {
			for _, x := range list {
				spec.Args = append(spec.Args, fmt.Sprint(x))
			}
		}
		var data []byte
		if s := str("data_base64"); s != "" {
			if base64.StdEncoding.DecodedLen(len(s)) > maxInlineBinary+3 {
				return text(fmt.Sprintf("data_base64 is over %d bytes; submit without it and PUT the binary to the upload URL", maxInlineBinary)), true
			}
			var err error
			if data, err = base64.StdEncoding.DecodeString(s); err != nil {
				return text("data_base64 is not base64: " + err.Error()), true
			}
			sum := sha256.Sum256(data)
			spec.Size, spec.SHA256 = int64(len(data)), hex.EncodeToString(sum[:])
		} else {
			spec.Size, spec.SHA256 = num("size"), str("sha256")
		}
		body, _ := json.Marshal(spec)
		code, out := env.call(auth, http.MethodPost, "/api/jobs", body)
		if code != http.StatusCreated || data == nil {
			return text(string(out)), code != http.StatusCreated
		}
		var made struct {
			Job    jobView `json:"job"`
			Upload string  `json:"upload"`
		}
		if err := json.Unmarshal(out, &made); err != nil {
			return text(string(out)), true
		}
		code, out = env.call(auth, http.MethodPut, made.Upload, data)
		return text(string(out)), code != http.StatusOK
	case "job_status":
		code, out := env.call(auth, http.MethodGet, jobPath(id), nil)
		return text(string(out)), code != http.StatusOK
	case "cancel_job":
		code, out := env.call(auth, http.MethodPost, jobPath(id, "cancel"), nil)
		return text(string(out)), code != http.StatusOK
	case "job_log":
		off := num("offset")
		rec := env.record(auth, http.MethodGet, jobPath(id, "log")+"?offset="+strconv.FormatInt(off, 10), nil)
		if rec.code != http.StatusOK {
			return text(rec.buf.String()), true
		}
		return text(rec.buf.String() + "\n[next offset: " + rec.h.Get("X-Log-Size") + "]"), false
	case "job_result":
		code, out := env.call(auth, http.MethodGet, jobPath(id), nil)
		if code != http.StatusOK {
			return text(string(out)), true
		}
		var j jobView
		if err := json.Unmarshal(out, &j); err != nil {
			return text(string(out)), true
		}
		if !finalState(j.State) {
			return text(fmt.Sprintf("job %s is %s; ask again when it is final\n%s", id, j.State, out)), true
		}
		content := text(string(out))
		for _, f := range j.Files {
			if f.Size > 2<<20 {
				content = append(content, mcpContent{Type: "text", Text: fmt.Sprintf("%s: %d bytes, too large to include; GET %s", f.Key, f.Size, jobPath(id, "files", f.Key))})
				continue
			}
			code, b := env.call(auth, http.MethodGet, jobPath(id, "files", f.Key), nil)
			switch {
			case code != http.StatusOK:
				content = append(content, mcpContent{Type: "text", Text: f.Key + ": " + string(b)})
			case strings.HasSuffix(f.Key, ".png"):
				content = append(content, mcpContent{Type: "text", Text: f.Key + ":"},
					mcpContent{Type: "image", Data: base64.StdEncoding.EncodeToString(b), MimeType: "image/png"})
			default:
				content = append(content, mcpContent{Type: "text", Text: "--- " + f.Key + "\n" + string(b)})
			}
		}
		return content, j.State != stFinished || j.ExitCode == nil || *j.ExitCode != 0
	}
	return text("no such tool: " + name), true
}

// recorder is an in-process ResponseWriter for call. On Workers it also
// takes a stream (jobs_js.go), reading it into memory, which only these
// small answers do.
type recorder struct {
	h    http.Header
	code int
	buf  bytes.Buffer
	err  error
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

// inBody is a request body already in memory. On Workers it hands itself
// to R2 as a Uint8Array (jobs_js.go), so a binary sent inline is stored and
// hashed by R2 exactly as one PUT over HTTP.
type inBody struct {
	*bytes.Reader
	b []byte
}

func (inBody) Close() error { return nil }

func (env Env) record(auth, method, path string, body []byte) *recorder {
	rec := &recorder{h: http.Header{}}
	req, err := http.NewRequest(method, "https://internal"+path, nil)
	if err != nil {
		rec.code = http.StatusInternalServerError
		rec.buf.WriteString(err.Error())
		return rec
	}
	if body != nil {
		req.Body, req.ContentLength = inBody{bytes.NewReader(body), body}, int64(len(body))
		sum := sha256.Sum256(body)
		req.Header.Set(hdrJobSHA256, hex.EncodeToString(sum[:]))
	}
	req.Header.Set("Authorization", auth)
	Handler(env).ServeHTTP(rec, req)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if rec.err != nil {
		rec.code = http.StatusBadGateway
		rec.buf.WriteString(rec.err.Error())
	}
	return rec
}

func (env Env) call(auth, method, path string, body []byte) (int, []byte) {
	rec := env.record(auth, method, path, body)
	return rec.code, rec.buf.Bytes()
}
