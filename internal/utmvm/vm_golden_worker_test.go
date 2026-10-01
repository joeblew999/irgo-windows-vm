package utmvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenEnv is every variable R2ConfigFromEnv reads.
var goldenEnv = []string{
	"IRGO_GOLDEN_URL", "IRGO_GOLDEN_TOKEN", "IRGO_GOLDEN_PUSH_TOKEN",
	"IRGO_R2_ACCOUNT_ID", "IRGO_R2_BUCKET", "IRGO_R2_ACCESS_KEY_ID", "IRGO_R2_SECRET_ACCESS_KEY", "IRGO_R2_API_TOKEN",
}

func clearGoldenEnv(t *testing.T) {
	for _, n := range goldenEnv {
		t.Setenv(n, "")
	}
}

// Without IRGO_GOLDEN_URL the S3 transport needs all five of its variables,
// and every missing one is named at once.
//
// Negative control, run by hand: return on the first missing variable and the
// count check fails.
func TestR2ConfigFromEnvNamesEveryMissingVariable(t *testing.T) {
	clearGoldenEnv(t)
	t.Setenv("IRGO_R2_BUCKET", "b")
	_, err := R2ConfigFromEnv(false)
	if !errors.Is(err, ErrR2NotConfigured) {
		t.Fatalf("got %v", err)
	}
	for _, e := range r2Env {
		if e.name != "IRGO_R2_BUCKET" && !strings.Contains(err.Error(), e.name) {
			t.Errorf("%s is missing and not named: %v", e.name, err)
		}
	}
	for _, e := range r2Env {
		t.Setenv(e.name, "x")
	}
	c, err := R2ConfigFromEnv(true)
	if err != nil || c.viaWorker() {
		t.Errorf("a full S3 environment: %v, via the Worker %v", err, c.viaWorker())
	}
}

// IRGO_GOLDEN_URL selects the Worker: a read token always, the write token
// to change the bucket, the account and bucket only when IRGO_R2_API_TOKEN
// asks for the public-access check, and the tokens only over https or to
// localhost.
//
// Negative control, run by hand 1 Oct 2026: drop `if write` (always require
// the push token) and the pull case fails.
func TestR2ConfigFromEnvSelectsTheWorker(t *testing.T) {
	for _, c := range []struct {
		name    string
		env     map[string]string
		write   bool
		missing []string // named in the error; nil means accepted
	}{
		{"pull", map[string]string{"IRGO_GOLDEN_URL": "https://w.example.workers.dev", "IRGO_GOLDEN_TOKEN": "r"}, false, nil},
		{"push without the write token", map[string]string{"IRGO_GOLDEN_URL": "https://w.example.workers.dev", "IRGO_GOLDEN_TOKEN": "r"},
			true, []string{"IRGO_GOLDEN_PUSH_TOKEN"}},
		{"no token at all", map[string]string{"IRGO_GOLDEN_URL": "https://w.example.workers.dev"},
			true, []string{"IRGO_GOLDEN_TOKEN", "IRGO_GOLDEN_PUSH_TOKEN"}},
		{"push", map[string]string{"IRGO_GOLDEN_URL": "https://w.example.workers.dev/", "IRGO_GOLDEN_TOKEN": "r", "IRGO_GOLDEN_PUSH_TOKEN": "p"}, true, nil},
		{"the privacy check needs the bucket", map[string]string{"IRGO_GOLDEN_URL": "https://w.example.workers.dev",
			"IRGO_GOLDEN_TOKEN": "r", "IRGO_R2_API_TOKEN": "a"}, false, []string{"IRGO_R2_ACCOUNT_ID", "IRGO_R2_BUCKET"}},
		{"plain http elsewhere", map[string]string{"IRGO_GOLDEN_URL": "http://w.example.workers.dev", "IRGO_GOLDEN_TOKEN": "r"},
			false, []string{"https"}},
		{"a path", map[string]string{"IRGO_GOLDEN_URL": "https://w.example.workers.dev/api/golden", "IRGO_GOLDEN_TOKEN": "r"},
			false, []string{"not an origin"}},
		{"wrangler dev", map[string]string{"IRGO_GOLDEN_URL": "http://localhost:8787", "IRGO_GOLDEN_TOKEN": "r"}, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearGoldenEnv(t)
			t.Setenv("IRGO_R2_ACCESS_KEY_ID", "") // S3's variables are not asked for
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg, err := R2ConfigFromEnv(c.write)
			if c.missing == nil {
				if err != nil || !cfg.viaWorker() || strings.HasSuffix(cfg.WorkerURL, "/") {
					t.Errorf("refused or not the Worker: %v %+v", err, cfg)
				}
				return
			}
			if !errors.Is(err, ErrR2NotConfigured) {
				t.Fatalf("got %v, want ErrR2NotConfigured", err)
			}
			for _, m := range c.missing {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("%q is not in %v", m, err)
				}
			}
			if strings.Contains(err.Error(), "IRGO_R2_ACCESS_KEY_ID") {
				t.Errorf("the Worker transport asked for S3 keys: %v", err)
			}
		})
	}
}

