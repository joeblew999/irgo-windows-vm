package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/workerclient"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// Client is workerclient's, for the queue's routes, with each refusal
// classified (ErrAuth, ErrNotFound, ErrGone) and the files checked.
type Client struct {
	URL    string // the Worker's origin
	Runner string // the Mac's name, sent with the runner's requests
	wc     *workerclient.Client
}

// NewClient is a client of the Worker at origin holding a token per scope.
func NewClient(origin string, tokens map[wire.Scope]string) *Client {
	return &Client{URL: strings.TrimSuffix(origin, "/"), wc: workerclient.New(origin, tokens)}
}

// FromEnv is a client from IRGO_REMOTE_URL and the token of scope (its
// variable is wire's: IRGO_REMOTE_TOKEN for a caller, IRGO_REMOTE_RUNNER_TOKEN
// for the Mac), plus IRGO_REMOTE_ADMIN_TOKEN when it is set. A missing one is
// ErrConfig, naming every variable that is missing.
func FromEnv(scope wire.Scope) (*Client, error) {
	info, _ := scope.Info()
	origin := os.Getenv(wire.EnvRemoteURL)
	tok := os.Getenv(info.Env)
	var missing []string
	if origin == "" {
		missing = append(missing, wire.EnvRemoteURL)
	}
	if tok == "" {
		missing = append(missing, info.Env)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: set %s (%s)", ErrConfig, strings.Join(missing, " and "), GuideURL)
	}
	if err := workerclient.CheckOrigin(origin); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrConfig, wire.EnvRemoteURL, err)
	}
	tokens := map[wire.Scope]string{scope: tok}
	if a, ok := wire.ScopeJobsAdmin.Info(); ok && os.Getenv(a.Env) != "" {
		tokens[wire.ScopeJobsAdmin] = os.Getenv(a.Env)
	}
	c := NewClient(origin, tokens)
	c.wc.HTTP = &http.Client{} // no overall timeout: an upload of 95 MB takes minutes
	return c, nil
}

// classify maps the Worker's refusal onto the package's errors.
func classify(err error) error {
	var e *workerclient.Error
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.Status == http.StatusUnauthorized, e.Code == wire.CodeNotConfigured:
		return fmt.Errorf("%w: %w", ErrAuth, err)
	case e.Status == http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case e.Status == http.StatusConflict && e.Route.Scope == wire.ScopeJobsRunner:
		return fmt.Errorf("%w: %w", ErrGone, err)
	}
	return err
}

// Submit creates a job from spec; it waits for its binary.
func (c *Client) Submit(ctx context.Context, spec Spec) (Job, error) {
	out, err := c.wc.JobSubmit(ctx, spec)
	return out.Job, classify(err)
}

// Upload sends the job's binary from path.
func (c *Client) Upload(ctx context.Context, id, path string) (Job, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Job{}, err
	}
	j, err := c.wc.JobInput(ctx, id, b)
	return j, classify(err)
}

// Status is the job now, with its place in the queue.
func (c *Client) Status(ctx context.Context, id string) (Job, error) {
	j, err := c.wc.JobGet(ctx, id)
	return j, classify(err)
}

// Cancel cancels the job.
func (c *Client) Cancel(ctx context.Context, id string) (Job, error) {
	j, err := c.wc.JobCancel(ctx, id)
	return j, classify(err)
}

// List is every job in the index (the admin token).
func (c *Client) List(ctx context.Context) ([]Job, error) {
	l, err := c.wc.JobList(ctx)
	return l.Jobs, classify(err)
}

// Log is the job's log from byte offset, and the offset to ask from next.
func (c *Client) Log(ctx context.Context, id string, offset int64) ([]byte, int64, error) {
	b, next, err := c.wc.JobLog(ctx, id, offset)
	return b, next, classify(err)
}

// File downloads one result file into dir and returns where it went,
// checked against the SHA-256 the Worker stored. With admin the job may be
// any caller's, read with the admin token.
func (c *Client) File(ctx context.Context, id, name, dir string, admin bool) (string, error) {
	if !wire.IsSafeName(name) {
		return "", fmt.Errorf("%q is not a result file name", name)
	}
	body, sum, err := c.wc.JobFile(ctx, id, name, admin)
	if err != nil {
		return "", classify(err)
	}
	defer func() { _ = body.Close() }()
	dst := filepath.Join(dir, name)
	return dst, writeChecked(dst, body, sum)
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
		return fmt.Errorf("%s arrived with SHA-256 %s, which should be %s", filepath.Base(dst), got, want)
	}
	return os.Rename(tmp.Name(), dst)
}

// Claim takes the oldest queued job for this Mac, or returns nil.
func (c *Client) Claim(ctx context.Context) (*Job, error) {
	j, err := c.wc.RunnerClaim(ctx, c.Runner)
	return j, classify(err)
}

// Heartbeat keeps the job's lease and answers whether it was cancelled.
// ErrGone means its lease already ran out or it was ended: stop.
func (c *Client) Heartbeat(ctx context.Context, id string) (bool, error) {
	hb, err := c.wc.RunnerHeartbeat(ctx, c.Runner, id)
	return hb.Cancel, classify(err)
}

// Input downloads the job's binary to dst and checks it against the spec.
func (c *Client) Input(ctx context.Context, j Job, dst string) error {
	body, err := c.wc.RunnerInput(ctx, c.Runner, j.ID)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = body.Close() }()
	return writeChecked(dst, body, j.Spec.SHA256)
}

// PutLog replaces the job's log with everything said so far.
func (c *Client) PutLog(ctx context.Context, id string, b []byte) error {
	return classify(c.wc.RunnerLog(ctx, c.Runner, id, b))
}

// PutFile stores one result file.
func (c *Client) PutFile(ctx context.Context, id, name string, b []byte) error {
	return classify(c.wc.RunnerFile(ctx, c.Runner, id, name, b))
}

// Result is how a job ended on the Mac.
type Result = wire.JobResult

// Finish reports the result. Every file it names must have been stored.
func (c *Client) Finish(ctx context.Context, id string, r Result) (Job, error) {
	j, err := c.wc.RunnerFinish(ctx, c.Runner, id, r)
	return j, classify(err)
}

// SpecFor reads a binary for a spec: its name, size and SHA-256. It must be
// a Windows executable named .exe.
func SpecFor(path, kind string, gui bool, args []string, timeout time.Duration) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, err
	}
	defer func() { _ = f.Close() }() // read-only
	h := sha256.New()
	head := make([]byte, 2)
	_, hErr := io.ReadFull(f, head)
	h.Write(head)
	n, err := io.Copy(h, f)
	if err != nil {
		return Spec{}, err
	}
	name := filepath.Base(path)
	switch {
	case !strings.HasSuffix(strings.ToLower(name), ".exe"):
		return Spec{}, fmt.Errorf("%s: the binary must be a Windows .exe (GOOS=windows GOARCH=arm64)", path)
	case hErr != nil || string(head) != "MZ":
		return Spec{}, errors.New(path + " is not a Windows executable (no MZ header); build it with GOOS=windows GOARCH=arm64 CGO_ENABLED=0")
	}
	return Spec{Kind: kind, Name: name, Size: n + 2, SHA256: hex.EncodeToString(h.Sum(nil)), GUI: gui, Args: args,
		TimeoutS: int(timeout / time.Second)}, nil
}
