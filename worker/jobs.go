package main

// The remote job queue (wire/jobs.go has its routes): a developer, an agent
// or a workflow on any OS submits a Windows binary, a Mac running
// `irgo-winvm serve` takes it, runs it on a fresh clone of the golden image,
// and sends the result back. The Mac only connects out, to here.
//
// Every live job is in one small object, jobs/index.json, changed only by a
// compare-and-swap (CAS), so two submits, a claim and a cancel can race and
// none is lost. The binaries and results are separate objects beside it. A
// caller sees only its own jobs, by id: another caller's id answers 404,
// exactly as an id that does not exist. The dispatcher has checked the
// route's scope before any handler here runs; a caller handler asks
// jobOwner who it is.

import (
	"crypto/rand"
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

	"github.com/joeblew999/irgo-windows-vm/wire"
)

const (
	maxIndexJobs = 500 // every live and recent job, all callers
	maxQueued    = 20  // live jobs per caller

	// leaseFor is how long a running job survives without a heartbeat from
	// its Mac; it heartbeats every 20 s. A job whose lease runs out is lost.
	leaseFor = 90 * time.Second
	// uploadTTL and queueTTL expire a job that never got its binary, or that
	// no Mac took.
	uploadTTL = 30 * time.Minute
	queueTTL  = 2 * time.Hour
	// overhead is what a job takes beyond its own timeout: the clone and its
	// boot (23 to 30 s measured), the push, the screenshots, the delete.
	overhead = 20 * time.Minute
	// keepFor is how long an ended job stays in the index. After that its
	// record is archived beside its files, which a lifecycle rule deletes.
	keepFor = 24 * time.Hour

	jobIndexKey = "jobs/index.json"
)

// Job is wire's: the index holds exactly what the routes answer with.
type Job = wire.Job

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

func (ix *jobIndex) view(j Job) wire.JobView {
	v := wire.JobView{Job: j}
	var queued []Job
	for _, o := range ix.Jobs {
		switch o.State {
		case wire.JobQueued:
			queued = append(queued, o)
		case wire.JobRunning:
			v.Running++
		}
	}
	if j.State == wire.JobQueued {
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
		case j.State == wire.JobUploading && now.Sub(j.Created) > uploadTTL:
			end(&j, wire.JobExpired, "the binary was never uploaded", j.Created.Add(uploadTTL))
		case j.State == wire.JobQueued && now.Sub(j.Queued) > queueTTL:
			end(&j, wire.JobExpired, fmt.Sprintf("no Mac took it within %s", queueTTL), j.Queued.Add(queueTTL))
		case j.State == wire.JobRunning && now.After(j.Lease) && j.Lease.Before(limit):
			end(&j, wire.JobLost, fmt.Sprintf("its Mac (%s) stopped reporting", j.Runner), j.Lease)
		case j.State == wire.JobRunning && now.After(limit):
			end(&j, wire.JobTimedOut, fmt.Sprintf("its Mac (%s) did not finish it within its timeout plus %s", j.Runner, overhead), limit)
		}
		if wire.JobFinal(j.State) && now.Sub(j.Finished) > keepFor {
			gone = append(gone, j)
			continue
		}
		keep = append(keep, j)
	}
	ix.Jobs = keep
	return gone
}

// errJob is a refusal the caller can act on.
type errJob struct {
	code wire.Code
	msg  string
}

func (e errJob) Error() string { return e.msg }

func refuse(code wire.Code, format string, a ...any) error {
	return errJob{code, fmt.Sprintf(format, a...)}
}

// errContention is a CAS lost on every attempt.
var errContention = refuse(wire.CodeBusy, "the job index changed under every attempt; nothing was stored, ask again")

// pause is how mutate backs off between attempts: time.Sleep on the host,
// setTimeout on Workers, where time.Sleep never returns (jobs_js.go).
var pause = time.Sleep

