package main

// The remote job queue: a developer, an agent or a GitHub workflow on any OS
// submits a Windows binary, a Mac running `irgo-winvm serve` takes it, runs it
// on a fresh clone of the golden image, and sends the result back. The Mac
// only ever connects out, to here; it accepts no connection.
//
// Callers (JOBS_TOKENS, one token per caller; JOBS_ADMIN_TOKEN sees them all):
//
//	POST /api/jobs                       submit a spec; answers the job and where to upload
//	PUT  /api/jobs/{id}/input            the binary, streamed, checked against the spec's SHA-256
//	GET  /api/jobs/{id}                  the job, with its place in the queue
//	GET  /api/jobs/{id}/log?offset=N     what the Mac has said about it, from byte N
//	GET  /api/jobs/{id}/files/{name}     one result: result.json, stdout.txt, test2json.json, *.png
//	POST /api/jobs/{id}/cancel           queued: cancelled now; running: the Mac stops it
//	GET  /api/jobs                       every job in the index (admin only)
//
// The Mac (JOBS_RUNNER_TOKEN):
//
//	POST /api/runner/claim                     the oldest queued job, now running; 204 if none
//	POST /api/runner/jobs/{id}/heartbeat       keeps the lease; answers whether to cancel
//	GET  /api/runner/jobs/{id}/input           the binary
//	PUT  /api/runner/jobs/{id}/log             the whole log so far
//	PUT  /api/runner/jobs/{id}/files/{name}    one result file
//	POST /api/runner/jobs/{id}/finish          the exit code and the files
//
// Every live job is in one small object, jobs/index.json, changed only by a
// compare-and-swap (CAS), so two submits, a claim and a cancel can race and
// none is lost. The binaries and results are separate objects beside it.
// A submitter sees only its own jobs, by id: another caller's id answers 404,
// exactly as an id that does not exist.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The secrets the queue reads. docs/DEVELOPMENT.md, "The remote job queue".
const (
	varJobTokens      = "JOBS_TOKENS"       // name=token,name=token: one per caller
	varJobAdminToken  = "JOBS_ADMIN_TOKEN"  // every job, and the list
	varJobRunnerToken = "JOBS_RUNNER_TOKEN" // the Mac that runs them
)

// Limits. The input is one request, and Cloudflare refuses a body over
// 100 MB on the Free and Pro plans before the Worker sees it.
const (
	maxJobInput   = 95 << 20
	maxJobFile    = 32 << 20
	maxJobLog     = 2 << 20
	maxJobFiles   = 64
	maxJobArgs    = 64
	maxJobArgLen  = 4096
	maxJobTimeout = 60 * 60 // seconds the program may run in the guest
	defJobTimeout = 10 * 60
	maxIndexJobs  = 500 // every live and recent job, all callers
	maxQueued     = 20  // per caller

	// leaseFor is how long a running job survives without a heartbeat from
	// its Mac; it heartbeats every 20 s. A job whose lease runs out is lost.
	leaseFor = 90 * time.Second
	// uploadTTL and queueTTL expire a job that never got its binary, or that
	// no Mac took.
	uploadTTL = 30 * time.Minute
	queueTTL  = 2 * time.Hour
	// overhead is what a job takes beyond its own timeout: the clone and its
	// boot (23 s measured, minutes when Windows is slow), the push, the
	// screenshots, the delete. Past timeout + overhead the Worker gives up on it.
	overhead = 20 * time.Minute
	// keepFor is how long a finished job stays in the index. After that its
	// record is archived beside its files, which a lifecycle rule on the
	// bucket deletes.
	keepFor = 24 * time.Hour

	jobIndexKey = "jobs/index.json"
)

// Job states. The first three are live; the rest are final.
const (
	stUploading = "uploading" // submitted, binary not yet received
	stQueued    = "queued"
	stRunning   = "running"
	stFinished  = "finished"  // it ran; ExitCode says how it went
	stCancelled = "cancelled" // by its caller or an admin
	stExpired   = "expired"   // no binary, or no Mac took it in time
	stLost      = "lost"      // its Mac stopped heartbeating
	stTimedOut  = "timed-out" // its Mac never finished it
)

func finalState(s string) bool {
	return s != stUploading && s != stQueued && s != stRunning
}

