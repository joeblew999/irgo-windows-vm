package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// Wait follows a job until it is final: the Mac's log as it grows, written to
// out, and a line whenever its state or place in the queue changes. It
// returns the final job. A Worker that does not answer is asked again, so a
// dropped connection does not end the wait; ctx does.
func Wait(ctx context.Context, c *Client, id string, out io.Writer, poll time.Duration) (Job, error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	var off int64
	var last string
	failures := 0
	for {
		j, err := c.Status(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return j, ctx.Err()
			}
			if failures++; failures > 30 {
				return j, err
			}
			_, _ = fmt.Fprintf(out, "asking for job %s: %v; trying again\n", id, err)
		} else {
			failures = 0
			if b, size, lErr := c.Log(ctx, id, off); lErr == nil {
				_, _ = out.Write(b)
				off = size
			}
			now := j.State
			if j.State == wire.JobQueued {
				now = fmt.Sprintf("queued, %d ahead of it, %d running", j.Position-1, j.Running)
			}
			if j.State == wire.JobRunning {
				now = "running on " + j.Runner
			}
			if now != last {
				_, _ = fmt.Fprintf(out, "job %s: %s\n", id, now)
				last = now
			}
			if wire.JobFinal(j.State) {
				return j, nil
			}
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Fetch downloads every result file of a final job into dir, and returns
// their paths.
func Fetch(ctx context.Context, c *Client, j Job, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range j.Files {
		p, err := c.File(ctx, j.ID, f.Key, dir)
		if err != nil {
			return paths, fmt.Errorf("%s: %w", f.Key, err)
		}
		paths = append(paths, p)
	}
	return paths, nil
}
