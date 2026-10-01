# The Cloudflare Worker

`worker/` is one Cloudflare Worker, written in Go on
[syumai/workers-go](https://github.com/syumai/workers-go) and deployed at
`https://irgo-windows-vm.gedw99.workers.dev`. How it fits the rest of the tool
is in [Architecture](ARCHITECTURE.md); the golden image it serves is described
in [Using it](USING.md#the-private-r2-cache).

## The API

It serves three things. GitHub Pages (`pages.yml`) keeps publishing the site as
before until the owner switches.

- **The site.** `site/dist`, from `mise run site:build`, as Workers static
  assets. Cloudflare serves a matching file before the Worker runs, so pages
  cost no Worker CPU. Only `/api/*` reaches Go (`run_worker_first`).
- **Live glaze status.** CI's conformance job posts each runner's
  `shots.json` and its pictures to `POST /api/glaze-status/<mac|windows>` with
  a bearer token, and the Worker stores them in the R2 bucket bound as `SITE`.
  `GET /api/glaze-status` returns the newest run per target, and the Glaze
  status page shows it above the recorded one, so the page is current without
  a redeploy. On GitHub Pages that request finds nothing and the page is
  unchanged.
- **The golden image** (`worker/golden.go`), the private bucket bound as
  `GOLDEN`, and the only way `vm-golden-push` and `vm-golden-pull` reach it
  when `IRGO_GOLDEN_URL` is set ([the private R2 cache](USING.md#the-private-r2-cache)):

  | request | token | does |
  |---|---|---|
  | `GET /api/golden/<key>` | `GOLDEN_TOKEN` | the object, streamed; `Range` answers 206 |
  | `HEAD /api/golden/<key>` | `GOLDEN_TOKEN` | `X-Golden-Size`, `X-Golden-Sha256` |
  | `PUT /api/golden/<key>` | `GOLDEN_PUSH_TOKEN` | stored only if it hashes to `X-Golden-Sha256`; 201 |
  | `DELETE /api/golden/<key>` | `GOLDEN_PUSH_TOKEN` | 204, also when nothing was there |
  | `GET /api/golden-list/<manifests\|chunks>?cursor=` | `GOLDEN_PUSH_TOKEN` | a page of keys and sizes |

  `<key>` is exactly one the cache writes: `golden/latest`,
  `golden/manifests/<sha256>.json` or `golden/chunks/<sha256>.zst`. Anything
  else is 404 with any token.

All of the handler is plain Go behind two small interfaces (`worker/api.go`).
`go:check` builds and tests it for the host, where `workers.Serve` is an
ordinary HTTP server and R2 is a map. Only `platform_js.go` touches the
Workers runtime.

## Rules the code keeps

- **Refused by default.** A request without the right token gets 401, whatever
  the path, so a caller without one learns nothing about which keys exist. A
  secret that is not set refuses everything with 503: an unconfigured Worker
  does not fall open.
- **The golden bucket is reachable only through `/api/golden`.** A binding is
  not a public route; the bucket keeps no public access, so the
  `vm-golden-push` check that it is private (r2.dev URL off, no custom domain)
  still describes it. Each token does one job: `GOLDEN_TOKEN` reads,
  `GOLDEN_PUSH_TOKEN` writes, deletes and lists, and neither does the other's.
  A pulling machine cannot enumerate the bucket. Listing exists only for
  `vm-golden-push -delete`, which has to find every manifest and every chunk
  no manifest names (an interrupted push leaves some); without it that answer
  would be cannot tell. The licence terms that make the image private are in
  `.plans/2026-09-30_1700_vm-golden-image.md`.
- **Bytes never pass through Go.** A GET hands R2's body stream to the
  Response, and a PUT hands the request's stream to R2's `put`, through two
  hooks of workers-go (`GetRawJSBody` on a request body, `WriteRawJSBody` on
  the ResponseWriter, which `io.Copy` reaches through the body's `WriteTo`).
  workers-go's own `r2.Bucket.Put` reads the whole body into Wasm memory and
  its `Get` takes no range, so `platform_js.go` calls the binding through
  `syscall/js`. The Worker cannot hash a stream it never reads, so R2 does:
  the PUT passes the claimed SHA-256 as `put`'s `sha256` option and R2 refuses
  a mismatch (error 10037, answered 400). A manifest's claim must also be its
  name. Every PUT needs `Content-Length` (411 otherwise; R2 stores a stream
  only of known length) and at most 80 MiB (413): a chunk is 64 MiB
  compressed, plus the few KiB zstd adds to data it cannot shrink.
- **Uploads are checked before anything is stored.** The target must match the
  manifest. Every picture the manifest names must be sent, nothing else may
  be, and each must be a PNG with a plain name. A run older than the stored
  one is refused (409), so a re-run of an old workflow cannot roll the page
  back. A stored record that does not parse counts as "cannot tell", and that
  refuses too. The record is written last, and success is reported only once
  it reads back as written.
- **A run's files never change.** They are stored under the first 8 bytes of
  the manifest's SHA-256 and served `immutable`. Posting the same run again
  writes the same bytes.

## Go or TinyGo

Both build this handler and both pass the same requests under `wrangler dev`.
**TinyGo is used.** Measured 1 Oct 2026, Go 1.27.1, TinyGo 0.42.0,
workers-go v0.36.0, wrangler 4.143.0:

| | TinyGo | Go (`GOOS=js`, `-s -w`) |
|---|---|---|
| `app.wasm` | 1.49 MB | 7.29 MB |
| deploy bundle (`wrangler deploy --dry-run`) | 1,478 KiB, 547 KiB gzip | 7,142 KiB, 1,998 KiB gzip |
| `GET /api/health`, local, median of 30 | 5.1 ms | 4.3 ms |
| `GET /api/glaze-status`, median of 30 | 5.9 ms | 5.4 ms |
| `POST` a Windows run (7 PNGs), median of 10 | 33.1 ms | 18.8 ms |

Workers allows 64 MiB uncompressed with no compressed limit, and 1 s of
startup, so either fits. TinyGo's bundle is a fifth of the size, and that is
what the platform has to compile against the startup limit. The local times
are wall clock with simulated R2, not Cloudflare CPU time, and the only
request where Go is clearly faster is the POST, which CI sends twice per push.
workers-go recommends TinyGo for size. Going back to Go means changing two
lines in `mise-tasks/worker/wasm`: `-mode=go`, and `GOOS=js GOARCH=wasm go build`.

Measured live on 1 Oct 2026 (`wrangler tail`, the deployed TinyGo build), the
golden endpoints cost the same CPU whatever the size, because the bytes are the
runtime's: 8 to 22 ms per request, a 401 included, so that is the Wasm
handler's floor. A 64 MiB PUT took 18 ms of CPU over 7.7 s of wall time, its
GET 13 ms. That is over the Free plan's nominal 10 ms, and every request
still succeeded. Cloudflare's own body limit on this account: a 96 MiB PUT
reached the Worker (and its 413), a 101 MiB one was refused by Cloudflare
before it, as the 100 MB limit of the Free and Pro plans says. From this Mac,
a 64 MiB chunk went up in about 9 s and came down in about 2.7 s. An answer
to HEAD keeps no `Content-Length` on Workers, hence `X-Golden-Size`.

## Traps

TinyGo cost two traps, both found under `wrangler dev` and both invisible to
`go test` on the host:

| trap | symptom | what to do |
|---|---|---|
| `http.ServeMux` patterns such as `"GET /api/health"` under TinyGo 0.42 | never match: a mux holding only that pattern answered that path with 404 | route by hand (`Handler` in `api.go`) |
| `regexp.MustCompile` of `[0-9a-f]{64}` at package level under TinyGo | `fatal error: stack overflow` before `main`; every request then fails with "Go program has already exited" or hangs | plain loops (`isHex`, `isPicture`, `isGoldenKey`) |

Three more, about the tools rather than the code:

- **`mise run` installs every tool in `mise.toml`**, not only the ones a task
  needs. In a fresh data dir with only Go installed, `mise run go:lint`
  installed TinyGo (1.2 GB), binaryen, node and wrangler before it ran. Every workflow therefore sets
  `install_args` on mise-action and `MISE_TASK_RUN_AUTO_INSTALL: false`. With
  both, the same run installed go, golangci-lint and goreleaser and nothing
  else.
- **TinyGo's `-target wasm` runs `wasm-opt`**, and without binaryen it fails
  with "no usable wasm-opt found". binaryen is pinned in `mise.toml`.
- **`workers-assets-gen -o build` empties `build/`** first, and **`wrangler
  dev` does not see a rebuilt `site/dist`**, because `site:build` replaces the
  directory. Restart `wrangler dev` after `site:build`. Its simulated R2
  survives in `worker/.wrangler/state`.

## Run it locally

No Cloudflare account is needed. The tokens below are local stand-ins:

```
mise install
mise run site:build
cd worker
wrangler dev --local --var GLAZE_STATUS_TOKEN:local-glaze --var GOLDEN_TOKEN:local-gold \
  --var GOLDEN_PUSH_TOKEN:local-push
```

`wrangler dev` runs `mise run worker:wasm` itself. Then, from the repository
root:

```
curl -s localhost:8787/api/health                                    # {"ok":true}
curl -s -w ' %{http_code}\n' -F manifest=@docs/screens/conformance/windows/shots.json \
  localhost:8787/api/glaze-status/windows                            # 401
d=docs/screens/conformance/windows; f=(-F "manifest=@$d/shots.json")
for p in $d/*.png; do f+=(-F "$(basename $p)=@$p"); done
curl -s -H 'Authorization: Bearer local-glaze' "${f[@]}" localhost:8787/api/glaze-status/windows   # 201
curl -s localhost:8787/api/glaze-status                              # the run, as JSON
curl -s -w ' %{http_code}\n' localhost:8787/api/golden/golden/latest # 401
curl -s -w ' %{http_code}\n' -H 'Authorization: Bearer local-gold' \
  localhost:8787/api/golden/golden/latest                            # 404: nothing pushed yet
```

And the golden cache end to end, from the repository root, against the
simulated bucket (no Cloudflare account, no API token, so the public-access
check says it was not done):

```
export IRGO_GOLDEN_URL=http://localhost:8787 IRGO_GOLDEN_TOKEN=local-gold IRGO_GOLDEN_PUSH_TOKEN=local-push
irgo-winvm vm-golden-push -bundle <a test bundle> && irgo-winvm vm-golden-pull -dir /tmp/pull
irgo-winvm vm-golden-push -delete -force
```

Then open `http://localhost:8787/glaze-status`: the live box sits above the
recorded run. Measured 1 Oct 2026, every one of these answered as commented.
The posted picture came back byte for byte identical to the committed one, an
older run was refused with 409, and `/api/golden/` with a valid token
(a listing) answered 404. The golden endpoints were measured the same day
with 1 and 65 MiB objects: a wrong `X-Golden-Sha256` answered 400 and stored
nothing, the whole object and a `Range` half came back byte for byte, and a
synthetic 300 MB sparse bundle went up, came back identical (`diff -r`), and
was deleted.

## Deploying it

Done on 1 Oct 2026 by these steps. In order:

1. **Authenticate wrangler**, with `wrangler login` or `CLOUDFLARE_API_TOKEN`.
   That token needs Workers Scripts Edit and Workers R2 Storage Edit on the
   account.
2. **Create the site bucket**: `wrangler r2 bucket create irgo-windows-vm-site`.
   Leave public access off (no r2.dev URL, no custom domain). The Worker is
   the only reader.
3. **The golden bucket**, `irgo-golden`, must exist and stay private (no
   r2.dev URL, no custom domain). `wrangler.toml` binds it as `GOLDEN`; if it
   is renamed, change `bucket_name` there.
4. **Build the site, then deploy**: `mise run site:build`, then
   `cd worker && wrangler deploy`. The deploy runs `mise run worker:wasm` and
   uploads `site/dist` as the Worker's assets. Its output names the URL,
   `https://irgo-windows-vm.<subdomain>.workers.dev`, and `startup_time_ms`.
5. **Set the three secrets** from `worker/`, one `wrangler secret put <NAME>`
   each, each a different `openssl rand -hex 32`. Until one is set, its
   endpoints answer 503.

   | secret | for |
   |---|---|
   | `GLAZE_STATUS_TOKEN` | CI posting glaze runs |
   | `GOLDEN_TOKEN` | reading the golden image (`IRGO_GOLDEN_TOKEN`) |
   | `GOLDEN_PUSH_TOKEN` | writing and deleting it (`IRGO_GOLDEN_PUSH_TOKEN`) |

   Keep the golden pair in `.env.r2` too (see
   [Setting up the bucket](USING.md#setting-up-the-bucket)).

6. **Point CI at it**:
   `gh variable set GLAZE_STATUS_URL --body https://irgo-windows-vm.<subdomain>.workers.dev`
   and `gh secret set GLAZE_STATUS_TOKEN` with the same value as step 5. The
   conformance job's last step then posts on every push to main. Until both
   are set, that step prints that it is not posting and succeeds.
7. **Check the deployment** with the curls above, using the real URL and
   tokens. A request with no token must get 401 on `/api/glaze-status/*`
   (POST), `/api/golden/*` (every method) and `/api/golden-list/*`, and the
   read token must get 401 on a PUT.

Switching the public site from GitHub Pages to the Worker (a custom domain on
the Worker, and retiring `pages.yml`) is a separate decision and is not part
of these steps.
