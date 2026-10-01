package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client talks to the Worker's job queue. One method per endpoint; the
// waiting, printing and mapping to exit codes are the callers'.
type Client struct {
	URL    string // the Worker, no trailing slash
	Token  string
	Runner string // the Mac's name, sent as X-Runner by the runner calls
	HTTP   *http.Client
}

// FromEnv is a client from IRGO_REMOTE_URL and the token named by tokenVar,
// or ErrConfig naming every variable that is missing.
func FromEnv(tokenVar string) (*Client, error) {
	c := &Client{URL: strings.TrimRight(os.Getenv(EnvURL), "/"), Token: os.Getenv(tokenVar)}
	var missing []string
	if c.URL == "" {
		missing = append(missing, EnvURL)
	}
	if c.Token == "" {
		missing = append(missing, tokenVar)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: set %s (docs/DEVELOPMENT.md, \"Driving a Mac from anywhere\")", ErrConfig, strings.Join(missing, " and "))
	}
	return c, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// No overall timeout: an upload of 95 MB on a slow line takes minutes.
	// Each call has its context instead.
	return http.DefaultClient
}

// apiError is a refusal from the Worker, with its message.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("the Worker answered %d: %s", e.Status, e.Msg) }

// call makes one request and returns the response, which the caller closes,
// or an error classified by status: ErrAuth, ErrNotFound, ErrGone, or the
// Worker's own message.
func (c *Client) call(ctx context.Context, method, path string, body io.Reader, size int64, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.Runner != "" {
		req.Header.Set("X-Runner", c.Runner)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 300 {
		return res, nil
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	ae := &apiError{res.StatusCode, msg}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w: %w", ErrAuth, ae)
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %w", ErrNotFound, ae)
	case http.StatusConflict:
		if strings.HasPrefix(path, "/api/runner/jobs/") {
			return nil, fmt.Errorf("%w: %w", ErrGone, ae)
		}
	}
	return nil, ae
}

// callJSON makes a request with a JSON body (or none) and decodes the JSON
// answer into out, when out is not nil.
func (c *Client) callJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	var size int64
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, size = bytes.NewReader(b), int64(len(b))
	}
	res, err := c.call(ctx, method, path, body, size, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func jobPath(id string, rest ...string) string {
	return "/api/jobs/" + strings.Join(append([]string{id}, rest...), "/")
}

func runnerPath(id string, rest ...string) string {
	return "/api/runner/jobs/" + strings.Join(append([]string{id}, rest...), "/")
}

// Submit creates a job from spec and returns it, waiting for its binary.
func (c *Client) Submit(ctx context.Context, spec Spec) (Job, error) {
	var out struct {
		Job Job `json:"job"`
	}
	err := c.callJSON(ctx, http.MethodPost, "/api/jobs", spec, &out)
	return out.Job, err
}

// Upload sends the job's binary from path. The Worker checks it against the
// spec's size and SHA-256 and queues the job.
func (c *Client) Upload(ctx context.Context, id, path string) (Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = f.Close() }() // read-only
	st, err := f.Stat()
	if err != nil {
		return Job{}, err
	}
	res, err := c.call(ctx, http.MethodPut, jobPath(id, "input"), f, st.Size(), map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var j Job
	return j, json.NewDecoder(res.Body).Decode(&j)
}

// Status is the job now, with its place in the queue.
func (c *Client) Status(ctx context.Context, id string) (Job, error) {
	var j Job
	return j, c.callJSON(ctx, http.MethodGet, jobPath(id), nil, &j)
}

// Cancel cancels the job: at once if it has not started, otherwise its Mac
// stops it at the next heartbeat.
func (c *Client) Cancel(ctx context.Context, id string) (Job, error) {
	var j Job
	return j, c.callJSON(ctx, http.MethodPost, jobPath(id, "cancel"), nil, &j)
}

// List is every job in the index. Admin token only.
func (c *Client) List(ctx context.Context) ([]Job, error) {
	var out struct {
		Jobs []Job `json:"jobs"`
	}
	return out.Jobs, c.callJSON(ctx, http.MethodGet, "/api/jobs", nil, &out)
}

// Log is the job's log from byte offset, and its whole size, which is the
// offset to ask from next.
func (c *Client) Log(ctx context.Context, id string, offset int64) ([]byte, int64, error) {
	res, err := c.call(ctx, http.MethodGet, jobPath(id, "log")+"?offset="+strconv.FormatInt(offset, 10), nil, 0, nil)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, offset, err
	}
	size, err := strconv.ParseInt(res.Header.Get("X-Log-Size"), 10, 64)
	if err != nil {
		return b, offset + int64(len(b)), nil
	}
	return b, size, nil
}

