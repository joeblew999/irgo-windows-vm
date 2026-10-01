package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

var jobVars = map[string]string{
	"JOBS_TOKENS":       "alice=tok-alice, bob=tok-bob",
	"JOBS_ADMIN_TOKEN":  "tok-admin",
	"JOBS_RUNNER_TOKEN": "tok-runner",
}

// jobsEnv is a Worker with an in-memory JOBS bucket and a clock the test
// moves.
func jobsEnv(vars map[string]string) (http.Handler, *memJobs, *time.Time) {
	b := newMemJobs()
	now := testNow
	env := Env{
		Var:  func(n string) string { return vars[n] },
		Jobs: func() (JobBucket, error) { return b, nil },
		Now:  func() time.Time { return now },
	}
	return Handler(env), b, &now
}

func jdo(h http.Handler, method, path, token string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

var exe = []byte("MZ this is not really a Windows binary")

func specJSON(b []byte, extra string) []byte {
	return []byte(fmt.Sprintf(`{"kind":"app","name":"hello.exe","size":%d,"sha256":%q%s}`, len(b), sum(b), extra))
}

func decodeJob(t *testing.T, w *httptest.ResponseRecorder) wire.JobView {
	t.Helper()
	var v wire.JobView
	body := w.Body.Bytes()
	var made struct {
		Job *wire.JobView `json:"job"`
	}
	if json.Unmarshal(body, &made) == nil && made.Job != nil {
		return *made.Job
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("not a job: %s", body)
	}
	return v
}

// submit makes a job for token and uploads its binary.
func submit(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	w := jdo(h, "POST", "/api/jobs", token, specJSON(exe, ""))
	if w.Code != 201 {
		t.Fatalf("submit: %d %s", w.Code, w.Body)
	}
	id := decodeJob(t, w).ID
	if w := jdo(h, "PUT", "/api/jobs/"+id+"/input", token, exe); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	return id
}

// Each token does its one job: the scopes are wire's, enforced by the
// dispatcher (TestEveryRouteEnforcesItsScope covers every route); this pins
// the queue's own rule on top, that a caller sees only its own jobs.
//
// Negative control (by hand, 1 Oct 2026): making callerJob skip the owner
// check fails "another caller's job"; making namedMatch match a name as well
// as a token fails "a token's name". Restored.
func TestJobsAuth(t *testing.T) {
	h, _, _ := jobsEnv(jobVars)
	id := submit(t, h, "tok-alice")
	for _, c := range []struct {
		name, method, path, token string
		want                      int
	}{
		{"no token", "GET", "/api/jobs/" + id, "", 401},
		{"wrong token", "GET", "/api/jobs/" + id, "tok-nobody", 401},
		{"a token's name is not a token", "GET", "/api/jobs/" + id, "alice", 401},
		{"another caller's job is not there", "GET", "/api/jobs/" + id, "tok-bob", 404},
		{"the owner sees it", "GET", "/api/jobs/" + id, "tok-alice", 200},
		{"the admin token does not read jobs", "GET", "/api/jobs/" + id, "tok-admin", 401},
		{"a caller cannot list", "GET", "/api/jobs", "tok-alice", 401},
		{"an admin can", "GET", "/api/jobs", "tok-admin", 200},
		{"a caller cannot claim", "POST", "/api/runner/claim", "tok-alice", 401},
		{"the runner cannot read a job", "GET", "/api/jobs/" + id, "tok-runner", 401},
		{"the runner token is not an MCP caller", "POST", "/api/mcp", "tok-runner", 401},
	} {
		if w := jdo(h, c.method, c.path, c.token, nil); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body, c.want)
		}
	}
}