// Through the Worker without IRGO_R2_API_TOKEN nothing is asked of the
// Cloudflare API, and the output says the bucket's own public routes were not
// checked rather than that it is private.
//
// Negative control, run by hand 1 Oct 2026: skip the Worker branch in
// requirePrivate and the API, which nothing answers, is asked and refuses.
func TestGoldenWorkerWithoutAPITokenSaysWhatItDidNotCheck(t *testing.T) {
	fake, r := newFakeWorker(t)
	r.APIToken, r.apiBase = "", "http://127.0.0.1:1" // nothing listens there
	fake.managedOn = true                            // invisible without the API
	var out strings.Builder
	say := func(f string, a ...any) { out.WriteString(strings.TrimSpace(f) + "\n") }
	if err := r.requirePrivate(context.Background(), say); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NOT checked") || strings.Contains(out.String(), "private") {
		t.Errorf("it said:\n%s", out.String())
	}
	bundle := makeBundle(t, t.TempDir())
	if _, err := GoldenPush(context.Background(), pushOpts(r, bundle, ""), testSay(t)); err != nil {
		t.Errorf("push through the Worker with no API token: %v", err)
	}
}

// The Worker's tokens each do one job: no token and a wrong token read
// nothing, and the read token cannot write or delete. Nothing is stored by a
// refused push, and a refused pull leaves no bundle.
//
// Which token the Worker accepts is the Worker's test (worker/golden_test.go,
// TestGoldenRefusals); this one shows the CLI stops on the refusal and
// leaves nothing behind. Negative control, run by hand 1 Oct 2026: let the
// fake accept the read token for PUT and the "read token cannot push" case
// fails.
func TestGoldenWorkerRefusesTheWrongToken(t *testing.T) {
	ctx := context.Background()
	bundle := makeBundle(t, t.TempDir())

	for _, c := range []struct {
		name        string
		read, write string
	}{
		{"no tokens", "", ""},
		{"read token cannot push", fakeReadToken, fakeReadToken},
		{"wrong read token", "nope", fakePushToken},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake, r := newFakeWorker(t)
			r.Token, r.PushToken = c.read, c.write
			_, err := GoldenPush(ctx, pushOpts(r, bundle, ""), testSay(t))
			if err == nil || !strings.Contains(err.Error(), "401") {
				t.Errorf("push: %v, want a 401", err)
			}
			if fake.refused == 0 || len(fake.keys(goldenPrefix)) != 0 {
				t.Errorf("%d refused, and stored %v", fake.refused, fake.keys(goldenPrefix))
			}
		})
	}

	fake, r := newFakeWorker(t)
	pushed, err := GoldenPush(ctx, pushOpts(r, bundle, ""), testSay(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", fakePushToken} {
		bad := r
		bad.Token = tok
		dir := filepath.Join(t.TempDir(), "pull")
		if _, err := GoldenPull(ctx, GoldenPullOptions{R2: bad, Dir: dir, Parallel: 1}, testSay(t)); err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("pull with read token %q: %v, want a 401", tok, err)
		}
		if _, sErr := os.Stat(filepath.Join(dir, "irgo-golden.utm")); !errors.Is(sErr, os.ErrNotExist) {
			t.Error("a refused pull left a bundle")
		}
	}

	readOnly := r
	readOnly.PushToken = fakeReadToken
	rm, err := InspectGoldenCacheRemoval(ctx, readOnly, "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("listing with the read token: %+v %v, want a 401", rm, err)
	}
	if err := GoldenCacheDelete(ctx, readOnly, GoldenCacheRemoval{ID: pushed.ID}, testSay(t)); err == nil {
		t.Error("deleting with the read token succeeded")
	}
	if _, ok := fake.objects[goldenManifestKey(pushed.ID)]; !ok {
		t.Error("the manifest is gone after a refused delete")
	}
}

// A body damaged on the way is refused by the bucket (R2, behind the real
// Worker, checks X-Golden-Sha256), the push fails, and latest is not written.
//
// Negative control, run by hand 1 Oct 2026: have workerStore.put fail only on
// a 5xx and the push reports success.
func TestGoldenWorkerPushRefusedWhenTheBodyArrivesDamaged(t *testing.T) {
	fake, r := newFakeWorker(t)
	fake.corruptPut = true
	_, err := GoldenPush(context.Background(), pushOpts(r, makeBundle(t, t.TempDir()), ""), testSay(t))
	if err == nil || !strings.Contains(err.Error(), "does not hash") {
		t.Fatalf("push over a damaging link: %v", err)
	}
	if left := fake.keys(goldenPrefix); len(left) != 0 {
		t.Errorf("stored despite the damage: %v", left)
	}
}
