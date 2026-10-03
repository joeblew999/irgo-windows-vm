package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	fleet "github.com/joeblew999/fleet-api/sdk/go"
	"github.com/joeblew999/fleet-api/sdk/go/client"
	"github.com/joeblew999/fleet-api/sdk/go/core"
	"github.com/joeblew999/fleet-api/sdk/go/option"
)

// Reporter sends device reports to fleet-api, spooling each first, so a
// report that cannot be sent is kept and sent with the next, in order. It
// never fails its caller: what went wrong is said, once until it changes.
type Reporter struct {
	// Post sends one report; NewPost is the real one.
	Post func(ctx context.Context, r *fleet.DeviceReport) error
	Dir  string // the spool
	Now  func() time.Time
	// Backoff is how long after a failed send nothing is tried. 0: a minute.
	Backoff time.Duration

	mu        sync.Mutex
	failedAt  time.Time
	lastError string
}

// spoolMax is how many reports are kept unsent: a week at one every five
// minutes. Past it the oldest go.
const spoolMax = 2016

// Send spools r and sends everything spooled, oldest first. A nil Reporter
// does nothing.
func (p *Reporter) Send(ctx context.Context, r *fleet.DeviceReport, say func(string, ...any)) {
	if p == nil {
		return
	}
	if err := p.spool(r); err != nil {
		say("spooling the report: %v; sending it without", err)
		if err := p.Post(ctx, r); err != nil {
			say("sending the report: %v; it is lost", err)
		}
		return
	}
	p.flush(ctx, say, true)
}

// Last is Send for the last report a keeper sends, as it stops: it is tried
// whatever the backoff, since there is no later pass to try it.
func (p *Reporter) Last(ctx context.Context, r *fleet.DeviceReport, say func(string, ...any)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.failedAt = time.Time{}
	p.mu.Unlock()
	p.Send(ctx, r, say)
}

// Flush sends what is spooled, unless a send failed less than Backoff ago.
func (p *Reporter) Flush(ctx context.Context, say func(string, ...any)) {
	if p == nil {
		return
	}
	p.flush(ctx, say, false)
}

func (p *Reporter) flush(ctx context.Context, say func(string, ...any), fresh bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	backoff := p.Backoff
	if backoff == 0 {
		backoff = time.Minute
	}
	if !p.failedAt.IsZero() && now.Sub(p.failedAt) < backoff {
		if fresh {
			say("not sending the report now: the last send failed %s ago; it is spooled in %s", now.Sub(p.failedAt).Round(time.Second), p.Dir)
		}
		return
	}
	names, err := p.spooled()
	if err != nil {
		say("reading the spool %s: %v", p.Dir, err)
		return
	}
	sent := 0
	for _, name := range names {
		path := filepath.Join(p.Dir, name)
		b, err := os.ReadFile(path)
		var r fleet.DeviceReport
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			say("dropping %s, which cannot be read: %v", name, err)
			_ = os.Remove(path)
			continue
		}
		err = p.Post(ctx, &r)
		switch {
		case err == nil:
		case errors.Is(err, ErrRefused):
			say("fleet-api refused the %s report of %s, so it is dropped: %v", r.Reason, time.UnixMilli(r.Ts).Format("15:04:05"), err)
		default:
			p.failedAt = now
			if msg := err.Error(); msg != p.lastError {
				p.lastError = msg
				say("sending reports to fleet-api: %v; %d kept in %s, tried again in %s", err, len(names)-sent, p.Dir, backoff)
			}
			return
		}
		if err := os.Remove(path); err != nil {
			say("removing the sent report %s: %v", path, err)
			return
		}
		sent++
	}
	if p.lastError != "" || (sent > 1) {
		say("sent %d reports to fleet-api", sent)
	}
	p.failedAt, p.lastError = time.Time{}, ""
}

// spool writes r as the next file, through a temporary file and a rename,
// and drops the oldest past spoolMax.
func (p *Reporter) spool(r *fleet.DeviceReport) error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(p.Dir, ".report-*")
	if err != nil {
		return err
	}
	_, wErr := f.Write(b)
	cErr := f.Close()
	if err := errors.Join(wErr, cErr); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	name := fmt.Sprintf("%016d-%s.json", r.Ts, r.Reason)
	if err := os.Rename(f.Name(), filepath.Join(p.Dir, name)); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	names, err := p.spooled()
	if err == nil && len(names) > spoolMax {
		for _, old := range names[:len(names)-spoolMax] {
			_ = os.Remove(filepath.Join(p.Dir, old))
		}
	}
	return nil
}

// spooled is the spooled reports, oldest first (the names start with the
// report's time, zero-padded).
func (p *Reporter) spooled() ([]string, error) {
	entries, err := os.ReadDir(p.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (p *Reporter) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// NewPost is the real send: fleet-api's Go SDK at url with the write token,
// one attempt with a 10 s limit (the loop is the retry). A refusal for what
// the report says is ErrRefused.
func NewPost(url, token string) func(ctx context.Context, r *fleet.DeviceReport) error {
	c := client.NewClient(
		option.WithBaseURL(strings.TrimRight(url, "/")),
		option.WithToken(token),
		option.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
		option.WithMaxAttempts(1),
	)
	return func(ctx context.Context, r *fleet.DeviceReport) error {
		_, err := c.Devices.Report(ctx, &fleet.ReportDevicesRequest{ID: r.ID, Body: r})
		switch status(err) {
		case 0:
			return err
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %w", ErrRefused, err)
		case http.StatusUnauthorized:
			return fmt.Errorf("the write token was refused (401): %w", err)
		}
		return err
	}
}

// status is the HTTP status an SDK error carries, or 0 for none (no answer,
// or no error).
func status(err error) int {
	if err == nil {
		return 0
	}
	var (
		unprocessable *fleet.UnprocessableEntityError
		tooLarge      *fleet.ContentTooLargeError
		unauthorized  *fleet.UnauthorizedError
		notFound      *fleet.NotFoundError
		api           *core.APIError
	)
	switch {
	case errors.As(err, &unprocessable):
		return http.StatusUnprocessableEntity
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.As(err, &unauthorized):
		return http.StatusUnauthorized
	case errors.As(err, &notFound):
		return http.StatusNotFound
	case errors.As(err, &api):
		return api.StatusCode
	}
	return 0
}