// The admin token reads any caller's result files through its own route,
// and only there: a caller's token is refused on it, so a caller still
// cannot read another's files, and the admin token is refused on the
// caller's route. A job that does not exist, or a name that is not a result
// file, is 404 to the admin too.
//
// Negative control (by hand, 1 Oct 2026): making jobAdminFile pass
// ownedBy(env.jobOwner(r)), as jobFile does, fails "the admin reads alice's
// file" and "bob's" with 404; making jobFile pass anyOwner fails "bob cannot
// read alice's file on his" here and "another caller's file" in
// TestJobLifecycle. Restored.
func TestJobAdminFile(t *testing.T) {
	// No caller is not every caller (dropping `caller != ""` fails this).
	if ownedBy("")(&Job{}) {
		t.Fatal("an empty caller name owns a job with no owner")
	}
	h, _, _ := jobsEnv(jobVars)
	png := append([]byte("\x89PNG\r\n\x1a\n"), "pic"...)
	files := map[string]string{}
	for _, tok := range []string{"tok-alice", "tok-bob"} {
		id := submit(t, h, tok)
		if w := jdo(h, "POST", "/api/runner/claim", "tok-runner", nil); w.Code != 200 || decodeJob(t, w).ID != id {
			t.Fatalf("claim: %d %s", w.Code, w.Body)
		}
		r := "/api/runner/jobs/" + id
		body := append(append([]byte(nil), png...), tok...)
		if w := jdo(h, "PUT", r+"/files/desktop.png", "tok-runner", body, wire.HeaderJobSHA256, sum(body)); w.Code != 201 {
			t.Fatalf("file: %d %s", w.Code, w.Body)
		}
		if w := jdo(h, "POST", r+"/finish", "tok-runner", []byte(`{"exit_code":0,"outcome":"ok","files":["desktop.png"]}`)); w.Code != 200 {
			t.Fatalf("finish: %d %s", w.Code, w.Body)
		}
		files[tok] = id
	}
	admin := func(id, name string) string { return wire.MustFind(wire.RouteJobAdminFile).URL("", id, name) }
	for _, c := range []struct {
		name, path, token string
		want              int
		body              string
	}{
		{"the admin reads alice's file", admin(files["tok-alice"], "desktop.png"), "tok-admin", 200, string(png) + "tok-alice"},
		{"and bob's", admin(files["tok-bob"], "desktop.png"), "tok-admin", 200, string(png) + "tok-bob"},
		{"a job that does not exist", admin("0123456789abcdef0123456789abcdef", "desktop.png"), "tok-admin", 404, ""},
		{"a file the job does not have", admin(files["tok-alice"], "stdout.txt"), "tok-admin", 404, ""},
		{"a name that is not a result file", admin(files["tok-alice"], "input"), "tok-admin", 404, ""},
		{"bob cannot read alice's file on the admin route", admin(files["tok-alice"], "desktop.png"), "tok-bob", 401, ""},
		{"nor alice her own", admin(files["tok-alice"], "desktop.png"), "tok-alice", 401, ""},
		{"nor the runner", admin(files["tok-alice"], "desktop.png"), "tok-runner", 401, ""},
		{"bob cannot read alice's file on his", "/api/jobs/" + files["tok-alice"] + "/files/desktop.png", "tok-bob", 404, ""},
		{"the admin token is not a caller's", "/api/jobs/" + files["tok-alice"] + "/files/desktop.png", "tok-admin", 401, ""},
	} {
		w := jdo(h, "GET", c.path, c.token, nil)
		if w.Code != c.want || c.body != "" && w.Body.String() != c.body {
			t.Errorf("%s: %d %q, want %d %q", c.name, w.Code, w.Body, c.want, c.body)
		}
		if c.want == 200 && (w.Header().Get("Content-Type") != "image/png" || w.Header().Get(wire.HeaderJobSHA256) != sum([]byte(c.body))) {
			t.Errorf("%s: headers %v", c.name, w.Header())
		}
	}
}

func TestJobsUnconfiguredRefuses(t *testing.T) {
	h, b, _ := jobsEnv(nil)
	for _, p := range []string{"/api/jobs", "/api/runner/claim", "/api/mcp"} {
		if w := jdo(h, "POST", p, "anything", specJSON(exe, "")); w.Code != 503 {
			t.Errorf("POST %s with no secrets: %d, want 503", p, w.Code)
		}
	}
	if len(b.small) != 0 {
		t.Fatal("an unconfigured Worker wrote the index")
	}
}