// JobSpec is what a caller asks for. internal/remote sends the same JSON.
type JobSpec struct {
	Kind     string   `json:"kind"` // "app" or "test" (a `go test -c` binary)
	Name     string   `json:"name"` // the binary's file name, *.exe
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	GUI      bool     `json:"gui,omitempty"`
	Args     []string `json:"args,omitempty"`
	TimeoutS int      `json:"timeout_s,omitempty"`
}

// Job is one job as the index holds it.
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

	ExitCode *int       `json:"exit_code,omitempty"` // the tool's exit code on the Mac
	Outcome  string     `json:"outcome,omitempty"`   // its name in command.Outcomes
	Message  string     `json:"message,omitempty"`
	Files    []BlobInfo `json:"files,omitempty"`
	Why      string     `json:"why,omitempty"` // why a job ended without running
}

// jobView is a job as a caller sees it: with its place in the queue.
type jobView struct {
	Job
	Position int `json:"position,omitempty"` // 1 is next; 0 when not queued
	Running  int `json:"running"`            // jobs running now, on any Mac
}

type jobIndex struct {
	Jobs []Job `json:"jobs"`
}

func (ix *jobIndex) find(id string) *Job {
	for i := range ix.Jobs {
		if ix.Jobs[i].ID == id {
			return &ix.Jobs[i]
		}
	}
	return nil
}

func (ix *jobIndex) view(j Job) jobView {
	v := jobView{Job: j}
	var queued []Job
	for _, o := range ix.Jobs {
		switch o.State {
		case stQueued:
			queued = append(queued, o)
		case stRunning:
			v.Running++
		}
	}
	if j.State == stQueued {
		sort.SliceStable(queued, func(a, b int) bool { return queued[a].Queued.Before(queued[b].Queued) })
		for i, o := range queued {
			if o.ID == j.ID {
				v.Position = i + 1
			}
		}
	}
	return v
}

// sweep ends what time has ended, and returns the jobs old enough to leave
// the index. It is a function of the index and the clock alone, so a reader
// can apply it to what it shows without writing, and the next writer
// persists the same answer.
func (ix *jobIndex) sweep(now time.Time) (gone []Job) {
	// A job ends at its deadline, not when someone noticed: a reader's sweep
	// is not stored, so "now" would differ between that read and the write
	// that later persists the same answer (TestJobSweep caught it).
	end := func(j *Job, state, why string, at time.Time) {
		j.State, j.Why, j.Finished = state, why, at
	}
	keep := ix.Jobs[:0:0]
	for _, j := range ix.Jobs {
		limit := j.Started.Add(time.Duration(j.Spec.TimeoutS)*time.Second + overhead)
		switch {
		case j.State == stUploading && now.Sub(j.Created) > uploadTTL:
			end(&j, stExpired, "the binary was never uploaded", j.Created.Add(uploadTTL))
		case j.State == stQueued && now.Sub(j.Queued) > queueTTL:
			end(&j, stExpired, fmt.Sprintf("no Mac took it within %s", queueTTL), j.Queued.Add(queueTTL))
		case j.State == stRunning && now.After(j.Lease) && j.Lease.Before(limit):
			end(&j, stLost, fmt.Sprintf("its Mac (%s) stopped reporting", j.Runner), j.Lease)
		case j.State == stRunning && now.After(limit):
			end(&j, stTimedOut, fmt.Sprintf("its Mac (%s) did not finish it within its timeout plus %s", j.Runner, overhead), limit)
		}
		if finalState(j.State) && now.Sub(j.Finished) > keepFor {
			gone = append(gone, j)
			continue
		}
		keep = append(keep, j)
	}
	ix.Jobs = keep
	return gone
}

// errContention is a CAS lost too many times in a row.
var errContention = errors.New("the job index changed under every attempt; try again")

// errJob is a refusal the caller can act on, with the status to answer.
type errJob struct {
	code int
	msg  string
}

func (e errJob) Error() string { return e.msg }

func refuse(code int, format string, a ...any) error {
	return errJob{code, fmt.Sprintf(format, a...)}
}

