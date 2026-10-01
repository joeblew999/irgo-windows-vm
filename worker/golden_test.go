package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

var goldenVars = map[string]string{varGoldenToken: "gold", varGoldenPushToken: "push"}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// put sends body to key, claiming sum (hexSum(body) when sum is "").
func put(h http.Handler, key, token string, body []byte, sum string) *httptest.ResponseRecorder {
	if sum == "" {
		sum = hexSum(body)
	}
	r := httptest.NewRequest("PUT", "/api/golden/"+key, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(hdrSHA256, sum)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func get(h http.Handler, method, path, token, rng string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if rng != "" {
		r.Header.Set("Range", rng)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Every route refuses without its own token, the read token cannot write,
// and nothing but the cache's own keys is reachable with any token.
//
// Negative control (by hand, 1 Oct 2026): authorizing PUT and DELETE with
// varGoldenToken instead of varGoldenPushToken fails the three "read token
// cannot write" cases; restored.
func TestGoldenRefusals(t *testing.T) {
	env, _, gb := testEnvGolden(goldenVars)
	h := Handler(env)
	id := strings.Repeat("ab", 32)
	chunk := "golden/chunks/" + id + ".zst"
	for _, c := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/api/golden/golden/latest", "", 401},
		{"GET", "/api/golden/golden/latest", "wrong", 401},
		{"GET", "/api/golden/golden/latest", "push", 401}, // each token does one job
		{"HEAD", "/api/golden/golden/latest", "", 401},
		{"GET", "/api/golden/", "", 401}, // no hint without a token
		{"GET", "/api/golden/", "gold", 404},
		{"GET", "/api/golden/golden/", "gold", 404},
		{"GET", "/api/golden/other/file", "gold", 404},
		{"GET", "/api/golden/golden/chunks/" + id + ".zst%2F..%2F..%2Fx", "gold", 404},
		{"GET", "/api/golden/golden/chunks/short.zst", "gold", 404},
		{"GET", "/api/golden/" + chunk, "gold", 404}, // not there
		{"PUT", "/api/golden/" + chunk, "", 401},
		{"PUT", "/api/golden/" + chunk, "gold", 401}, // the read token cannot write
		{"DELETE", "/api/golden/" + chunk, "gold", 401},
		{"GET", "/api/golden-list/chunks", "gold", 401}, // nor list
		{"GET", "/api/golden-list/chunks", "", 401},
		{"GET", "/api/golden-list/other", "push", 404},
		{"POST", "/api/golden/" + chunk, "push", 405},
	} {
		if w := get(h, c.method, c.path, c.token, ""); w.Code != c.want {
			t.Errorf("%s %s token=%q: %d %s, want %d", c.method, c.path, c.token, w.Code, w.Body, c.want)
		}
	}
	if len(gb.m) != 0 {
		t.Errorf("a refused request stored something: %v", gb.m)
	}

	// An unconfigured Worker refuses rather than falls open.
	env2, _, _ := testEnvGolden(map[string]string{varGoldenToken: "gold"})
	if w := put(Handler(env2), chunk, "push", []byte("x"), ""); w.Code != 503 {
		t.Errorf("no push token set: %d, want 503", w.Code)
	}
}

// A chunk goes in, comes back whole and in ranges, says its size and hash on
// HEAD, and goes away; deleting it again is success.
//
// Negative control (by hand, 1 Oct 2026): computing Content-Range's end as
// Offset+Length (one past) fails the range cases; restored.
func TestGoldenRoundTrip(t *testing.T) {
	env, _, _ := testEnvGolden(goldenVars)
	h := Handler(env)
	body := bytes.Repeat([]byte("0123456789"), 1000)
	key := "golden/chunks/" + strings.Repeat("0f", 32) + ".zst"
	path := "/api/golden/" + key

	if w := put(h, key, "push", body, ""); w.Code != 201 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	w := get(h, "HEAD", path, "gold", "")
	if w.Code != 200 || w.Header().Get(hdrSize) != "10000" || w.Header().Get(hdrSHA256) != hexSum(body) {
		t.Errorf("head: %d %v", w.Code, w.Header())
	}
	w = get(h, "GET", path, "gold", "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("Content-Length") != "10000" {
		t.Errorf("get: %d, %d bytes", w.Code, w.Body.Len())
	}
	for _, c := range []struct {
		rng, cr string
		from, n int
	}{
		{"bytes=4000-", "bytes 4000-9999/10000", 4000, 6000},
		{"bytes=10-19", "bytes 10-19/10000", 10, 10},
		{"bytes=9990-20000", "bytes 9990-9999/10000", 9990, 10},
		{"bytes=-5", "bytes 9995-9999/10000", 9995, 5},
	} {
		w := get(h, "GET", path, "gold", c.rng)
		if w.Code != 206 || w.Header().Get("Content-Range") != c.cr ||
			!bytes.Equal(w.Body.Bytes(), body[c.from:c.from+c.n]) || w.Header().Get("Content-Length") != strconv.Itoa(c.n) {
			t.Errorf("%s: %d %q, %d bytes", c.rng, w.Code, w.Header().Get("Content-Range"), w.Body.Len())
		}
	}
	if w := get(h, "GET", path, "gold", "bytes=10000-"); w.Code != 416 || w.Header().Get("Content-Range") != "bytes */10000" {
		t.Errorf("a range past the end: %d %v", w.Code, w.Header())
	}
	if w := get(h, "GET", path, "gold", "bytes=1-2,5-6"); w.Code != 200 || w.Body.Len() != 10000 {
		t.Errorf("a multi-range is ignored and the whole object sent: %d", w.Code)
	}

	for range 2 {
		if w := get(h, "DELETE", path, "push", ""); w.Code != 204 {
			t.Errorf("delete: %d %s", w.Code, w.Body)
		}
	}
	if w := get(h, "HEAD", path, "gold", ""); w.Code != 404 {
		t.Errorf("after delete, head: %d", w.Code)
	}
}

// A body that does not hash to its claim is not stored; a manifest must be
// named by its hash; a body needs a length and a claim.
//
// Negative control (by hand, 1 Oct 2026): removing the manifest-name compare
// in goldenPut fails "manifest under another name"; restored.
func TestGoldenPutRefusals(t *testing.T) {
	env, _, gb := testEnvGolden(goldenVars)
	h := Handler(env)
	body := []byte(`{"format":1}`)
	chunk := "golden/chunks/" + strings.Repeat("0f", 32) + ".zst"
	if w := put(h, chunk, "push", body, strings.Repeat("0", 64)); w.Code != 400 || !strings.Contains(w.Body.String(), "does not hash") {
		t.Errorf("a wrong hash: %d %s", w.Code, w.Body)
	}
	if w := put(h, chunk, "push", body, "nothex"); w.Code != 400 {
		t.Errorf("no usable hash: %d %s", w.Code, w.Body)
	}
	other := "golden/manifests/" + strings.Repeat("1", 64) + ".json"
	if w := put(h, other, "push", body, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "named by its SHA-256") {
		t.Errorf("a manifest under another name: %d %s", w.Code, w.Body)
	}
	if len(gb.m) != 0 {
		t.Fatalf("a refused put stored something: %v", gb.m)
	}

	r := httptest.NewRequest("PUT", "/api/golden/"+chunk, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer push")
	r.Header.Set(hdrSHA256, hexSum(body))
	r.Header.Del("Content-Length")
	r.ContentLength = -1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 411 {
		t.Errorf("no Content-Length: %d", w.Code)
	}

	r = httptest.NewRequest("PUT", "/api/golden/"+chunk, bytes.NewReader(nil))
	r.Header.Set("Authorization", "Bearer push")
	r.Header.Set(hdrSHA256, hexSum(nil))
	r.Header.Set("Content-Length", strconv.Itoa(maxGoldenPut+1))
	r.ContentLength = maxGoldenPut + 1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Errorf("too large: %d", w.Code)
	}

	good := "golden/manifests/" + hexSum(body) + ".json"
	if w := put(h, good, "push", body, ""); w.Code != 201 {
		t.Errorf("a manifest under its own name: %d %s", w.Code, w.Body)
	}
}

// golden-list pages through manifests and chunks and names nothing else.
func TestGoldenList(t *testing.T) {
	env, _, gb := testEnvGolden(goldenVars)
	gb.pageSize = 2
	h := Handler(env)
	want := map[string]bool{}
	for i := range 5 {
		b := []byte{byte(i)}
		k := "golden/chunks/" + hexSum(b) + ".zst"
		if w := put(h, k, "push", b, ""); w.Code != 201 {
			t.Fatal(w.Body)
		}
		want[k] = true
	}
	gb.m["golden/chunks/stray"] = memBlob{body: []byte("x")}
	if w := put(h, "golden/latest", "push", []byte("x\n"), ""); w.Code != 201 {
		t.Fatal(w.Body)
	}
	got := map[string]bool{}
	cursor, pages := "", 0
	for {
		w := get(h, "GET", "/api/golden-list/chunks?cursor="+cursor, "push", "")
		var page struct {
			Objects []BlobInfo
			Cursor  string
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		pages++
		for _, o := range page.Objects {
			if o.Size != 1 {
				t.Errorf("%s: size %d", o.Key, o.Size)
			}
			got[o.Key] = true
		}
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
	if len(got) != len(want) || pages != 3 {
		t.Errorf("listed %d keys in %d pages, want %d in 3: %v", len(got), pages, len(want), got)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("%s not listed", k)
		}
	}
}