func TestJobSpecRefusals(t *testing.T) {
	h, _, _ := jobsEnv(jobVars)
	for name, body := range map[string]string{
		"not json":   "nope",
		"bad kind":   `{"kind":"sh","name":"a.exe","size":1,"sha256":"` + sum(exe) + `"}`,
		"a path":     `{"kind":"app","name":"../a.exe","size":1,"sha256":"` + sum(exe) + `"}`,
		"not an exe": `{"kind":"app","name":"a.sh","size":1,"sha256":"` + sum(exe) + `"}`,
		"too big":    fmt.Sprintf(`{"kind":"app","name":"a.exe","size":%d,"sha256":"%s"}`, wire.MaxJobInput+1, sum(exe)),
		"no hash":    `{"kind":"app","name":"a.exe","size":1}`,
		"timeout":    `{"kind":"app","name":"a.exe","size":1,"sha256":"` + sum(exe) + `","timeout_s":99999}`,
	} {
		if w := jdo(h, "POST", "/api/jobs", "tok-alice", []byte(body)); w.Code != 400 {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body)
		}
	}
}

// The whole life of a job: submitted, uploaded (a damaged upload refused
// first), queued, claimed, logged, results stored, finished.
func TestJobLifecycle(t *testing.T) {
	h, _, now := jobsEnv(jobVars)
	w := jdo(h, "POST", "/api/jobs", "tok-alice", specJSON(exe, `,"gui":true,"args":["-x","{out}"]`))
	if w.Code != 201 {
		t.Fatalf("submit: %d %s", w.Code, w.Body)
	}
	j := decodeJob(t, w)
	if j.State != wire.JobUploading || j.Spec.TimeoutS != wire.DefJobTimeout || !strings.Contains(w.Body.String(), `"upload":"/api/jobs/`+j.ID+`/input"`) {
		t.Fatalf("submitted: %s", w.Body)
	}
	// Nothing to claim until the binary is in.
	if w := jdo(h, "POST", "/api/runner/claim", "tok-runner", nil); w.Code != 204 {
		t.Fatalf("claim before upload: %d", w.Code)
	}
	bad := append([]byte(nil), exe...)
	bad[0] ^= 1
	if w := jdo(h, "PUT", "/api/jobs/"+j.ID+"/input", "tok-alice", bad); w.Code != 400 {
		t.Fatalf("damaged upload: %d %s, want 400", w.Code, w.Body)
	}
	if w := jdo(h, "PUT", "/api/jobs/"+j.ID+"/input", "tok-alice", exe[:3]); w.Code != 400 {
		t.Fatalf("short upload: %d, want 400", w.Code)
	}
	if w := jdo(h, "PUT", "/api/jobs/"+j.ID+"/input", "tok-alice", exe); w.Code != 200 || decodeJob(t, w).State != wire.JobQueued {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := jdo(h, "PUT", "/api/jobs/"+j.ID+"/input", "tok-alice", exe); w.Code != 409 {
		t.Fatalf("second upload: %d, want 409", w.Code)
	}
	second := submit(t, h, "tok-bob")
	if v := decodeJob(t, jdo(h, "GET", "/api/jobs/"+second, "tok-bob", nil)); v.Position != 2 {
		t.Fatalf("second job's position: %d, want 2", v.Position)
	}

	*now = now.Add(time.Second)
	w = jdo(h, "POST", "/api/runner/claim", "tok-runner", nil, "X-Runner", "mac-1")
	if w.Code != 200 || decodeJob(t, w).ID != j.ID || decodeJob(t, w).Runner != "mac-1" {
		t.Fatalf("claim: %d %s (the oldest is %s)", w.Code, w.Body, j.ID)
	}
	if v := decodeJob(t, jdo(h, "GET", "/api/jobs/"+second, "tok-bob", nil)); v.Position != 1 || v.Running != 1 {
		t.Fatalf("after the claim: position %d running %d, want 1 and 1", v.Position, v.Running)
	}
	r := "/api/runner/jobs/" + j.ID
	if w := jdo(h, "GET", r+"/input", "tok-runner", nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), exe) {
		t.Fatalf("input: %d", w.Code)
	}
	log1 := []byte("clone job-x\n")
	if w := jdo(h, "PUT", r+"/log", "tok-runner", log1, wire.HeaderJobSHA256, sum(log1)); w.Code != 201 {
		t.Fatalf("log: %d %s", w.Code, w.Body)
	}
	log2 := append(log1, "running\n"...)
	jdo(h, "PUT", r+"/log", "tok-runner", log2, wire.HeaderJobSHA256, sum(log2))
	if w := jdo(h, "GET", "/api/jobs/"+j.ID+"/log?offset="+fmt.Sprint(len(log1)), "tok-alice", nil); w.Body.String() != "running\n" || w.Header().Get("X-Log-Size") != fmt.Sprint(len(log2)) {
		t.Fatalf("log from offset: %q size %s", w.Body, w.Header().Get("X-Log-Size"))
	}
	if w := jdo(h, "GET", "/api/jobs/"+j.ID+"/log?offset=999", "tok-alice", nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("log past the end: %d %q", w.Code, w.Body)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), "pic"...)
	if w := jdo(h, "PUT", r+"/files/desktop.png", "tok-runner", png, wire.HeaderJobSHA256, sum(png)); w.Code != 201 {
		t.Fatalf("file: %d %s", w.Code, w.Body)
	}
	if w := jdo(h, "PUT", r+"/files/run.sh", "tok-runner", png, wire.HeaderJobSHA256, sum(png)); w.Code != 400 {
		t.Fatalf("a file that is not a result type: %d, want 400", w.Code)
	}
	if w := jdo(h, "POST", r+"/finish", "tok-runner", []byte(`{"exit_code":1,"outcome":"failed","files":["missing.png"]}`)); w.Code != 400 {
		t.Fatalf("finish naming a file never stored: %d, want 400", w.Code)
	}
	w = jdo(h, "POST", r+"/finish", "tok-runner", []byte(`{"exit_code":1,"outcome":"failed","message":"hello.exe exited 3 in the guest","files":["desktop.png"]}`))
	v := decodeJob(t, w)
	if w.Code != 200 || v.State != wire.JobFinished || v.ExitCode == nil || *v.ExitCode != 1 || len(v.Files) != 1 || v.Files[0].Size != int64(len(png)) {
		t.Fatalf("finish: %d %s", w.Code, w.Body)
	}
	if w := jdo(h, "GET", "/api/jobs/"+j.ID+"/files/desktop.png", "tok-alice", nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), png) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("result file: %d %s", w.Code, w.Header())
	}
	if w := jdo(h, "GET", "/api/jobs/"+j.ID+"/files/desktop.png", "tok-bob", nil); w.Code != 404 {
		t.Fatalf("another caller's file: %d, want 404", w.Code)
	}
	// A finished job takes no more reports.
	if w := jdo(h, "POST", r+"/heartbeat", "tok-runner", nil); w.Code != 409 {
		t.Fatalf("heartbeat after finish: %d, want 409", w.Code)
	}
}