// loadIndex reads the index and sweeps it, without writing.
func loadIndex(b JobBucket, now time.Time) (jobIndex, error) {
	var ix jobIndex
	body, _, ok, err := b.Load(jobIndexKey)
	if err != nil || !ok {
		return ix, err
	}
	if err := json.Unmarshal(body, &ix); err != nil {
		// Cannot tell what is queued: refuse rather than start again empty
		// and drop every job in it.
		return ix, fmt.Errorf("%s does not parse (%v); an admin must repair or delete it", jobIndexKey, err)
	}
	ix.sweep(now)
	return ix, nil
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

func newJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// checkSpec validates a spec and fills its defaults.
func checkSpec(s *wire.JobSpec) error {
	switch {
	case s.Kind != wire.JobKindApp && s.Kind != wire.JobKindTest:
		return fmt.Errorf(`kind must be %q or %q, not %q`, wire.JobKindApp, wire.JobKindTest, s.Kind)
	case !wire.IsSafeName(s.Name) || !strings.HasSuffix(strings.ToLower(s.Name), ".exe"):
		return fmt.Errorf("name %q must be a plain file name ending in .exe", s.Name)
	case s.Size <= 0 || s.Size > wire.MaxJobInput:
		return fmt.Errorf("size %d must be between 1 and %d bytes", s.Size, wire.MaxJobInput)
	case !wire.IsHex(s.SHA256, 64):
		return errors.New("sha256 must be 64 lower-case hex digits")
	case len(s.Args) > wire.MaxJobArgs:
		return fmt.Errorf("%d arguments, over %d", len(s.Args), wire.MaxJobArgs)
	case s.TimeoutS < 0 || s.TimeoutS > wire.MaxJobTimeout:
		return fmt.Errorf("timeout_s %d must be between 1 and %d", s.TimeoutS, wire.MaxJobTimeout)
	}
	for _, a := range s.Args {
		if len(a) > wire.MaxJobArgLen || strings.ContainsRune(a, 0) {
			return fmt.Errorf("an argument is over %d bytes or holds a NUL", wire.MaxJobArgLen)
		}
	}
	if s.TimeoutS == 0 {
		s.TimeoutS = wire.DefJobTimeout
	}
	return nil
}

// namedTokens parses a Named scope's secret: name=token,name=token, or a
// bare token, which is the caller "caller".
func namedTokens(secret string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(secret, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, tok, ok := strings.Cut(pair, "=")
		if !ok {
			name, tok = "caller", pair
		}
		if name != "" && tok != "" {
			out[name] = tok
		}
	}
	return out
}

// namedMatch is the name whose token got is, "" for none. Every token is
// compared, each in constant time, so the time taken does not say which one
// nearly matched.
func namedMatch(secret, got string) string {
	who := ""
	for name, tok := range namedTokens(secret) {
		if tokenMatches(got, tok) {
			who = name
		}
	}
	return who
}

// jobOwner is the caller a request to a ScopeJobs route came from; the
// dispatcher has already refused one with no valid token.
func (env Env) jobOwner(r *http.Request) string {
	info, _ := wire.ScopeJobs.Info()
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return namedMatch(env.Var(info.Secret), got)
}

func (env Env) jobBucket(w http.ResponseWriter) (JobBucket, bool) {
	b, err := env.Jobs()
	if err != nil {
		fail(w, wire.CodeInternal, "the JOBS bucket: %v", err)
		return nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return b, true
}

// callerJob finds a job of this caller's, in the index or archived; it
// answers 404 itself for one that is not there or not theirs.
func (env Env) callerJob(w http.ResponseWriter, r *http.Request, b JobBucket, id string) (jobIndex, *Job, bool) {
	return env.findJob(w, b, id, ownedBy(env.jobOwner(r)))
}

// ownedBy is a job of caller's. No caller owns nothing: an empty name never
// matches, rather than matching every job.
func ownedBy(caller string) func(*Job) bool {
	return func(j *Job) bool { return caller != "" && j.Owner == caller }
}

// anyOwner is every job, for the admin token's routes.
func anyOwner(*Job) bool { return true }

// findJob finds a job, in the index or archived, that may answers yes for;
// it answers 404 itself for one that is not there or that may refuses, so
// another caller's job looks exactly like one that does not exist.
func (env Env) findJob(w http.ResponseWriter, b JobBucket, id string, may func(*Job) bool) (jobIndex, *Job, bool) {
	ix, err := loadIndex(b, env.Now().UTC())
	if err != nil {
		fail(w, wire.CodeStorage, "%v", err)
		return ix, nil, false
	}
	j := ix.find(id)
	if j == nil && wire.IsJobID(id) {
		body, _, ok, lErr := b.Load(jobArchiveKey(id))
		var a Job
		switch {
		case lErr != nil:
			fail(w, wire.CodeStorage, "%v", lErr)
			return ix, nil, false
		case ok && json.Unmarshal(body, &a) == nil:
			j = &a
		}
	}
	if j == nil || !may(j) {
		fail(w, wire.CodeNotFound, "no such job")
		return ix, nil, false
	}
	return ix, j, true
}

func jobFail(w http.ResponseWriter, err error) {
	var e errJob
	if errors.As(err, &e) {
		fail(w, e.code, "%s", e.msg)
		return
	}
	fail(w, wire.CodeStorage, "%v", err)
}

// jobSubmit is POST /api/jobs.
func (env Env) jobSubmit(w http.ResponseWriter, r *http.Request, _ []string) {
	var spec wire.JobSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, wire.MaxJobSpec)).Decode(&spec); err != nil {
		fail(w, wire.CodeBadRequest, "the body must be a job spec in JSON: %v", err)
		return
	}
	if err := checkSpec(&spec); err != nil {
		fail(w, wire.CodeBadRequest, "%v", err)
		return
	}
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	who := env.jobOwner(r)
	id, err := newJobID()
	if err != nil {
		fail(w, wire.CodeInternal, "%v", err)
		return
	}
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		mine := 0
		for _, j := range ix.Jobs {
			if j.Owner == who && !wire.JobFinal(j.State) {
				mine++
			}
		}
		switch {
		case mine >= maxQueued:
			return refuse(wire.CodeTooMany, "refused: %s already has %d jobs waiting or running", who, mine)
		case len(ix.Jobs) >= maxIndexJobs:
			return refuse(wire.CodeBusy, "refused: %d jobs in the index; try later", len(ix.Jobs))
		}
		ix.Jobs = append(ix.Jobs, Job{ID: id, Owner: who, Spec: spec, State: wire.JobUploading, Created: now})
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wire.JobCreated{Job: ix.view(*ix.find(id)), Upload: wire.MustFind(wire.RouteJobInput).URL("", id)})
}

