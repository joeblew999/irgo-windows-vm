package main

// The Worker's HTTP API. The site itself is not here: Cloudflare serves
// site/dist as static assets before the Worker runs, and only /api/* reaches
// this handler (wrangler.toml, run_worker_first).
//
//	GET  /api/health                              no secrets, says the Worker is up
//	GET  /api/glaze-status                        the latest run per target, JSON
//	POST /api/glaze-status/{target}               a run: shots.json and its pictures (token)
//	GET  /api/glaze-status/{target}/runs/{id}/{f} one stored file of a run
//	GET  /api/golden/{key...}                     302 to a presigned R2 URL (token)
//
// Everything here is plain Go behind two small interfaces (Env, Store), so it
// is tested with `go test` on the host; main_js.go binds them to R2 and the
// Worker's environment.

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Store is the part of an R2 bucket the API uses. Get returns ok=false for a
// key that does not exist. There is deliberately no List: nothing here lists.
type Store interface {
	Get(key string) (body []byte, contentType string, ok bool, err error)
	Put(key string, body []byte, contentType string) error
}

// Env is what the platform supplies per request.
type Env struct {
	Var  func(name string) string // vars and secrets from wrangler.toml / wrangler secret
	Site func() (Store, error)    // the SITE bucket: glaze status, public data
	Now  func() time.Time
}

// The names of the vars and secrets Env.Var is asked for. docs/DEVELOPMENT.md,
// "The Cloudflare Worker", lists them with how each is set.
const (
	varGlazeToken    = "GLAZE_STATUS_TOKEN"       // secret: who may post a run
	varGoldenToken   = "GOLDEN_TOKEN"             // secret: who may fetch the golden image
	varAccountID     = "R2_ACCOUNT_ID"            // var
	varGoldenBucket  = "GOLDEN_BUCKET"            // var: the private bucket's name
	varGoldenKeyID   = "GOLDEN_ACCESS_KEY_ID"     // secret: R2 S3 token, read-only, that bucket only
	varGoldenSecret  = "GOLDEN_SECRET_ACCESS_KEY" // secret
	goldenURLExpires = 30 * time.Minute           // what vm-golden-pull's own presign uses
)

// Limits on a posted run. A run today is 7 pictures of 2–25 KB.
const (
	maxRunBytes     = 16 << 20
	maxPictureBytes = 4 << 20
	maxPictures     = 64
)

var (
	targets  = map[string]bool{"mac": true, "windows": true}
	pngMagic = []byte("\x89PNG\r\n\x1a\n")
)

// The validators below are plain loops, not regexps: compiling
// `[0-9a-f]{64}` at init overflows TinyGo's Wasm stack ("fatal error: stack
// overflow" before main, measured 1 Oct 2026 under wrangler dev), and the
// host tests cannot see that.

// isHex is s being exactly n lower-case hex digits.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return allBytes(s, func(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' })
}

func allBytes(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// isRunID is a run's id: the first 8 bytes of its manifest's SHA-256, in hex.
func isRunID(s string) bool { return isHex(s, 16) }

// isPicture is a safe picture name: [A-Za-z0-9_.-]{1,100} then .png, so it
// can be neither a path nor a hidden file.
func isPicture(s string) bool {
	stem, ok := strings.CutSuffix(s, ".png")
	if !ok || stem == "" || len(stem) > 100 || stem[0] == '.' {
		return false
	}
	return allBytes(stem, func(c byte) bool {
		return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
	})
}

// isGoldenKey is one of the golden image's keys, exactly as
// internal/utmvm/vm_golden_cache.go writes them under golden/: latest,
// manifests/<sha256>.json, chunks/<sha256>.zst. Anything else is refused: no
// listing, and no other object in the bucket is reachable through here.
func isGoldenKey(k string) bool {
	if k == "golden/latest" {
		return true
	}
	if id, ok := strings.CutPrefix(k, "golden/manifests/"); ok {
		id, ok = strings.CutSuffix(id, ".json")
		return ok && isHex(id, 64)
	}
	if id, ok := strings.CutPrefix(k, "golden/chunks/"); ok {
		id, ok = strings.CutSuffix(id, ".zst")
		return ok && isHex(id, 64)
	}
	return false
}

// Handler is the whole API.
//
// Routed by hand, not with ServeMux's "GET /x/{y}" patterns: under TinyGo
// 0.42 those patterns never match (measured 1 Oct 2026, wrangler dev: a mux
// with only "GET /api/health" answered that path 404), while `go test`
// passes on the host, so the tests would not have caught it.
func Handler(env Env) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
		get := r.Method == http.MethodGet
		switch {
		case !strings.HasPrefix(r.URL.Path, "/api/"):
		case get && len(p) == 1 && p[0] == "health":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case get && len(p) == 1 && p[0] == "glaze-status":
			env.glazeLatest(w, r)
			return
		case r.Method == http.MethodPost && len(p) == 2 && p[0] == "glaze-status":
			env.glazePost(w, r, p[1])
			return
		case get && len(p) == 5 && p[0] == "glaze-status" && p[2] == "runs":
			env.glazeFile(w, r, p[1], p[3], p[4])
			return
		case (get || r.Method == http.MethodHead) && len(p) >= 1 && p[0] == "golden":
			env.golden(w, r, strings.TrimPrefix(r.URL.Path, "/api/golden/"))
			return
		}
		fail(w, http.StatusNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path)
	})
}