// Negative control (by hand, 1 Oct 2026): making mutate ignore Swap's
// answer (return on the first try whatever it said) loses the other
// caller's job and fails this. Restored.
func TestJobIndexRaceLosesNothing(t *testing.T) {
	h, b, _ := jobsEnv(jobVars)
	first := submit(t, h, "tok-alice")
	var second string
	b.beforeSwap = func() { second = submit(t, h, "tok-bob") }
	third := submit(t, h, "tok-alice")
	w := jdo(h, "GET", "/api/jobs", "tok-admin", nil)
	for _, id := range []string{first, second, third} {
		if !strings.Contains(w.Body.String(), id) {
			t.Errorf("job %s lost from the index: %s", id, w.Body)
		}
	}
}

func TestJobCancel(t *testing.T) {
	h, _, _ := jobsEnv(jobVars)
	queued := submit(t, h, "tok-alice")
	if w := jdo(h, "POST", "/api/jobs/"+queued+"/cancel", "tok-bob", nil); w.Code != 404 {
		t.Fatalf("cancelling another caller's job: %d, want 404", w.Code)
	}
	if v := decodeJob(t, jdo(h, "POST", "/api/jobs/"+queued+"/cancel", "tok-alice", nil)); v.State != wire.JobCancelled {
		t.Fatalf("cancelled while queued: %s", v.State)
	}
	if w := jdo(h, "POST", "/api/runner/claim", "tok-runner", nil); w.Code != 204 {
		t.Fatalf("a cancelled job was claimed: %d", w.Code)
	}
	running := submit(t, h, "tok-alice")
	jdo(h, "POST", "/api/runner/claim", "tok-runner", nil)
	r := "/api/runner/jobs/" + running
	if w := jdo(h, "POST", r+"/heartbeat", "tok-runner", nil); !strings.Contains(w.Body.String(), `"cancel":false`) {
		t.Fatalf("heartbeat: %s", w.Body)
	}
	if v := decodeJob(t, jdo(h, "POST", "/api/jobs/"+running+"/cancel", "tok-alice", nil)); v.State != wire.JobRunning || !v.Cancel {
		t.Fatalf("cancel while running: %+v", v)
	}
	if w := jdo(h, "POST", r+"/heartbeat", "tok-runner", nil); !strings.Contains(w.Body.String(), `"cancel":true`) {
		t.Fatalf("the Mac was not told to stop: %s", w.Body)
	}
	if v := decodeJob(t, jdo(h, "POST", r+"/finish", "tok-runner", []byte(`{"exit_code":7,"outcome":"not-run"}`))); v.State != wire.JobCancelled {
		t.Fatalf("finished after a cancel: %s", v.State)
	}
}

