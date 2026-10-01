# The Worker API as a single source, like the CLI/MCP command table

Status: proposed → agent FF · 2026-10-01

## Symptom
CLI and MCP are generated from one table (internal/command). The Worker is not: its routes, token
scopes, request/response shapes and status codes are written by hand in worker/ and again by hand
in every client (glaze-status posting in CI, vm_golden_worker.go, the ledger client, remote jobs,
capacity snapshots). Each new endpoint (agents AA, CC, DD are adding some now) is a second and third
copy that can drift.

## Change
- A dependency-free Go module (e.g. `wire/`, importable by the TinyGo Worker and the root module)
  holding: the route table (method, path pattern, token scope, request/response Go types, success
  and error status codes, size limits, description), shared constants (key patterns, headers),
  and error codes.
- worker/: routing built from the table (one handler per route, registered by name); a test that
  the Worker serves exactly the table's routes and enforces each route's scope.
- Clients: one typed client package generated or written against the table (golden transfer,
  glaze status, ledger, remote jobs, capacity), used by the CLI, MCP tools and CI scripts; a test
  that no client builds a URL outside the table.
- Docs: a generated API reference page on the site (like the command reference), plus an OpenAPI
  document served by the Worker.
- MCP: remote tools derived from the same table where they map to Worker routes.

## Order
FF builds the module from the endpoints on main (health, glaze-status, golden), lands it, then the
main session tells AA, CC, DD to define their routes in it before they merge.

## Verify
go:check; route-table tests both directions with negative controls; deployed Worker answers
the OpenAPI document; site page generated.