// authorized answers whether the request carries the secret named by v. A
// secret that is not set refuses everything: an unconfigured Worker must not
// fall open. Compared as SHA-256 digests so the comparison does not depend on
// the length of either.
func (env Env) authorized(w http.ResponseWriter, r *http.Request, v string) bool {
	want := env.Var(v)
	if want == "" {
		fail(w, http.StatusServiceUnavailable, "refused: %s is not set on this Worker", v)
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	g, h := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	if !ok || got == "" || subtle.ConstantTimeCompare(g[:], h[:]) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="irgo-windows-vm"`)
		fail(w, http.StatusUnauthorized, "refused: no valid bearer token")
		return false
	}
	return true
}

// latest is glaze/<target>/latest.json: which run is newest. It is written
// last, after the run's files, so a reader never sees a run half stored.
type latest struct {
	Target   string          `json:"target"`
	Run      string          `json:"run"`
	Received time.Time       `json:"received"`
	Base     string          `json:"base"` // where the run's files are served, ending in /
	Manifest json.RawMessage `json:"manifest"`
}

func latestKey(target string) string { return "glaze/" + target + "/latest.json" }
func runKey(target, id, file string) string {
	return "glaze/" + target + "/runs/" + id + "/" + file
}

// manifestView is the part of glazecheck.Manifest (internal/glazecheck/shots.go,
// written as shots.json) the Worker checks. The manifest is stored as the bytes
// that were posted, never re-encoded, so this is not a second definition of it.
type manifestView struct {
	Target string
	When   time.Time
	Tests  []struct{ Picture string }
}

func (env Env) glazeLatest(w http.ResponseWriter, r *http.Request) {
	st, err := env.Site()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the SITE bucket: %v", err)
		return
	}
	out := map[string]json.RawMessage{}
	for t := range targets {
		b, _, ok, gErr := st.Get(latestKey(t))
		switch {
		case gErr != nil:
			fail(w, http.StatusBadGateway, "reading %s: %v", latestKey(t), gErr)
			return
		case !ok:
			out[t] = json.RawMessage("null")
		default:
			out[t] = b
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (env Env) glazePost(w http.ResponseWriter, r *http.Request, target string) {
	if !env.authorized(w, r, varGlazeToken) {
		return
	}
	if !targets[target] {
		fail(w, http.StatusNotFound, "no such target %q: mac or windows", target)
		return
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		fail(w, http.StatusUnsupportedMediaType, "send multipart/form-data: a part named manifest (shots.json) and one part per picture, named by its file name")
		return
	}
	manifest, pictures, err := readRun(multipart.NewReader(http.MaxBytesReader(w, r.Body, maxRunBytes), params["boundary"]))
	if err != nil {
		fail(w, http.StatusBadRequest, "%v", err)
		return
	}
	var m manifestView
	if err := json.Unmarshal(manifest, &m); err != nil {
		fail(w, http.StatusBadRequest, "manifest is not shots.json: %v", err)
		return
	}
	if m.Target != target {
		fail(w, http.StatusBadRequest, "manifest is for target %q, posted to %q", m.Target, target)
		return
	}
	if m.When.IsZero() {
		fail(w, http.StatusBadRequest, "manifest has no When")
		return
	}
	named := map[string]bool{}
	for _, t := range m.Tests {
		if t.Picture == "" {
			continue
		}
		named[t.Picture] = true
		if _, ok := pictures[t.Picture]; !ok {
			fail(w, http.StatusBadRequest, "manifest names %s, which was not sent", t.Picture)
			return
		}
	}
	for name := range pictures {
		if !named[name] {
			fail(w, http.StatusBadRequest, "%s was sent but the manifest does not name it", name)
			return
		}
	}

	st, err := env.Site()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the SITE bucket: %v", err)
		return
	}
	// An older run must not replace a newer one: a re-run of an old workflow
	// would otherwise roll the page back. A current record that cannot be read
	// is cannot tell, and refuses.
	if b, _, ok, gErr := st.Get(latestKey(target)); gErr != nil {
		fail(w, http.StatusBadGateway, "reading %s: %v", latestKey(target), gErr)
		return
	} else if ok {
		var cur latest
		var cm manifestView
		if jErr := json.Unmarshal(b, &cur); jErr != nil || json.Unmarshal(cur.Manifest, &cm) != nil {
			fail(w, http.StatusConflict, "cannot tell whether this run is newer: %s does not parse; delete it to start again", latestKey(target))
			return
		}
		if m.When.Before(cm.When) {
			fail(w, http.StatusConflict, "refused: this run (%s) is older than the stored one (%s)", m.When.Format(time.RFC3339), cm.When.Format(time.RFC3339))
			return
		}
	}

	sum := sha256.Sum256(manifest)
	id := hex.EncodeToString(sum[:8])
	for name, b := range pictures {
		if err := st.Put(runKey(target, id, name), b, "image/png"); err != nil {
			fail(w, http.StatusBadGateway, "storing %s: %v", name, err)
			return
		}
	}
	if err := st.Put(runKey(target, id, "shots.json"), manifest, "application/json"); err != nil {
		fail(w, http.StatusBadGateway, "storing shots.json: %v", err)
		return
	}
	rec, err := json.Marshal(latest{
		Target: target, Run: id, Received: env.Now().UTC(),
		Base:     "/api/glaze-status/" + target + "/runs/" + id + "/",
		Manifest: manifest,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if err := st.Put(latestKey(target), rec, "application/json"); err != nil {
		fail(w, http.StatusBadGateway, "storing %s: %v", latestKey(target), err)
		return
	}
	// Success only once the record reads back as written.
	back, _, ok, err := st.Get(latestKey(target))
	if err != nil || !ok || !bytes.Equal(back, rec) {
		fail(w, http.StatusBadGateway, "%s did not read back as written (err=%v, found=%v)", latestKey(target), err, ok)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"target": target, "run": id, "pictures": len(pictures)})
}

// readRun reads the posted parts: exactly one manifest, and pictures that are
// PNGs with safe names, each at most once.
func readRun(mr *multipart.Reader) (manifest []byte, pictures map[string][]byte, err error) {
	pictures = map[string][]byte{}
	for {
		p, pErr := mr.NextPart()
		if errors.Is(pErr, io.EOF) {
			break
		}
		if pErr != nil {
			return nil, nil, fmt.Errorf("reading the form: %v", pErr)
		}
		name := p.FormName()
		b, rErr := io.ReadAll(io.LimitReader(p, maxPictureBytes+1))
		if rErr != nil {
			return nil, nil, fmt.Errorf("reading part %q: %v", name, rErr)
		}
		if len(b) > maxPictureBytes {
			return nil, nil, fmt.Errorf("part %q is over %d bytes", name, maxPictureBytes)
		}
		switch {
		case name == "manifest":
			if manifest != nil {
				return nil, nil, errors.New("manifest sent twice")
			}
			manifest = b
		case isPicture(name):
			if _, dup := pictures[name]; dup {
				return nil, nil, fmt.Errorf("%s sent twice", name)
			}
			if !bytes.HasPrefix(b, pngMagic) {
				return nil, nil, fmt.Errorf("%s is not a PNG", name)
			}
			if len(pictures) == maxPictures {
				return nil, nil, fmt.Errorf("more than %d pictures", maxPictures)
			}
			pictures[name] = b
		default:
			return nil, nil, fmt.Errorf("unexpected part %q: only manifest and <name>.png", name)
		}
	}
	if manifest == nil {
		return nil, nil, errors.New("no part named manifest")
	}
	return manifest, pictures, nil
}

func (env Env) glazeFile(w http.ResponseWriter, r *http.Request, target, id, file string) {
	if !targets[target] || !isRunID(id) || (file != "shots.json" && !isPicture(file)) {
		fail(w, http.StatusNotFound, "no such file")
		return
	}
	st, err := env.Site()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the SITE bucket: %v", err)
		return
	}
	b, ct, ok, err := st.Get(runKey(target, id, file))
	switch {
	case err != nil:
		fail(w, http.StatusBadGateway, "reading %s: %v", file, err)
	case !ok:
		fail(w, http.StatusNotFound, "no such file")
	default:
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		// A run's files never change: its id is the hash of its manifest.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(b)
	}
}

// golden answers with a redirect to a presigned URL on R2's S3 endpoint
// rather than streaming the object: a 64 MiB chunk through Go-in-Wasm would
// cost far more CPU than a Worker request is allowed, and the presigned URL
// keeps Range (resume) working against R2 itself. The private bucket is not
// bound to the Worker at all; it is reached only with a read-only S3 token.
func (env Env) golden(w http.ResponseWriter, r *http.Request, key string) {
	if !env.authorized(w, r, varGoldenToken) {
		return
	}
	if !isGoldenKey(key) {
		fail(w, http.StatusNotFound, "not a golden-image key: golden/latest, golden/manifests/<sha256>.json or golden/chunks/<sha256>.zst")
		return
	}
	acct, bucket := env.Var(varAccountID), env.Var(varGoldenBucket)
	keyID, secret := env.Var(varGoldenKeyID), env.Var(varGoldenSecret)
	if acct == "" || bucket == "" || keyID == "" || secret == "" {
		fail(w, http.StatusServiceUnavailable, "refused: the golden bucket is not configured (%s, %s, %s, %s)", varAccountID, varGoldenBucket, varGoldenKeyID, varGoldenSecret)
		return
	}
	u := presign(presignInput{
		Method: r.Method, Host: acct + ".r2.cloudflarestorage.com", Path: "/" + bucket + "/" + key,
		Region: "auto", AccessKeyID: keyID, SecretAccessKey: secret,
		Time: env.Now(), Expires: goldenURLExpires,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", u)
	w.WriteHeader(http.StatusFound)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}

func fail(w http.ResponseWriter, code int, format string, a ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, a...)})
}
