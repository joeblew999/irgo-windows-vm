package wire

import (
	"net/http"
	"strings"
	"time"
)

// The remote job queue: a developer, an agent or a workflow on any OS hands
// a Windows binary to a Mac running `irgo-winvm serve`, which runs it on a
// fresh clone and sends the result back (worker/jobs.go, internal/remote).
// Its routes, scopes, codes and types, appended to the tables in init so the
// queue reads as one place.

// Route names.
const (
	RouteJobSubmit       = "job-submit"
	RouteJobInput        = "job-input"
	RouteJobGet          = "job-get"
	RouteJobLog          = "job-log"
	RouteJobFile         = "job-file"
	RouteJobCancel       = "job-cancel"
	RouteJobList         = "job-list"
	RouteRunnerClaim     = "runner-claim"
	RouteRunnerHeartbeat = "runner-heartbeat"
	RouteRunnerInput     = "runner-input"
	RouteRunnerLog       = "runner-log"
	RouteRunnerFile      = "runner-file"
	RouteRunnerFinish    = "runner-finish"
	RouteJobsMCP         = "jobs-mcp"
)

// Scopes. A caller's token submits and reads its own jobs, the admin's lists
// them all, the runner's (the Mac's) takes and reports them; none does
// another's job.
const (
	ScopeJobs       Scope = "jobs"
	ScopeJobsAdmin  Scope = "jobs-admin"
	ScopeJobsRunner Scope = "jobs-runner"
)

// Named reports whether the scope's secret holds one token per caller,
// name=token,name=token, rather than one token. A bare token is the caller
// "caller". The name is recorded as each job's owner.
func (s Scope) Named() bool { return s == ScopeJobs }

// EnvRemoteURL is where a client finds the Worker for remote jobs.
const EnvRemoteURL = "IRGO_REMOTE_URL"

// Codes the queue adds.
const (
	CodeTooMany Code = "too-many"
	CodeBusy    Code = "busy"
)

// Headers the queue uses.
const (
	HeaderJobSHA256 = "X-Job-Sha256" // a stored file's SHA-256: claimed on PUT, verified on GET
	HeaderLogSize   = "X-Log-Size"   // the log's whole size: the offset to ask from next
	HeaderRunner    = "X-Runner"     // the Mac's name, on the runner's requests
)

// Limits. The binary is one request, and Cloudflare refuses a body over
// 100 MB on the Free and Pro plans before the Worker sees it.
const (
	MaxJobInput   = 95 << 20
	MaxJobFile    = 32 << 20
	MaxJobLog     = 2 << 20
	MaxJobFiles   = 64
	MaxJobArgs    = 64
	MaxJobArgLen  = 4096
	MaxJobTimeout = 60 * 60 // seconds the program may run in the guest
	DefJobTimeout = 10 * 60
	MaxJobSpec    = 64 << 10
	MaxJobMCP     = 24 << 20 // a tool call carrying a binary of up to 16 MiB in base64
)

// Job kinds.
const (
	JobKindApp  = "app"  // an .exe: run, its output and exit code back
	JobKindTest = "test" // a `go test -c` binary: run with -test.v=test2json, events back
)

// Job states. The first three are live; the rest are final.
const (
	JobUploading = "uploading"
	JobQueued    = "queued"
	JobRunning   = "running"
	JobFinished  = "finished"  // it ran; ExitCode says how it went
	JobCancelled = "cancelled" // by its caller
	JobExpired   = "expired"   // no binary, or no Mac took it in time
	JobLost      = "lost"      // its Mac stopped heartbeating
	JobTimedOut  = "timed-out" // its Mac never finished it
)

// JobFinal reports whether a job in state s has ended.
func JobFinal(s string) bool { return s != JobUploading && s != JobQueued && s != JobRunning }

// JobSpec is what a caller asks for.
type JobSpec struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"` // the binary's file name, *.exe
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	GUI      bool     `json:"gui,omitempty"`
	Args     []string `json:"args,omitempty"`
	TimeoutS int      `json:"timeout_s,omitempty"`
}