// Negative control (by hand, 1 Oct 2026): deleting the lease case in sweep
// leaves the job running and fails "lost"; deleting the archive Swap in
// mutate fails "archived". Restored.
func TestJobSweep(t *testing.T) {
	h, _, now := jobsEnv(jobVars)
	lost := submit(t, h, "tok-alice")
	jdo(h, "POST", "/api/runner/claim", "tok-runner", nil)
	waiting := submit(t, h, "tok-alice")
	w := jdo(h, "POST", "/api/jobs", "tok-alice", specJSON(exe, ""))
	noBinary := decodeJob(t, w).ID

	*now = now.Add(leaseFor + time.Second)
	if v := decodeJob(t, jdo(h, "GET", "/api/jobs/"+lost, "tok-alice", nil)); v.State != wire.JobLost {
		t.Fatalf("lost: %s, want %s", v.State, wire.JobLost)
	}
	if w := jdo(h, "POST", "/api/runner/jobs/"+lost+"/heartbeat", "tok-runner", nil); w.Code != 409 || !strings.Contains(w.Body.String(), wire.JobLost) {
		t.Fatalf("a heartbeat after the lease ran out: %d %s", w.Code, w.Body)
	}
	*now = now.Add(queueTTL)
	for _, id := range []string{waiting, noBinary} {
		if v := decodeJob(t, jdo(h, "GET", "/api/jobs/"+id, "tok-alice", nil)); v.State != wire.JobExpired {
			t.Fatalf("%s: %s, want expired", id, v.State)
		}
	}
	*now = now.Add(keepFor + time.Hour)
	submit(t, h, "tok-bob") // a write, which retires the old ones
	if w := jdo(h, "GET", "/api/jobs", "tok-admin", nil); strings.Contains(w.Body.String(), lost) {
		t.Fatalf("a day-old job is still in the index: %s", w.Body)
	}
	if v := decodeJob(t, jdo(h, "GET", "/api/jobs/"+lost, "tok-alice", nil)); v.State != wire.JobLost {
		t.Fatalf("archived: %s, want it readable as %s", v.State, wire.JobLost)
	}
}

