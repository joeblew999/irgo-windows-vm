package workerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// The remote job queue's routes (wire/jobs.go): a caller's (ScopeJobs), the
// admin's list, and the Mac's (ScopeJobsRunner). internal/remote waits,
// prints and maps outcomes on top of these.

func jsonBody(v any) ([]byte, http.Header, error) {
	b, err := json.Marshal(v)
	h := http.Header{}
	h.Set("Content-Type", wire.TypeJSON)
	return b, h, err
}

func shaHeader(b []byte, h http.Header) http.Header {
	if h == nil {
		h = http.Header{}
	}
	s := sha256.Sum256(b)
	h.Set(wire.HeaderJobSHA256, hex.EncodeToString(s[:]))
	return h
}

// JobSubmit creates a job from spec; it waits for its binary.
func (c *Client) JobSubmit(ctx context.Context, spec wire.JobSpec) (wire.JobCreated, error) {
	var out wire.JobCreated
	b, h, err := jsonBody(spec)
	if err != nil {
		return out, err
	}
	resp, err := c.Do(ctx, wire.RouteJobSubmit, nil, nil, b, h)
	if err == nil {
		err = decode(resp, 1<<20, &out)
	}
	return out, err
}

// JobInput sends the job's binary; the Worker checks it against the spec and
// queues the job.
func (c *Client) JobInput(ctx context.Context, id string, bin []byte) (wire.JobView, error) {
	var out wire.JobView
	h := http.Header{}
	h.Set("Content-Type", wire.TypeOctets)
	resp, err := c.Do(ctx, wire.RouteJobInput, []string{id}, nil, bin, h)
	if err == nil {
		err = decode(resp, 1<<20, &out)
	}
	return out, err
}

// JobGet is the job now, with its place in the queue.
func (c *Client) JobGet(ctx context.Context, id string) (wire.JobView, error) {
	var out wire.JobView
	resp, err := c.Do(ctx, wire.RouteJobGet, []string{id}, nil, nil, nil)
	if err == nil {
		err = decode(resp, 1<<20, &out)
	}
	return out, err
}

// JobLog is the job's log from byte offset, and the offset to ask from next.
func (c *Client) JobLog(ctx context.Context, id string, offset int64) ([]byte, int64, error) {
	resp, err := c.Do(ctx, wire.RouteJobLog, []string{id}, url.Values{"offset": {strconv.FormatInt(offset, 10)}}, nil, nil)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, wire.MaxJobLog))
	if err != nil {
		return nil, offset, err
	}
	next, pErr := strconv.ParseInt(resp.Header.Get(wire.HeaderLogSize), 10, 64)
	if pErr != nil {
		next = offset + int64(len(b))
	}
	return b, next, nil
}

// JobFile opens one result file and returns its SHA-256 as stored ("" when
// the Worker did not say). The caller closes it.
func (c *Client) JobFile(ctx context.Context, id, name string) (io.ReadCloser, string, error) {
	resp, err := c.Do(ctx, wire.RouteJobFile, []string{id, name}, nil, nil, nil)
	if err != nil {
		return nil, "", err
	}
	return resp.Body, resp.Header.Get(wire.HeaderJobSHA256), nil
}

// JobCancel cancels the job.
func (c *Client) JobCancel(ctx context.Context, id string) (wire.JobView, error) {
	var out wire.JobView
	resp, err := c.Do(ctx, wire.RouteJobCancel, []string{id}, nil, nil, nil)
	if err == nil {
		err = decode(resp, 1<<20, &out)
	}
	return out, err
}

// JobList is every job in the index (the admin token).
func (c *Client) JobList(ctx context.Context) (wire.JobList, error) {
	var out wire.JobList
	resp, err := c.Do(ctx, wire.RouteJobList, nil, nil, nil, nil)
	if err == nil {
		err = decode(resp, 16<<20, &out)
	}
	return out, err
}

func runnerHeader(runner string) http.Header {
	h := http.Header{}
	if runner != "" {
		h.Set(wire.HeaderRunner, runner)
	}
	return h
}

// RunnerClaim takes the oldest queued job for this Mac; nil when none.
func (c *Client) RunnerClaim(ctx context.Context, runner string) (*wire.JobView, error) {
	resp, err := c.Do(ctx, wire.RouteRunnerClaim, nil, nil, nil, runnerHeader(runner))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		_ = resp.Body.Close()
		return nil, nil
	}
	var out wire.JobView
	return &out, decode(resp, 1<<20, &out)
}

// RunnerHeartbeat renews the job's lease and says whether it was cancelled.
func (c *Client) RunnerHeartbeat(ctx context.Context, runner, id string) (wire.JobHeartbeat, error) {
	var out wire.JobHeartbeat
	resp, err := c.Do(ctx, wire.RouteRunnerHeartbeat, []string{id}, nil, nil, runnerHeader(runner))
	if err == nil {
		err = decode(resp, 4096, &out)
	}
	return out, err
}

// RunnerInput opens the running job's binary. The caller closes it.
func (c *Client) RunnerInput(ctx context.Context, runner, id string) (io.ReadCloser, error) {
	resp, err := c.Do(ctx, wire.RouteRunnerInput, []string{id}, nil, nil, runnerHeader(runner))
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// RunnerLog replaces the job's log.
func (c *Client) RunnerLog(ctx context.Context, runner, id string, b []byte) error {
	resp, err := c.Do(ctx, wire.RouteRunnerLog, []string{id}, nil, b, shaHeader(b, runnerHeader(runner)))
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// RunnerFile stores one result file.
func (c *Client) RunnerFile(ctx context.Context, runner, id, name string, b []byte) error {
	resp, err := c.Do(ctx, wire.RouteRunnerFile, []string{id, name}, nil, bytes.Clone(b), shaHeader(b, runnerHeader(runner)))
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// RunnerFinish reports how the job ended.
func (c *Client) RunnerFinish(ctx context.Context, runner, id string, res wire.JobResult) (wire.JobView, error) {
	var out wire.JobView
	b, h, err := jsonBody(res)
	if err != nil {
		return out, err
	}
	for k, v := range runnerHeader(runner) {
		h[k] = v
	}
	resp, err := c.Do(ctx, wire.RouteRunnerFinish, []string{id}, nil, b, h)
	if err == nil {
		err = decode(resp, 1<<20, &out)
	}
	return out, err
}