// Job is a job as the Worker keeps it.
type Job struct {
	ID       string    `json:"id"`
	Owner    string    `json:"owner"`
	Spec     JobSpec   `json:"spec"`
	State    string    `json:"state"`
	Created  time.Time `json:"created"`
	Queued   time.Time `json:"queued,omitzero"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Runner   string    `json:"runner,omitempty"`
	Lease    time.Time `json:"lease,omitzero"`
	Cancel   bool      `json:"cancel_requested,omitempty"`

	ExitCode *int       `json:"exit_code,omitempty"` // the Mac's command's exit code
	Outcome  string     `json:"outcome,omitempty"`   // its name on the tool's exit-code table
	Message  string     `json:"message,omitempty"`
	Files    []BlobInfo `json:"files,omitempty"` // Key is the file's name
	Why      string     `json:"why,omitempty"`   // why a job ended without running
}

// JobView is a job as a caller sees it: with its place in the queue.
type JobView struct {
	Job
	Position int `json:"position,omitempty"` // 1 is next; 0 when not queued
	Running  int `json:"running"`            // jobs running now, on any Mac
}

// JobCreated is the answer to a submitted spec.
type JobCreated struct {
	Job    JobView `json:"job"`
	Upload string  `json:"upload"` // where to PUT the binary
}

// JobList is every job in the index.
type JobList struct {
	Jobs []JobView `json:"jobs"`
}

// JobHeartbeat is the answer to the runner's heartbeat.
type JobHeartbeat struct {
	Cancel bool   `json:"cancel"`
	State  string `json:"state"`
}

// JobResult is how a job ended on the Mac. Every file it names must have
// been stored first.
type JobResult struct {
	ExitCode int      `json:"exit_code"`
	Outcome  string   `json:"outcome"`
	Message  string   `json:"message,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// IsJobID is a job's id: 32 lower-case hex digits, 128 random bits.
func IsJobID(s string) bool { return IsHex(s, 32) }

// IsSafeName is [A-Za-z0-9_.-]{1,100}, not starting with a dot: a file
// name, never a path.
func IsSafeName(s string) bool {
	if s == "" || len(s) > 100 || s[0] == '.' {
		return false
	}
	return AllBytes(s, func(c byte) bool {
		return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
	})
}

// JobFileTypes are the result files a Mac may store, by extension.
var JobFileTypes = map[string]string{
	".png": "image/png", ".json": TypeJSON, ".txt": "text/plain; charset=utf-8",
}

// JobFileType is a result file's media type; ok is false for a name that is
// not one.
func JobFileType(name string) (string, bool) {
	if !IsSafeName(name) {
		return "", false
	}
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	ct, ok := JobFileTypes[name[i:]]
	return ct, ok
}

var (
	jobIDParam   = Param{"id", "path", "the job's id: 32 hex digits"}
	jobFileParam = Param{"name", "path", "a result file: result.json, stdout.txt, test2json.json, desktop.png, shot-*.png"}
	jobErrs      = []Code{CodeNotFound, CodeStorage, CodeInternal}
	jobCommands  = []string{"remote-submit", "remote-status", "remote-logs", "remote-result", "remote-cancel"}
)

func init() {
	Scopes = append(Scopes,
		ScopeInfo{ScopeJobs, "JOBS_TOKENS", "IRGO_REMOTE_TOKEN", "a remote job's caller: submits and reads its own jobs (the secret is name=token,name=token, one per caller)"},
		ScopeInfo{ScopeJobsAdmin, "JOBS_ADMIN_TOKEN", "IRGO_REMOTE_ADMIN_TOKEN", "lists every job in the queue"},
		ScopeInfo{ScopeJobsRunner, "JOBS_RUNNER_TOKEN", "IRGO_REMOTE_RUNNER_TOKEN", "the Mac running irgo-winvm serve: takes jobs and reports them"},
	)
	Codes = append(Codes,
		CodeInfo{CodeTooMany, http.StatusTooManyRequests, "the caller already has the most live jobs it may"},
		CodeInfo{CodeBusy, http.StatusServiceUnavailable, "the job queue changed under every attempt to update it; nothing was stored, ask again"},
	)
	Routes = append(Routes,
		Route{
			Name: RouteJobSubmit, Method: http.MethodPost, Path: "/api/jobs", Scope: ScopeJobs,
			Summary: "submit a job spec; the job waits for its binary at the answer's upload path",
			Request: JobSpec{}, RequestType: TypeJSON, MaxBody: MaxJobSpec,
			Success: http.StatusCreated, Response: JobCreated{}, ResponseType: TypeJSON,
			Errors: []Code{CodeBadRequest, CodeTooMany, CodeBusy, CodeStorage, CodeInternal}, Commands: []string{"remote-submit"},
		},
		Route{
			Name: RouteJobInput, Method: http.MethodPut, Path: "/api/jobs/{id}/input", Scope: ScopeJobs,
			Summary: "the job's binary, stored only if R2 finds it hashes to the spec's sha256; the job is then queued",
			Params:  []Param{jobIDParam}, RequestType: TypeOctets, MaxBody: MaxJobInput, NeedLength: true,
			Success: http.StatusOK, Response: JobView{}, ResponseType: TypeJSON,
			Errors: []Code{CodeNotFound, CodeConflict, CodeBadRequest, CodeDigestMismatch, CodeBusy, CodeStorage, CodeInternal}, Commands: []string{"remote-submit"},
		},
		Route{
			Name: RouteJobGet, Method: http.MethodGet, Path: "/api/jobs/{id}", Scope: ScopeJobs,
			Summary: "the job, its place in the queue and how many are running; another caller's job is 404, as one that does not exist",
			Params:  []Param{jobIDParam},
			Success: http.StatusOK, Response: JobView{}, ResponseType: TypeJSON, Errors: jobErrs, Commands: jobCommands,
		},
		Route{
			Name: RouteJobLog, Method: http.MethodGet, Path: "/api/jobs/{id}/log", Scope: ScopeJobs,
			Summary: "what the Mac has said about the job, from byte offset; " + HeaderLogSize + " is the offset to ask from next",
			Params:  []Param{jobIDParam, {"offset", "query", "bytes already read; default 0"}},
			Success: http.StatusOK, ResponseType: "text/plain",
			ResponseHeaders: []Param{{HeaderLogSize, "header", "the log's whole size"}},
			Errors:          []Code{CodeBadRequest, CodeNotFound, CodeStorage, CodeInternal}, Commands: []string{"remote-submit", "remote-logs"},
		},
		Route{
			Name: RouteJobFile, Method: http.MethodGet, Path: "/api/jobs/{id}/files/{name}", Scope: ScopeJobs,
			Summary: "one result file of the job",
			Params:  []Param{jobIDParam, jobFileParam},
			Success: http.StatusOK, ResponseType: "image/png, application/json or text/plain",
			ResponseHeaders: []Param{{HeaderJobSHA256, "header", "its SHA-256 as stored"}},
			Errors:          jobErrs, Commands: []string{"remote-submit", "remote-result"},
		},
		Route{
			Name: RouteJobCancel, Method: http.MethodPost, Path: "/api/jobs/{id}/cancel", Scope: ScopeJobs,
			Summary: "cancel the job: at once while it waits; a running one is stopped by its Mac at its next step",
			Params:  []Param{jobIDParam},
			Success: http.StatusOK, Response: JobView{}, ResponseType: TypeJSON,
			Errors: []Code{CodeNotFound, CodeConflict, CodeBusy, CodeStorage, CodeInternal}, Commands: []string{"remote-cancel"},
		},
		Route{
			Name: RouteJobList, Method: http.MethodGet, Path: "/api/jobs", Scope: ScopeJobsAdmin,
			Summary: "every job in the index, live and ended in the last day",
			Success: http.StatusOK, Response: JobList{}, ResponseType: TypeJSON,
			Errors: []Code{CodeStorage, CodeInternal}, Commands: []string{"remote-status"},
		},
		Route{
			Name: RouteRunnerClaim, Method: http.MethodPost, Path: "/api/runner/claim", Scope: ScopeJobsRunner,
			Summary: "the oldest queued job, now running on this Mac with a 90 s lease; 204 when there is none",
			Headers: []Param{{HeaderRunner, "header", "the Mac's name, recorded on the job"}},
			Success: http.StatusOK, Also: []int{http.StatusNoContent}, Response: JobView{}, ResponseType: TypeJSON,
			Errors: []Code{CodeBusy, CodeStorage, CodeInternal}, Commands: []string{"serve"},
		},
		Route{
			Name: RouteRunnerHeartbeat, Method: http.MethodPost, Path: "/api/runner/jobs/{id}/heartbeat", Scope: ScopeJobsRunner,
			Summary: "renew the job's lease and learn whether it was cancelled; 409 once it is not running, which stops the Mac",
			Params:  []Param{jobIDParam},
			Success: http.StatusOK, Response: JobHeartbeat{}, ResponseType: TypeJSON,
			Errors: []Code{CodeConflict, CodeBusy, CodeStorage, CodeInternal}, Commands: []string{"serve"},
		},
		Route{
			Name: RouteRunnerInput, Method: http.MethodGet, Path: "/api/runner/jobs/{id}/input", Scope: ScopeJobsRunner,
			Summary: "the running job's binary",
			Params:  []Param{jobIDParam},
			Success: http.StatusOK, ResponseType: TypeOctets,
			Errors: []Code{CodeConflict, CodeNotFound, CodeStorage, CodeInternal}, Commands: []string{"serve"},
		},
		Route{
			Name: RouteRunnerLog, Method: http.MethodPut, Path: "/api/runner/jobs/{id}/log", Scope: ScopeJobsRunner,
			Summary:     "replace the job's log with everything said so far",
			Params:      []Param{jobIDParam},
			Headers:     []Param{{HeaderJobSHA256, "header", "required: the body's SHA-256"}},
			RequestType: "text/plain", MaxBody: MaxJobLog, NeedLength: true,
			Success: http.StatusCreated, Response: BlobInfo{}, ResponseType: TypeJSON,
			Errors: []Code{CodeConflict, CodeBadRequest, CodeDigestMismatch, CodeStorage, CodeInternal}, Commands: []string{"serve"},
		},
		Route{
			Name: RouteRunnerFile, Method: http.MethodPut, Path: "/api/runner/jobs/{id}/files/{name}", Scope: ScopeJobsRunner,
			Summary:     "store one result file",
			Params:      []Param{jobIDParam, jobFileParam},
			Headers:     []Param{{HeaderJobSHA256, "header", "required: the body's SHA-256"}},
			RequestType: TypeOctets, MaxBody: MaxJobFile, NeedLength: true,
			Success: http.StatusCreated, Response: BlobInfo{}, ResponseType: TypeJSON,
			Errors: []Code{CodeConflict, CodeBadRequest, CodeDigestMismatch, CodeStorage, CodeInternal}, Commands: []string{"serve"},
		},
		Route{
			Name: RouteRunnerFinish, Method: http.MethodPost, Path: "/api/runner/jobs/{id}/finish", Scope: ScopeJobsRunner,
			Summary: "how the job ended: the exit code, its outcome, the files stored",
			Params:  []Param{jobIDParam},
			Request: JobResult{}, RequestType: TypeJSON, MaxBody: MaxJobSpec,
			Success: http.StatusOK, Response: JobView{}, ResponseType: TypeJSON,
			Errors: []Code{CodeConflict, CodeBadRequest, CodeBusy, CodeStorage, CodeInternal}, Commands: []string{"serve"},
		},
		Route{
			Name: RouteJobsMCP, Method: http.MethodPost, Path: "/api/mcp", Scope: ScopeJobs,
			Summary: "the job queue as a remote MCP server: Streamable HTTP, stateless, one JSON-RPC request per POST. " +
				"Tools submit_job, job_status, job_log, job_result, cancel_job, each one request to the routes above with the caller's token",
			RequestType: TypeJSON, MaxBody: MaxJobMCP,
			Success: http.StatusOK, Also: []int{http.StatusAccepted}, ResponseType: "application/json (a JSON-RPC 2.0 answer)",
			Errors: []Code{CodeBadRequest},
		},
	)
}