// loadIndex reads the index and sweeps it, without writing.
func loadIndex(b JobBucket, now time.Time) (jobIndex, string, bool, error) {
	var ix jobIndex
	body, etag, ok, err := b.Load(jobIndexKey)
	if err != nil || !ok {
		return ix, "", ok, err
	}
	if err := json.Unmarshal(body, &ix); err != nil {
		// Cannot tell what is queued: refuse rather than start again empty
		// and drop every job in it.
		return ix, "", false, fmt.Errorf("%s does not parse (%v); an admin must repair or delete it", jobIndexKey, err)
	}
	ix.sweep(now)
	return ix, etag, true, nil
}

// mutate applies fn to the index and stores it with a compare-and-swap,
// again from a fresh read whenever another writer got there first. Jobs the
// sweep retires are archived beside their files before they leave the index.
func (env Env) mutate(b JobBucket, fn func(ix *jobIndex, now time.Time) error) (jobIndex, error) {
	for attempt := range 16 {
		if attempt > 0 {
			// Jittered, growing: twelve submits at once on R2 lost one after
			// ten immediate retries (measured live, 1 Oct 2026).
			pause(time.Duration(attempt*attempt*5+mrand.IntN(20)) * time.Millisecond)
		}
		now := env.Now().UTC()
		var ix jobIndex
		body, etag, ok, err := b.Load(jobIndexKey)
		if err != nil {
			return ix, err
		}
		if ok {
			if err := json.Unmarshal(body, &ix); err != nil {
				return ix, fmt.Errorf("%s does not parse (%v); an admin must repair or delete it", jobIndexKey, err)
			}
		}
		for _, j := range ix.sweep(now) {
			rec, _ := json.Marshal(j)
			// Create-only: a retry after a lost swap finds it written.
			if _, err := b.Swap(jobArchiveKey(j.ID), rec, ""); err != nil {
				return ix, err
			}
		}
		if err := fn(&ix, now); err != nil {
			return ix, err
		}
		out, err := json.Marshal(ix)
		if err != nil {
			return ix, err
		}
		if !ok {
			etag = ""
		}
		stored, err := b.Swap(jobIndexKey, out, etag)
		if err != nil {
			return ix, err
		}
		if stored {
			return ix, nil
		}
	}
	return jobIndex{}, errContention
}

