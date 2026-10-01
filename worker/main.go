// Command worker is the Cloudflare Worker for irgo-windows-vm: the site as
// static assets, the live glaze status, and the private golden image's only
// way in and out. Built for Wasm (mise run worker:wasm) it runs on Workers;
// built for the host, as go:check does, workers.Serve starts a plain HTTP
// server on :9900 (or $PORT) with in-memory buckets and the process
// environment, to debug the handler without wrangler.
package main

import (
	"time"

	"github.com/syumai/workers-go"
)

func main() {
	workers.Serve(Handler(Env{Var: getenv, Site: siteBucket, Golden: goldenBucket, Jobs: jobsBucket, Now: time.Now}))
}