// jobInput is PUT /api/jobs/{id}/input: the binary, then the job queued. R2
// checks the bytes against the spec's SHA-256 as they arrive, so a damaged
// upload stores nothing and the job stays waiting for a good one.
func (env Env) jobInput(w http.ResponseWriter, r *http.Request, p []string) {
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	_, j, ok := env.callerJob(w, r, b, p[0])
	if !ok {
		return
	}
	switch {
	case j.State != wire.JobUploading:
		fail(w, wire.CodeConflict, "the job is %s, not waiting for its binary", j.State)
		return
	case r.ContentLength != j.Spec.Size:
		fail(w, wire.CodeBadRequest, "%d bytes sent, the spec says %d", r.ContentLength, j.Spec.Size)
		return
	}
	info, err := b.Put(jobInputKey(j.ID), r.Body, r.ContentLength, j.Spec.SHA256)
	switch {
	case errors.Is(err, errDigestMismatch):
		fail(w, wire.CodeDigestMismatch, "the binary does not hash to the spec's sha256; nothing was stored")
		return
	case err != nil:
		fail(w, wire.CodeStorage, "storing the binary: %v", err)
		return
	case info.Size != j.Spec.Size:
		fail(w, wire.CodeStorage, "stored %d bytes of %d", info.Size, j.Spec.Size)
		return
	}
	id := j.ID
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		cur := ix.find(id)
		if cur == nil || cur.State != wire.JobUploading {
			return refuse(wire.CodeConflict, "the job is no longer waiting for its binary")
		}
		cur.State, cur.Queued = wire.JobQueued, now
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ix.view(*ix.find(id)))
}

// jobGet is GET /api/jobs/{id}.
func (env Env) jobGet(w http.ResponseWriter, r *http.Request, p []string) {
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	if ix, j, ok := env.callerJob(w, r, b, p[0]); ok {
		writeJSON(w, http.StatusOK, ix.view(*j))
	}
}