func jobArchiveKey(id string) string    { return "jobs/" + id + "/job.json" }
func jobInputKey(id string) string      { return "jobs/" + id + "/input.exe" }
func jobLogKey(id string) string        { return "jobs/" + id + "/log.txt" }
func jobFileKey(id, name string) string { return "jobs/" + id + "/files/" + name }
func isJobID(s string) bool             { return isHex(s, 32) }
func newJobID() (string, error)         { return randHex(16) }
func jobPath(id string, rest ...string) string {
	return "/api/jobs/" + strings.Join(append([]string{id}, rest...), "/")
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isSafeName is [A-Za-z0-9_.-]{1,100}, not starting with a dot: a file name,
// never a path.
func isSafeName(s string) bool {
	if s == "" || len(s) > 100 || s[0] == '.' {
		return false
	}
	return allBytes(s, func(c byte) bool {
		return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
	})
}

// jobFileTypes are the result files a Mac may store, by extension.
var jobFileTypes = map[string]string{
	".png": "image/png", ".json": "application/json", ".txt": "text/plain; charset=utf-8",
}

func jobFileType(name string) (string, bool) {
	if !isSafeName(name) {
		return "", false
	}
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	ct, ok := jobFileTypes[name[i:]]
	return ct, ok
}

// check validates a spec and fills its defaults.
func (s *JobSpec) check() error {
	switch {
	case s.Kind != "app" && s.Kind != "test":
		return fmt.Errorf(`kind must be "app" or "test", not %q`, s.Kind)
	case !isSafeName(s.Name) || !strings.HasSuffix(strings.ToLower(s.Name), ".exe"):
		return fmt.Errorf("name %q must be a plain file name ending in .exe", s.Name)
	case s.Size <= 0 || s.Size > maxJobInput:
		return fmt.Errorf("size %d must be between 1 and %d bytes", s.Size, maxJobInput)
	case !isHex(s.SHA256, 64):
		return errors.New("sha256 must be 64 lower-case hex digits")
	case len(s.Args) > maxJobArgs:
		return fmt.Errorf("%d arguments, over %d", len(s.Args), maxJobArgs)
	case s.TimeoutS < 0 || s.TimeoutS > maxJobTimeout:
		return fmt.Errorf("timeout_s %d must be between 1 and %d", s.TimeoutS, maxJobTimeout)
	}
	for _, a := range s.Args {
		if len(a) > maxJobArgLen || strings.ContainsRune(a, 0) {
			return fmt.Errorf("an argument is over %d bytes or holds a NUL", maxJobArgLen)
		}
	}
	if s.TimeoutS == 0 {
		s.TimeoutS = defJobTimeout
	}
	return nil
}

// Roles a token can have.
type jobRole int

const (
	roleSubmit jobRole = iota + 1
	roleAdmin
	roleRunner
)

// jobCaller authenticates a request to the queue. With no queue secret set
// at all, everything is refused with 503: an unconfigured Worker does not
// fall open. Every configured token is compared, each in constant time over
// its SHA-256, so the time taken does not say which one nearly matched.
func (env Env) jobCaller(w http.ResponseWriter, r *http.Request) (string, jobRole, bool) {
	type cred struct {
		name string
		role jobRole
		sum  [32]byte
	}
	var creds []cred
	for _, pair := range strings.Split(env.Var(varJobTokens), ",") {
		name, tok, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && name != "" && tok != "" {
			creds = append(creds, cred{name, roleSubmit, sha256.Sum256([]byte(tok))})
		}
	}
	if t := env.Var(varJobAdminToken); t != "" {
		creds = append(creds, cred{"admin", roleAdmin, sha256.Sum256([]byte(t))})
	}
	if t := env.Var(varJobRunnerToken); t != "" {
		creds = append(creds, cred{"runner", roleRunner, sha256.Sum256([]byte(t))})
	}
	if len(creds) == 0 {
		fail(w, http.StatusServiceUnavailable, "refused: the job queue is not configured on this Worker (%s, %s, %s)", varJobTokens, varJobAdminToken, varJobRunnerToken)
		return "", 0, false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(got))
	var who string
	var role jobRole
	for _, c := range creds {
		if subtle.ConstantTimeCompare(sum[:], c.sum[:]) == 1 && ok && got != "" {
			who, role = c.name, c.role
		}
	}
	if role == 0 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="irgo-windows-vm"`)
		fail(w, http.StatusUnauthorized, "refused: no valid bearer token")
		return "", 0, false
	}
	return who, role, true
}

// jobs routes /api/jobs/* and /api/runner/*. p is the path after /api/.
func (env Env) jobs(w http.ResponseWriter, r *http.Request, p []string) {
	who, role, ok := env.jobCaller(w, r)
	if !ok {
		return
	}
	b, err := env.Jobs()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the JOBS bucket: %v", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	m := r.Method
	if p[0] == "runner" {
		if role != roleRunner {
			fail(w, http.StatusForbidden, "refused: only the runner token may use /api/runner")
			return
		}
		runner := r.Header.Get("X-Runner")
		if !isSafeName(runner) {
			runner = "mac"
		}
		switch {
		case m == http.MethodPost && len(p) == 2 && p[1] == "claim":
			env.jobClaim(w, b, runner)
		case len(p) >= 4 && p[1] == "jobs" && isJobID(p[2]):
			env.runnerJob(w, r, b, p[2], p[3:])
		default:
			fail(w, http.StatusNotFound, "no such endpoint: %s %s", m, r.URL.Path)
		}
		return
	}
	if role == roleRunner {
		fail(w, http.StatusForbidden, "refused: the runner token only takes and reports jobs")
		return
	}
	switch {
	case m == http.MethodPost && len(p) == 1:
		env.jobSubmit(w, r, b, who)
	case m == http.MethodGet && len(p) == 1:
		if role != roleAdmin {
			fail(w, http.StatusForbidden, "refused: listing jobs needs the admin token; ask for a job by its id")
			return
		}
		ix, _, _, err := loadIndex(b, env.Now().UTC())
		if err != nil {
			fail(w, http.StatusBadGateway, "%v", err)
			return
		}
		out := make([]jobView, 0, len(ix.Jobs))
		for _, j := range ix.Jobs {
			out = append(out, ix.view(j))
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
	case len(p) >= 2 && isJobID(p[1]):
		env.callerJob(w, r, b, who, role == roleAdmin, p[1], p[2:])
	default:
		fail(w, http.StatusNotFound, "no such endpoint: %s %s", m, r.URL.Path)
	}
}

func (env Env) jobSubmit(w http.ResponseWriter, r *http.Request, b JobBucket, who string) {
	var spec JobSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&spec); err != nil {
		fail(w, http.StatusBadRequest, "the body must be a job spec in JSON: %v", err)
		return
	}
	if err := spec.check(); err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	id, err := newJobID()
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		mine := 0
		for _, j := range ix.Jobs {
			if j.Owner == who && !finalState(j.State) {
				mine++
			}
		}
		switch {
		case mine >= maxQueued:
			return refuse(http.StatusTooManyRequests, "refused: %s already has %d jobs waiting or running", who, mine)
		case len(ix.Jobs) >= maxIndexJobs:
			return refuse(http.StatusServiceUnavailable, "refused: %d jobs in the index; try later", len(ix.Jobs))
		}
		ix.Jobs = append(ix.Jobs, Job{ID: id, Owner: who, Spec: spec, State: stUploading, Created: now})
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"job": ix.view(*ix.find(id)), "upload": jobPath(id, "input")})
}

// callerJob is /api/jobs/{id}/... for the job's owner, or an admin.
func (env Env) callerJob(w http.ResponseWriter, r *http.Request, b JobBucket, who string, admin bool, id string, rest []string) {
	now := env.Now().UTC()
	ix, _, _, err := loadIndex(b, now)
	if err != nil {
		fail(w, http.StatusBadGateway, "%v", err)
		return
	}
	j := ix.find(id)
	if j == nil {
		// Archived: read back, read-only.
		body, _, ok, lErr := b.Load(jobArchiveKey(id))
		var a Job
		switch {
		case lErr != nil:
			fail(w, http.StatusBadGateway, "%v", lErr)
			return
		case ok && json.Unmarshal(body, &a) == nil:
			j = &a
		}
	}
	if j == nil || (!admin && j.Owner != who) {
		fail(w, http.StatusNotFound, "no such job")
		return
	}
	m := r.Method
	switch {
	case m == http.MethodGet && len(rest) == 0:
		writeJSON(w, http.StatusOK, ix.view(*j))
	case m == http.MethodPut && len(rest) == 1 && rest[0] == "input":
		env.jobInput(w, r, b, *j)
	case m == http.MethodGet && len(rest) == 1 && rest[0] == "log":
		jobLog(w, r, b, id)
	case m == http.MethodGet && len(rest) == 2 && rest[0] == "files":
		ct, ok := jobFileType(rest[1])
		if !ok {
			fail(w, http.StatusNotFound, "no such file")
			return
		}
		streamBlob(w, b, jobFileKey(id, rest[1]), ct)
	case m == http.MethodPost && len(rest) == 1 && rest[0] == "cancel":
		ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
			j := ix.find(id)
			switch {
			case j == nil:
				return refuse(http.StatusConflict, "the job has already left the queue")
			case j.State == stUploading || j.State == stQueued:
				j.State, j.Finished, j.Why = stCancelled, now, "cancelled before it ran"
			case j.State == stRunning:
				j.Cancel = true
			}
			return nil
		})
		if err != nil {
			jobFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ix.view(*ix.find(id)))
	default:
		fail(w, http.StatusNotFound, "no such endpoint: %s %s", m, r.URL.Path)
	}
}

// jobInput stores the binary, then queues the job. R2 checks the bytes
// against the spec's SHA-256 as they arrive, so a damaged upload stores
// nothing and the job stays waiting for a good one.
func (env Env) jobInput(w http.ResponseWriter, r *http.Request, b JobBucket, j Job) {
	switch {
	case j.State != stUploading:
		fail(w, http.StatusConflict, "the job is %s, not waiting for its binary", j.State)
		return
	case r.ContentLength < 0:
		fail(w, http.StatusLengthRequired, "send Content-Length")
		return
	case r.ContentLength != j.Spec.Size:
		fail(w, http.StatusBadRequest, "%d bytes sent, the spec says %d", r.ContentLength, j.Spec.Size)
		return
	}
	info, err := b.Put(jobInputKey(j.ID), r.Body, r.ContentLength, j.Spec.SHA256)
	switch {
	case errors.Is(err, errDigestMismatch):
		fail(w, http.StatusBadRequest, "the binary does not hash to the spec's sha256; nothing was stored")
		return
	case err != nil:
		fail(w, http.StatusBadGateway, "storing the binary: %v", err)
		return
	case info.Size != j.Spec.Size:
		fail(w, http.StatusBadGateway, "stored %d bytes of %d", info.Size, j.Spec.Size)
		return
	}
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		cur := ix.find(j.ID)
		if cur == nil || cur.State != stUploading {
			return refuse(http.StatusConflict, "the job is no longer waiting for its binary")
		}
		cur.State, cur.Queued = stQueued, now
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ix.view(*ix.find(j.ID)))
}

// jobLog answers the log from ?offset=N, and its whole size in X-Log-Size,
// so a client polling it prints each line once.
func jobLog(w http.ResponseWriter, r *http.Request, b JobBucket, id string) {
	var off int64
	if s := r.URL.Query().Get("offset"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "offset must be a byte count")
			return
		}
		off = n
	}
	info, ok, err := b.Head(jobLogKey(id))
	if err != nil {
		fail(w, http.StatusBadGateway, "%v", err)
		return
	}
	if !ok {
		info.Size = 0
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Log-Size", strconv.FormatInt(info.Size, 10))
	if off >= info.Size {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, body, ok, err := b.Get(jobLogKey(id), &byteRange{off, info.Size - off})
	if err != nil || !ok {
		fail(w, http.StatusBadGateway, "reading the log: %v (found=%v)", err, ok)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size-off, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func streamBlob(w http.ResponseWriter, b JobBucket, key, ct string) {
	info, body, ok, err := b.Get(key, nil)
	switch {
	case err != nil:
		fail(w, http.StatusBadGateway, "%v", err)
		return
	case !ok:
		fail(w, http.StatusNotFound, "no such file")
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	if info.SHA256 != "" {
		w.Header().Set(hdrJobSHA256, info.SHA256)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

const hdrJobSHA256 = "X-Job-Sha256"

// jobClaim hands the oldest queued job to a Mac.
func (env Env) jobClaim(w http.ResponseWriter, b JobBucket, runner string) {
	var id string
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		id = ""
		var next *Job
		for i := range ix.Jobs {
			j := &ix.Jobs[i]
			if j.State == stQueued && (next == nil || j.Queued.Before(next.Queued)) {
				next = j
			}
		}
		if next == nil {
			return errNothing
		}
		next.State, next.Started, next.Runner, next.Lease = stRunning, now, runner, now.Add(leaseFor)
		id = next.ID
		return nil
	})
	switch {
	case errors.Is(err, errNothing):
		w.WriteHeader(http.StatusNoContent)
	case err != nil:
		jobFail(w, err)
	default:
		writeJSON(w, http.StatusOK, ix.view(*ix.find(id)))
	}
}

// errNothing is a claim with nothing queued: no write is needed.
var errNothing = errors.New("nothing queued")

// runnerJob is /api/runner/jobs/{id}/... for the Mac running it.
func (env Env) runnerJob(w http.ResponseWriter, r *http.Request, b JobBucket, id string, rest []string) {
	m := r.Method
	// Every runner call is about a job it is running; anything else is a
	// Mac that lost its lease and must stop.
	ix, _, _, err := loadIndex(b, env.Now().UTC())
	if err != nil {
		fail(w, http.StatusBadGateway, "%v", err)
		return
	}
	j := ix.find(id)
	if j == nil || j.State != stRunning {
		state := "gone"
		if j != nil {
			state = j.State
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the job is " + state + ", not running", "state": state})
		return
	}
	switch {
	case m == http.MethodPost && len(rest) == 1 && rest[0] == "heartbeat":
		ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
			j := ix.find(id)
			if j == nil || j.State != stRunning {
				return refuse(http.StatusConflict, "the job is no longer running")
			}
			j.Lease = now.Add(leaseFor)
			return nil
		})
		if err != nil {
			jobFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cancel": ix.find(id).Cancel, "state": stRunning})
	case m == http.MethodGet && len(rest) == 1 && rest[0] == "input":
		streamBlob(w, b, jobInputKey(id), "application/octet-stream")
	case m == http.MethodPut && len(rest) == 1 && rest[0] == "log":
		putBlob(w, r, b, jobLogKey(id), maxJobLog)
	case m == http.MethodPut && len(rest) == 2 && rest[0] == "files":
		if _, ok := jobFileType(rest[1]); !ok {
			fail(w, http.StatusBadRequest, "a result file is a plain name ending in .png, .json or .txt")
			return
		}
		putBlob(w, r, b, jobFileKey(id, rest[1]), maxJobFile)
	case m == http.MethodPost && len(rest) == 1 && rest[0] == "finish":
		env.jobFinish(w, r, b, id)
	default:
		fail(w, http.StatusNotFound, "no such endpoint: %s %s", m, r.URL.Path)
	}
}

func putBlob(w http.ResponseWriter, r *http.Request, b JobBucket, key string, limit int64) {
	sum := r.Header.Get(hdrJobSHA256)
	switch {
	case r.ContentLength < 0:
		fail(w, http.StatusLengthRequired, "send Content-Length")
		return
	case r.ContentLength > limit:
		fail(w, http.StatusRequestEntityTooLarge, "%d bytes, over the %d this accepts", r.ContentLength, limit)
		return
	case !isHex(sum, 64):
		fail(w, http.StatusBadRequest, "send %s: the body's SHA-256", hdrJobSHA256)
		return
	}
	info, err := b.Put(key, r.Body, r.ContentLength, sum)
	switch {
	case errors.Is(err, errDigestMismatch):
		fail(w, http.StatusBadRequest, "the body does not hash to %s; nothing was stored", hdrJobSHA256)
	case err != nil:
		fail(w, http.StatusBadGateway, "storing: %v", err)
	default:
		writeJSON(w, http.StatusCreated, info)
	}
}

// jobFinish records how the job ended. Each file it names must have been
// stored; the record lists them with their sizes and digests, as stored.
func (env Env) jobFinish(w http.ResponseWriter, r *http.Request, b JobBucket, id string) {
	var res struct {
		ExitCode int      `json:"exit_code"`
		Outcome  string   `json:"outcome"`
		Message  string   `json:"message"`
		Files    []string `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&res); err != nil {
		fail(w, http.StatusBadRequest, "the body must be the result in JSON: %v", err)
		return
	}
	if len(res.Files) > maxJobFiles {
		fail(w, http.StatusBadRequest, "%d files, over %d", len(res.Files), maxJobFiles)
		return
	}
	files := make([]BlobInfo, 0, len(res.Files))
	for _, f := range res.Files {
		if _, ok := jobFileType(f); !ok {
			fail(w, http.StatusBadRequest, "%q is not a result file name", f)
			return
		}
		info, ok, err := b.Head(jobFileKey(id, f))
		switch {
		case err != nil:
			fail(w, http.StatusBadGateway, "%v", err)
			return
		case !ok:
			fail(w, http.StatusBadRequest, "%s was named but never stored", f)
			return
		}
		files = append(files, BlobInfo{Key: f, Size: info.Size, SHA256: info.SHA256})
	}
	if len(res.Message) > 4096 {
		res.Message = res.Message[:4096]
	}
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		j := ix.find(id)
		if j == nil || j.State != stRunning {
			return refuse(http.StatusConflict, "the job is no longer running")
		}
		code := res.ExitCode
		j.State, j.Finished, j.ExitCode, j.Outcome, j.Message, j.Files = stFinished, now, &code, res.Outcome, res.Message, files
		if j.Cancel {
			j.State, j.Why = stCancelled, "cancelled while it ran"
		}
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ix.view(*ix.find(id)))
}

func jobFail(w http.ResponseWriter, err error) {
	var e errJob
	switch {
	case errors.As(err, &e):
		fail(w, e.code, "%s", e.msg)
	case errors.Is(err, errContention):
		fail(w, http.StatusServiceUnavailable, "%v", err)
	default:
		fail(w, http.StatusBadGateway, "%v", err)
	}
}