// File downloads one result file into dir and returns where it went. Its
// SHA-256 is checked against what the Mac stored when the Worker says.
func (c *Client) File(ctx context.Context, id, name, dir string) (string, error) {
	if filepath.Base(name) != name || name == "" || name[0] == '.' {
		return "", fmt.Errorf("%q is not a result file name", name)
	}
	res, err := c.call(ctx, http.MethodGet, jobPath(id, "files", name), nil, 0, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	dst := filepath.Join(dir, name)
	return dst, writeChecked(dst, res.Body, res.Header.Get("X-Job-Sha256"))
}

// writeChecked writes r to dst through a temporary file, and renames it into
// place only once it hashes to want (when want is set).
func writeChecked(dst string, r io.Reader, want string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".part-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after the rename
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		_ = tmp.Close() // already failing
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("%s arrived with SHA-256 %s, the Worker stored %s", filepath.Base(dst), got, want)
	}
	return os.Rename(tmp.Name(), dst)
}

// Claim takes the oldest queued job for this Mac, or returns nil when there
// is none.
func (c *Client) Claim(ctx context.Context) (*Job, error) {
	res, err := c.call(ctx, http.MethodPost, "/api/runner/claim", nil, 0, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	var j Job
	return &j, json.NewDecoder(res.Body).Decode(&j)
}

// Heartbeat keeps the job's lease and answers whether it was cancelled.
// ErrGone means its lease already ran out or it was ended: stop.
func (c *Client) Heartbeat(ctx context.Context, id string) (cancel bool, err error) {
	var out struct {
		Cancel bool `json:"cancel"`
	}
	return out.Cancel, c.callJSON(ctx, http.MethodPost, runnerPath(id, "heartbeat"), nil, &out)
}

// Input downloads the job's binary to dst and checks it against the spec.
func (c *Client) Input(ctx context.Context, j Job, dst string) error {
	res, err := c.call(ctx, http.MethodGet, runnerPath(j.ID, "input"), nil, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	return writeChecked(dst, res.Body, j.Spec.SHA256)
}

// put sends bytes with their SHA-256, which R2 checks as they arrive.
func (c *Client) put(ctx context.Context, path string, b []byte) error {
	s := sha256.Sum256(b)
	res, err := c.call(ctx, http.MethodPut, path, bytes.NewReader(b), int64(len(b)),
		map[string]string{"X-Job-Sha256": hex.EncodeToString(s[:])})
	if err != nil {
		return err
	}
	return res.Body.Close()
}

// PutLog replaces the job's log with everything said so far.
func (c *Client) PutLog(ctx context.Context, id string, b []byte) error {
	return c.put(ctx, runnerPath(id, "log"), b)
}

// PutFile stores one result file.
func (c *Client) PutFile(ctx context.Context, id, name string, b []byte) error {
	return c.put(ctx, runnerPath(id, "files", name), b)
}

// Result is how a job ended on the Mac.
type Result struct {
	ExitCode int      `json:"exit_code"`
	Outcome  string   `json:"outcome"`
	Message  string   `json:"message,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// Finish reports the result. Every file it names must have been stored.
func (c *Client) Finish(ctx context.Context, id string, r Result) (Job, error) {
	var j Job
	return j, c.callJSON(ctx, http.MethodPost, runnerPath(id, "finish"), r, &j)
}

// SpecFor reads a binary for a spec: its name, size and SHA-256.
func SpecFor(path, kind string, gui bool, args []string, timeout time.Duration) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, err
	}
	defer func() { _ = f.Close() }() // read-only
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Spec{}, err
	}
	name := filepath.Base(path)
	if !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return Spec{}, fmt.Errorf("%s: the binary must be a Windows .exe (GOOS=windows GOARCH=arm64)", path)
	}
	if !isPE(path) {
		return Spec{}, errors.New(path + " is not a Windows executable (no MZ header); build it with GOOS=windows GOARCH=arm64 CGO_ENABLED=0")
	}
	return Spec{Kind: kind, Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), GUI: gui, Args: args,
		TimeoutS: int(timeout / time.Second)}, nil
}

// isPE is whether the file starts as a Windows executable does.
func isPE(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // read-only
	b := make([]byte, 2)
	_, err = io.ReadFull(f, b)
	return err == nil && string(b) == "MZ"
}