// jobLog is GET /api/jobs/{id}/log?offset=N: the log from N, and its whole
// size in X-Log-Size, so a client polling it prints each line once.
func (env Env) jobLog(w http.ResponseWriter, r *http.Request, p []string) {
	var off int64
	if s := r.URL.Query().Get("offset"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			fail(w, wire.CodeBadRequest, "offset must be a byte count")
			return
		}
		off = n
	}
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	if _, _, ok := env.callerJob(w, r, b, p[0]); !ok {
		return
	}
	info, found, err := b.Head(jobLogKey(p[0]))
	if err != nil {
		fail(w, wire.CodeStorage, "%v", err)
		return
	}
	if !found {
		info.Size = 0
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set(wire.HeaderLogSize, strconv.FormatInt(info.Size, 10))
	if off >= info.Size {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, body, found, err := b.Get(jobLogKey(p[0]), &byteRange{off, info.Size - off})
	if err != nil || !found {
		fail(w, wire.CodeStorage, "reading the log: %v (found=%v)", err, found)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size-off, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// jobFile is GET /api/jobs/{id}/files/{name}: the caller's own job's file.
func (env Env) jobFile(w http.ResponseWriter, r *http.Request, p []string) {
	env.serveJobFile(w, p, ownedBy(env.jobOwner(r)))
}

// jobAdminFile is GET /api/admin/jobs/{id}/files/{name}, for the admin
// token: the same file, whoever's job it is.
func (env Env) jobAdminFile(w http.ResponseWriter, _ *http.Request, p []string) {
	env.serveJobFile(w, p, anyOwner)
}

// serveJobFile streams file p[1] of job p[0], if may allows that job.
func (env Env) serveJobFile(w http.ResponseWriter, p []string, may func(*Job) bool) {
	ct, ok := wire.JobFileType(p[1])
	if !ok {
		fail(w, wire.CodeNotFound, "no such file")
		return
	}
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	if _, _, ok := env.findJob(w, b, p[0], may); ok {
		streamBlob(w, b, jobFileKey(p[0], p[1]), ct)
	}
}

// jobCancel is POST /api/jobs/{id}/cancel.
func (env Env) jobCancel(w http.ResponseWriter, r *http.Request, p []string) {
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	if _, _, ok := env.callerJob(w, r, b, p[0]); !ok {
		return
	}
	id := p[0]
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		j := ix.find(id)
		switch {
		case j == nil:
			return refuse(wire.CodeConflict, "the job has already left the queue")
		case j.State == wire.JobUploading || j.State == wire.JobQueued:
			j.State, j.Finished, j.Why = wire.JobCancelled, now, "cancelled before it ran"
		case j.State == wire.JobRunning:
			j.Cancel = true
		}
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ix.view(*ix.find(id)))
}

// jobList is GET /api/jobs, for the admin token.
func (env Env) jobList(w http.ResponseWriter, _ *http.Request, _ []string) {
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	ix, err := loadIndex(b, env.Now().UTC())
	if err != nil {
		fail(w, wire.CodeStorage, "%v", err)
		return
	}
	out := wire.JobList{Jobs: make([]wire.JobView, 0, len(ix.Jobs))}
	for _, j := range ix.Jobs {
		out.Jobs = append(out.Jobs, ix.view(j))
	}
	writeJSON(w, http.StatusOK, out)
}

func streamBlob(w http.ResponseWriter, b JobBucket, key, ct string) {
	info, body, ok, err := b.Get(key, nil)
	switch {
	case err != nil:
		fail(w, wire.CodeStorage, "%v", err)
		return
	case !ok:
		fail(w, wire.CodeNotFound, "no such file")
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	if info.SHA256 != "" {
		w.Header().Set(wire.HeaderJobSHA256, info.SHA256)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// errNothing is a claim with nothing queued: no write is needed.
var errNothing = errors.New("nothing queued")

// runnerClaim is POST /api/runner/claim: the oldest queued job, to this Mac.
func (env Env) runnerClaim(w http.ResponseWriter, r *http.Request, _ []string) {
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	runner := r.Header.Get(wire.HeaderRunner)
	if !wire.IsSafeName(runner) {
		runner = "mac"
	}
	var id string
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		id = ""
		var next *Job
		for i := range ix.Jobs {
			j := &ix.Jobs[i]
			if j.State == wire.JobQueued && (next == nil || j.Queued.Before(next.Queued)) {
				next = j
			}
		}
		if next == nil {
			return errNothing
		}
		next.State, next.Started, next.Runner, next.Lease = wire.JobRunning, now, runner, now.Add(leaseFor)
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

// running is the job, when it is running; otherwise it answers 409 with the
// state, which stops the Mac.
func (env Env) running(w http.ResponseWriter, b JobBucket, id string) bool {
	ix, err := loadIndex(b, env.Now().UTC())
	if err != nil {
		fail(w, wire.CodeStorage, "%v", err)
		return false
	}
	j := ix.find(id)
	if j == nil || j.State != wire.JobRunning {
		state := "gone"
		if j != nil {
			state = j.State
		}
		fail(w, wire.CodeConflict, "the job is %s, not running", state)
		return false
	}
	return true
}

// runnerHeartbeat is POST /api/runner/jobs/{id}/heartbeat.
func (env Env) runnerHeartbeat(w http.ResponseWriter, _ *http.Request, p []string) {
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	id := p[0]
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		j := ix.find(id)
		if j == nil || j.State != wire.JobRunning {
			state := "gone"
			if j != nil {
				state = j.State
			}
			return refuse(wire.CodeConflict, "the job is %s, not running", state)
		}
		j.Lease = now.Add(leaseFor)
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire.JobHeartbeat{Cancel: ix.find(id).Cancel, State: wire.JobRunning})
}

// runnerInput is GET /api/runner/jobs/{id}/input.
func (env Env) runnerInput(w http.ResponseWriter, _ *http.Request, p []string) {
	b, ok := env.jobBucket(w)
	if ok && env.running(w, b, p[0]) {
		streamBlob(w, b, jobInputKey(p[0]), wire.TypeOctets)
	}
}

// runnerLog is PUT /api/runner/jobs/{id}/log.
func (env Env) runnerLog(w http.ResponseWriter, r *http.Request, p []string) {
	b, ok := env.jobBucket(w)
	if ok && env.running(w, b, p[0]) {
		putBlob(w, r, b, jobLogKey(p[0]))
	}
}

// runnerFile is PUT /api/runner/jobs/{id}/files/{name}.
func (env Env) runnerFile(w http.ResponseWriter, r *http.Request, p []string) {
	if _, ok := wire.JobFileType(p[1]); !ok {
		fail(w, wire.CodeBadRequest, "a result file is a plain name ending in .png, .json or .txt")
		return
	}
	b, ok := env.jobBucket(w)
	if ok && env.running(w, b, p[0]) {
		putBlob(w, r, b, jobFileKey(p[0], p[1]))
	}
}

func putBlob(w http.ResponseWriter, r *http.Request, b JobBucket, key string) {
	sum := r.Header.Get(wire.HeaderJobSHA256)
	if !wire.IsHex(sum, 64) {
		fail(w, wire.CodeBadRequest, "send %s: the body's SHA-256", wire.HeaderJobSHA256)
		return
	}
	info, err := b.Put(key, r.Body, r.ContentLength, sum)
	switch {
	case errors.Is(err, errDigestMismatch):
		fail(w, wire.CodeDigestMismatch, "the body does not hash to %s; nothing was stored", wire.HeaderJobSHA256)
	case err != nil:
		fail(w, wire.CodeStorage, "storing: %v", err)
	default:
		writeJSON(w, http.StatusCreated, info)
	}
}

// runnerFinish is POST /api/runner/jobs/{id}/finish. Each file it names must
// have been stored; the record lists them as stored.
func (env Env) runnerFinish(w http.ResponseWriter, r *http.Request, p []string) {
	var res wire.JobResult
	if err := json.NewDecoder(io.LimitReader(r.Body, wire.MaxJobSpec)).Decode(&res); err != nil {
		fail(w, wire.CodeBadRequest, "the body must be the result in JSON: %v", err)
		return
	}
	if len(res.Files) > wire.MaxJobFiles {
		fail(w, wire.CodeBadRequest, "%d files, over %d", len(res.Files), wire.MaxJobFiles)
		return
	}
	b, ok := env.jobBucket(w)
	if !ok {
		return
	}
	id := p[0]
	files := make([]wire.BlobInfo, 0, len(res.Files))
	for _, f := range res.Files {
		if _, ok := wire.JobFileType(f); !ok {
			fail(w, wire.CodeBadRequest, "%q is not a result file name", f)
			return
		}
		info, ok, err := b.Head(jobFileKey(id, f))
		switch {
		case err != nil:
			fail(w, wire.CodeStorage, "%v", err)
			return
		case !ok:
			fail(w, wire.CodeBadRequest, "%s was named but never stored", f)
			return
		}
		files = append(files, wire.BlobInfo{Key: f, Size: info.Size, SHA256: info.SHA256})
	}
	if len(res.Message) > 4096 {
		res.Message = res.Message[:4096]
	}
	ix, err := env.mutate(b, func(ix *jobIndex, now time.Time) error {
		j := ix.find(id)
		if j == nil || j.State != wire.JobRunning {
			return refuse(wire.CodeConflict, "the job is no longer running")
		}
		code := res.ExitCode
		j.State, j.Finished, j.ExitCode, j.Outcome, j.Message, j.Files = wire.JobFinished, now, &code, res.Outcome, res.Message, files
		if j.Cancel {
			j.State, j.Why = wire.JobCancelled, "cancelled while it ran"
		}
		return nil
	})
	if err != nil {
		jobFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ix.view(*ix.find(id)))
}