func TestJobPerCallerLimit(t *testing.T) {
	h, _, _ := jobsEnv(jobVars)
	for range maxQueued {
		submit(t, h, "tok-alice")
	}
	if w := jdo(h, "POST", "/api/jobs", "tok-alice", specJSON(exe, "")); w.Code != 429 {
		t.Fatalf("over the limit: %d, want 429", w.Code)
	}
	if w := jdo(h, "POST", "/api/jobs", "tok-bob", specJSON(exe, "")); w.Code != 201 {
		t.Fatalf("another caller is not limited by alice: %d", w.Code)
	}
}

func rpc(t *testing.T, h http.Handler, token, method string, params any) map[string]any {
	t.Helper()
	p, _ := json.Marshal(params)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, p)
	w := jdo(h, "POST", "/api/mcp", token, []byte(body))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %d %s", method, w.Code, w.Body)
	}
	return out
}

func toolText(t *testing.T, res map[string]any) (string, bool) {
	t.Helper()
	r, _ := res["result"].(map[string]any)
	c, _ := r["content"].([]any)
	if len(c) == 0 {
		t.Fatalf("no content: %v", res)
	}
	first, _ := c[0].(map[string]any)
	s, _ := first["text"].(string)
	isErr, _ := r["isError"].(bool)
	return s, isErr
}

func TestJobsMCP(t *testing.T) {
	h, _, _ := jobsEnv(jobVars)
	init := rpc(t, h, "tok-alice", "initialize", map[string]any{"protocolVersion": mcpProtocol})
	if r, _ := init["result"].(map[string]any); r["protocolVersion"] != mcpProtocol {
		t.Fatalf("initialize: %v", init)
	}
	if w := jdo(h, "POST", "/api/mcp", "tok-alice", []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); w.Code != 202 {
		t.Fatalf("a notification: %d, want 202", w.Code)
	}
	list := rpc(t, h, "tok-alice", "tools/list", map[string]any{})
	if !strings.Contains(fmt.Sprint(list), "submit_job") {
		t.Fatalf("tools/list: %v", list)
	}
	s, isErr := toolText(t, rpc(t, h, "tok-alice", "tools/call", map[string]any{
		"name": "submit_job", "arguments": map[string]any{"kind": "app", "name": "hello.exe", "data_base64": base64.StdEncoding.EncodeToString(exe)},
	}))
	if isErr || !strings.Contains(s, `"state":"queued"`) {
		t.Fatalf("submit_job: %s", s)
	}
	var j wire.JobView
	_ = json.Unmarshal([]byte(s), &j)
	if s, isErr := toolText(t, rpc(t, h, "tok-alice", "tools/call", map[string]any{"name": "job_result", "arguments": map[string]any{"id": j.ID}})); !isErr || !strings.Contains(s, "queued") {
		t.Fatalf("job_result before it ran must say so and be an error: %s", s)
	}
	if _, isErr := toolText(t, rpc(t, h, "tok-bob", "tools/call", map[string]any{"name": "job_status", "arguments": map[string]any{"id": j.ID}})); !isErr {
		t.Fatal("job_status on another caller's job succeeded")
	}
	if w := jdo(h, "GET", "/api/runner/jobs/"+j.ID+"/input", "tok-runner", nil); w.Code != 409 {
		// Queued, not running: the runner may not read it yet.
		t.Fatalf("input of a queued job: %d, want 409", w.Code)
	}
	jdo(h, "POST", "/api/runner/claim", "tok-runner", nil)
	if w := jdo(h, "GET", "/api/runner/jobs/"+j.ID+"/input", "tok-runner", nil); !bytes.Equal(w.Body.Bytes(), exe) {
		t.Fatalf("the binary sent inline did not arrive intact: %d", w.Code)
	}
}
