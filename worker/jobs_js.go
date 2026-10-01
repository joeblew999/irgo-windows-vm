//go:build js && wasm

package main

import (
	"errors"
	"syscall/js"
	"time"

	"github.com/syumai/workers-go/cloudflare"
)

// jobsBucket is the R2 binding JOBS (wrangler.toml): the streaming half is
// the golden bucket's jsBlobs, the CAS half is below.
func jobsBucket() (JobBucket, error) {
	b := cloudflare.GetBinding("JOBS")
	if b.IsUndefined() {
		return nil, errors.New("JOBS is not bound (wrangler.toml, r2_buckets)")
	}
	return jsJobs{jsBlobs{b}}, nil
}

type jsJobs struct{ jsBlobs }

func (s jsJobs) Load(key string) ([]byte, string, bool, error) {
	o, err := await(s.b.Call("get", key))
	if err != nil || o.IsNull() {
		return nil, "", false, err
	}
	buf, err := await(o.Call("arrayBuffer"))
	if err != nil {
		return nil, "", false, err
	}
	u8 := js.Global().Get("Uint8Array").New(buf)
	body := make([]byte, u8.Length())
	js.CopyBytesToGo(body, u8)
	return body, o.Get("etag").String(), true, nil
}

// Swap is R2's conditional put: onlyIf etagMatches to replace, and
// etagDoesNotMatch "*" to create only when absent. A put whose condition
// fails stores nothing and resolves to null.
func (s jsJobs) Swap(key string, body []byte, etag string) (bool, error) {
	u8 := js.Global().Get("Uint8Array").New(len(body))
	js.CopyBytesToJS(u8, body)
	cond := js.Global().Get("Object").New()
	if etag == "" {
		cond.Set("etagDoesNotMatch", "*")
	} else {
		cond.Set("etagMatches", etag)
	}
	opts := js.Global().Get("Object").New()
	opts.Set("onlyIf", cond)
	meta := js.Global().Get("Object").New()
	meta.Set("contentType", "application/json")
	opts.Set("httpMetadata", meta)
	o, err := await(s.b.Call("put", key, u8, opts))
	if err != nil {
		return false, err
	}
	return !o.IsNull(), nil
}

func init() { pause = jsPause }

// jsPause waits on the runtime's setTimeout. time.Sleep under TinyGo on
// Workers never returned: every request that backed off in mutate hung
// until the client gave up (measured live, 1 Oct 2026, 12 of 20 parallel
// submits), while host tests and single requests passed.
func jsPause(d time.Duration) {
	var cb js.Func
	p := js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve := args[0]
		cb = js.FuncOf(func(js.Value, []js.Value) any { resolve.Invoke(); return nil })
		js.Global().Call("setTimeout", cb, d.Milliseconds())
		return nil
	}))
	_, _ = await(p)
	cb.Release()
}

// GetRawJSBody hands a body already in Go memory to R2 as a Uint8Array.
func (b inBody) GetRawJSBody() js.Value {
	u8 := js.Global().Get("Uint8Array").New(len(b.b))
	js.CopyBytesToJS(u8, b.b)
	return u8
}

// WriteRawJSBody reads a stream into the recorder: the MCP tools' answers,
// all small, read through the same handler as an HTTP caller's.
func (r *recorder) WriteRawJSBody(body js.Value) {
	buf, err := await(js.Global().Get("Response").New(body).Call("arrayBuffer"))
	if err != nil {
		r.err = err
		return
	}
	u8 := js.Global().Get("Uint8Array").New(buf)
	b := make([]byte, u8.Length())
	js.CopyBytesToGo(b, u8)
	r.buf.Write(b)
}
