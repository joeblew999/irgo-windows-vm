# The Cloudflare Worker

`worker/` is one Cloudflare Worker, written in Go on
[syumai/workers-go](https://github.com/syumai/workers-go) and deployed at
`https://irgo-windows-vm.gedw99.workers.dev`. How it fits the rest of the tool
is in [Architecture](ARCHITECTURE.md); the golden image it serves is described
in [Using it](USING.md#the-private-r2-cache).

## The API

It serves five things. GitHub Pages (`pages.yml`) keeps publishing the site as
before until the owner switches.

- **The site.** `site/dist`, from `mise run site:build`, as Workers static
  assets. Cloudflare serves a matching file before the Worker runs, so pages
  cost no Worker CPU. Only `/api/*` reaches Go (`run_worker_first`).
- **Live glaze status.** CI's conformance job posts each runner's
  `shots.json` and its pictures with `glaze-check -post <mac|windows>`, and the Worker stores them in the R2 bucket bound as `SITE`.
  `GET /api/glaze-status` returns the newest run per target, and the Glaze
  status page shows it above the recorded one, so the page is current without
  a redeploy. On GitHub Pages that request finds nothing and the page is
  unchanged.
- **The golden image** (`worker/golden.go`), the private bucket bound as
  `GOLDEN`, and the only way `vm-golden-push` and `vm-golden-pull` reach it
  when `IRGO_GOLDEN_URL` is set ([the private R2 cache](USING.md#the-private-r2-cache)).
  A key is exactly one the cache writes: `golden/latest`,
  `golden/manifests/<sha256>.json` or `golden/chunks/<sha256>.zst`. Anything
  else is 404 with any token.
- **The remote job queue** (`worker/jobs.go`), in the private bucket bound
  as `JOBS`: see [The remote job queue](#the-remote-job-queue).
- **The ledger** (`worker/ledger.go`), who used which VM where, in the D1
  database bound as `LEDGER`: see [The ledger](#the-ledger).

Every route, its token, its body limit and its answers are on the
[Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html) page, generated from the route table (see
[The route table](#the-route-table)); no document lists them by hand. All of
the handler is plain Go behind small interfaces (`worker/api.go`). `go:check`
builds and tests it for the host, where `workers.Serve` is an ordinary HTTP
server, R2 is a map and D1 is SQLite. Only `platform_js.go` touches the
Workers runtime.

## The route table

The Worker's API is declared once, in package `wire` (`wire/routes.go`), the
way the commands are declared once in `internal/command`. Everything that
serves or calls it is derived from that table:

| reads the table | how |
|---|---|
| `worker/` | `Handler` matches each request with `wire.Match` and calls the handler registered under the route's name in `handlers` (`worker/api.go`); before it, the dispatcher checks the route's token scope (401, or 503 when its secret is unset; a `Page` route also takes the token as a Basic password), `NeedLength` (411) and `MaxBody` (413). It panics at start if `handlers` and the table disagree |
| `internal/workerclient` | the one client. A call names a route; the URL is `wire.Route.URL`, the token is the one for the route's scope, success is the route's status, and anything else is a `*workerclient.Error` with the Worker's `wire.Code`. `vm-golden-push`/`-pull` (`internal/utmvm/vm_golden_worker.go`), the ledger (`internal/ledger`, which keeps its spool and sends each batch once through it) and CI's glaze post (`glaze-check -post`) all go through it |
| `/api/openapi.json` | the OpenAPI 3.1 document, from `wire/openapi` (reflection over the request and response types, so not in the TinyGo build), written to `worker/openapi.json` by `mise run worker:wasm` and embedded |
| the site | the [Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html) page (`site/api.go`), and the path the Glaze status page fetches |
| MCP | a tool whose command calls a route (`Route.Commands`) names it in its description |

`wire` is a package of the root module, not a module of its own: a replace
directive in the root `go.mod` would break `go install …/cmd/irgo-winvm@latest`,
which refuses a module that has one. `worker/` and `site/` require the root
module with `replace … => ../`; module graph pruning keeps their `go.sum`
free of the tool's dependencies, and only `wire`'s standard-library imports
are built. `wire`'s `TestStandardLibraryOnly` keeps it that way (no
third-party module, no `regexp`), and `mise run worker:wasm` is what proves
TinyGo takes it.

The tests, each with its negative control in its comment:

- `wire`: `TestTable` (unique names and method+path, every `{param}`
  described, every scope and code declared), `TestMatch`, `TestURLEscapes`.
- `worker`: `TestHandlersAreTheTable`; `TestEveryRouteEnforcesItsScope`, which
  replaces every handler with a spy and checks each route is reached with its
  own token and refused (handler never run) with none, a wrong one, each
  other scope's, and with its secret unset; `TestBodyLimitsFromTheTable`;
  `TestOpenAPIIsCurrent`. The route-specific refusal tests
  (`TestGoldenRefusals`, `TestGlazePostRefusals`, `TestLedgerRefusals`) pin
  the scopes literally, so a wrong scope in the table itself fails too.
- `internal/workerclient`: `TestEachCallCarriesItsScopesToken`,
  `TestErrorsCarryTheCode`, and `TestNoURLsOutsideTheClient`. That last one is
  a grep, because the compiler cannot tell a URL from any other string: no
  non-test file outside `wire/`, `internal/workerclient/` and `worker/` may
  contain a route's literal path prefix (taken from the table, so a new route
  is covered), comments excepted.
- `internal/mcpserver`: `TestWorkerRoutesNameRealCommands`.

### Adding a route

Three edits and a regenerate:

1. **One entry in `wire.Routes`** (`wire/routes.go`): a `Route*` name
   constant, method, path (`{name}` for one segment, a final `{name...}` for
   the rest), `Scope`, summary, `Params`, request and response types (the zero
   value of a Go type in `wire`, or a media type for a body that is not
   JSON), `Success`, the handler's own `Errors`, `MaxBody` for a body, and
   `Commands` if an `irgo-winvm` command calls it. A new token is a new
   `Scope` and a row in `wire.Scopes` (the Worker secret and the client's
   variable). Shared constants, key patterns, limits and the request and
   response types go in `wire` too, so the Worker and the client share them.
2. **One handler** in `worker/`, `func(env Env, w, r, params []string)`,
   registered under the route's name in `handlers` (`worker/api.go`). It does
   not check the token or the body's size: the dispatcher did. It answers an
   error with `fail(w, wire.Code…, …)`.
3. **One client method** in `internal/workerclient`, built on `c.Do(ctx,
   wire.RouteX, params, query, body, header)`, decoding into the route's
   `wire` type.
4. `mise run worker:wasm` regenerates `worker/openapi.json`; then
   `mise run go:check`. The site page, the OpenAPI document and the MCP
   descriptions follow on their own.

The [traps](#traps) below apply to anything a handler or `wire` does: no
`ServeMux` patterns, no package-level regexps.

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

TinyGo cost three traps, all invisible to `go test` on the host:

| trap | symptom | what to do |
|---|---|---|
| `http.ServeMux` patterns such as `"GET /api/health"` under TinyGo 0.42 | never match: a mux holding only that pattern answered that path with 404 | route by hand (`wire.Match`) |
| `regexp.MustCompile` of `[0-9a-f]{64}` at package level under TinyGo | `fatal error: stack overflow` before `main`; every request then fails with "Go program has already exited" or hangs | plain loops (`wire.IsHex`, `wire.IsPicture`, `wire.IsGoldenKey`) |
| `time.Sleep` in a request under TinyGo | never returns: twelve of twenty parallel submits that backed off in `mutate` hung until the client gave up, while the host tests and a single request passed (measured live, 1 Oct 2026) | wait on the runtime's `setTimeout` (`jsPause` in `jobs_js.go`) |

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
5. **Set the secrets** from `worker/`, one `wrangler secret put <NAME>`
   each, each a different `openssl rand -hex 32`. Until one is set, its
   endpoints answer 503.

   | secret | for |
   |---|---|
   | `GLAZE_STATUS_TOKEN` | CI posting glaze runs |
   | `GOLDEN_TOKEN` | reading the golden image (`IRGO_GOLDEN_TOKEN`) |
   | `GOLDEN_PUSH_TOKEN` | writing and deleting it (`IRGO_GOLDEN_PUSH_TOKEN`) |
   | `LEDGER_TOKEN` | the tool posting to [the ledger](#the-ledger) (`IRGO_LEDGER_TOKEN`) |
   | `LEDGER_READ_TOKEN` | reading the ledger: its page and JSON (`IRGO_LEDGER_READ_TOKEN`) |
   | `JOBS_TOKENS` | [the job queue](#the-remote-job-queue)'s callers, `name=token,…` (`IRGO_REMOTE_TOKEN`) |
   | `JOBS_ADMIN_TOKEN` | listing every job (`IRGO_REMOTE_ADMIN_TOKEN`) |
   | `JOBS_RUNNER_TOKEN` | the Mac running `serve` (`IRGO_REMOTE_RUNNER_TOKEN`) |

   Keep the golden pair in `.env.r2` too (see
   [Setting up the bucket](USING.md#setting-up-the-bucket)).

6. **Point CI at it**:
   `gh variable set GLAZE_STATUS_URL --body https://irgo-windows-vm.<subdomain>.workers.dev`
   and `gh secret set GLAZE_STATUS_TOKEN` with the same value as step 5. The
   conformance job's last step then posts on every push to main. Until both
   are set, that step prints that it is not posting and succeeds.
7. **Check the deployment** with the curls above, using the real URL and
   tokens. Every route with a token must answer 401 without one (the
   [Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html) page lists them), the read token must get 401 on a PUT, and
   `/api/openapi.json` must be the committed `worker/openapi.json`.

Switching the public site from GitHub Pages to the Worker (a custom domain on
the Worker, and retiring `pages.yml`) is a separate decision and is not part
of these steps.

## The remote job queue

`worker/jobs.go`, routes in `wire/jobs.go`: how a developer, an agent or a
workflow on any OS hands a Windows binary to a Mac running `irgo-winvm serve`
([how to use it](FOR-AGENTS.md#from-another-machine-linux-windows-github),
[what the Mac does](ARCHITECTURE.md#remote-jobs)). The Mac accepts no
connection, so the queue lives here and the Mac polls for work. Every route,
its body and its answers are on the [Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html) page; in short:

| scope (secret) | routes |
|---|---|
| `jobs` (`JOBS_TOKENS`) | `POST /api/jobs` a spec; `PUT …/{id}/input` the binary, at most 95 MiB, stored only if R2 finds it hashes to the spec; `GET …/{id}`, `…/{id}/log?offset=N`, `…/{id}/files/{name}`; `POST …/{id}/cancel`; `POST /api/mcp` |
| `jobs-admin` (`JOBS_ADMIN_TOKEN`) | `GET /api/jobs`, every job in the index |
| `jobs-runner` (`JOBS_RUNNER_TOKEN`) | `POST /api/runner/claim`, and the running job's `heartbeat`, `input`, `log`, `files/{name}`, `finish`; 409 once it is not running, which stops the Mac |

**One object, changed by compare-and-swap.** Every live job is in
`jobs/index.json` in the private bucket bound as `JOBS` (`irgo-jobs`), and
every change is a conditional put: `onlyIf: {etagMatches}` to replace,
`etagDoesNotMatch: "*"` to create. R2 stores nothing and answers null when
another writer got there first, and `mutate` reads again and reapplies, after a
jittered pause, up to 16 times (then `busy`, 503, which the client retries).
Measured live on 1 Oct 2026: 15 submits at once, all 15 in the index and none
refused; without the pause, 12 at once lost one to ten immediate retries. Each
job's binary, log and results are objects beside it under `jobs/<id>/`,
streamed as the golden image is. Not a Durable Object, the natural queue:
workers-go can call one but not define one, so its class would be JavaScript
outside `go test` (the ledger chose D1 for the same reason). Not a WebSocket:
the Mac polls every 3 s, one class B read, nothing when idle.

**Time is part of every read** (`sweep`): no binary after 30 min, or queued
2 h with no Mac, is `expired`; a lease that runs out is `lost`; running past
its timeout plus 20 min is `timed-out`. Each ends at its deadline, not when it
was noticed, so a read's answer and the next write's agree. A day after it
ends a job leaves the index and its record is written beside its files; a
lifecycle rule deletes `jobs/` after 7 days.

**Tokens and limits.** `JOBS_TOKENS` is `name=token,name=token`, one per
caller (`wire.Scope.Named`), so one can be revoked alone and every job records
its caller's name; a bare token is the caller `caller`. A caller sees only its
own jobs: another caller's id answers 404, as one that does not exist does.
Each token does one job, as every other scope's: the admin token lists and
reads nothing else, the runner's takes and reports jobs, and either is 401 on
the other's routes. Limits: the binary 95 MiB, a result file 32 MiB and 64 of
them, the log 2 MiB, 64 arguments of 4 KiB, a timeout of at most 1 h (default
10 min), 20 live jobs per caller (`too-many`, 429), 500 in the index.

**The MCP server** (`POST /api/mcp`) is the Streamable HTTP transport in its
stateless form: one JSON-RPC request per POST, one JSON answer, no session, no
event stream. Each tool is one request to the routes above, made in-process
through `Handler` with the caller's own token, so a tool can do nothing the
token cannot do by curl, and there is one implementation of each operation.

**Set up on 1 Oct 2026**: `wrangler r2 bucket create irgo-jobs` (no public
access), `wrangler r2 bucket lifecycle add irgo-jobs expire-jobs jobs/
--expire-days 7`, the three secrets, a deploy. `.env.r2` on the owner's Mac
holds `IRGO_REMOTE_URL`, `IRGO_REMOTE_TOKEN` (the owner's caller token),
`IRGO_REMOTE_LINUX_TOKEN` and `IRGO_REMOTE_CI_TOKEN` (two more callers),
`IRGO_REMOTE_ADMIN_TOKEN` and `IRGO_REMOTE_RUNNER_TOKEN`.

## The ledger

Who used which VM, on which machine, doing what, and what was started and never
finished, kept where any machine can read it. The events come from every
command the tool runs; what they carry, and why sending them can never fail a
command, is in [Architecture](ARCHITECTURE.md#the-ledger-client).

It stores events in the D1 database `irgo-ledger`, schema in
`worker/migrations/`. D1 rather than a Durable Object: workers-go opens a D1
database as `database/sql` but cannot define a Durable Object class, and the
questions asked are queries. On the host and in the tests the same migration
runs on SQLite (modernc.org/sqlite, in the worker module only), so the tests
run the real SQL. The d1 driver under TinyGo was measured with `wrangler dev`
before it was used: inserts, nulls, `RowsAffected` for duplicates.

Its routes (`ledger-post`, `ledger-events`, `ledger-vms`, `ledger-page`,
and `ledger-bare`, which redirects `/api/ledger` to the page) and their
filters are on the [Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html) page. Posting needs `LEDGER_TOKEN`; reading
needs `LEDGER_READ_TOKEN`, which the page also takes as an HTTP Basic password
(`Route.Page`).

- **Stale** is open work never closed: a start with no end, or a lease with
  no release, older than `stale` (3 h by default), or a lease past its
  `Expires`. The view reads the last 14 days, at most 5,000 events, and says
  when it was truncated. A start and an end in the same millisecond count as
  closed whichever id sorts first; live, a refused `app-create` once showed as
  in use.
- **Never public.** It names hosts, users and repositories. The page is
  rendered by the Worker, not a page of the site, because static assets are
  served to anyone before the Worker runs. A browser cannot send a bearer
  token on a navigation, so the page also takes the read token as an HTTP
  Basic password and asks for one. The two tokens each do one job.
- **Limits** are D1's on the Free plan: 50 queries per invocation (hence 25
  events, one statement each) and a daily row budget, which the indexes on
  `ts`, `op`, `(machine, vm, ts)` and `(owner, ts)` keep small.

Set up on 1 Oct 2026: `wrangler d1 create irgo-ledger` (its id is in
`wrangler.toml`), `wrangler d1 migrations apply irgo-ledger --remote`, the two
secrets, then a deploy. Locally: `wrangler d1 migrations apply irgo-ledger
--local`, then `wrangler dev --local --var LEDGER_TOKEN:local-lw --var
LEDGER_READ_TOKEN:local-lr`. A schema change is a new numbered file in
`worker/migrations/`, applied the same way.
