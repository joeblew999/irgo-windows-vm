-- The ledger: which agent used which VM, on which machine, doing what.
-- docs/DEVELOPMENT.md, "The ledger". Applied to D1 with
-- `wrangler d1 migrations apply irgo-ledger --remote`, and to the in-memory
-- SQLite of the host build and the tests (worker/platform_other.go), so both
-- run exactly this file.
--
-- One row per event, written once: the id is the client's, so a batch sent
-- twice (a flush that timed out after the Worker had stored it) adds nothing.
-- Times are Unix milliseconds: ts is the machine's clock, received the Worker's.
CREATE TABLE IF NOT EXISTS events (
  id          TEXT PRIMARY KEY,
  ts          INTEGER NOT NULL,
  received    INTEGER NOT NULL,
  type        TEXT NOT NULL,
  op          TEXT NOT NULL DEFAULT '',
  machine     TEXT NOT NULL,
  host        TEXT NOT NULL DEFAULT '',
  owner       TEXT NOT NULL DEFAULT '',
  client      TEXT NOT NULL DEFAULT '',
  repo        TEXT NOT NULL DEFAULT '',
  vm          TEXT NOT NULL DEFAULT '',
  command     TEXT NOT NULL DEFAULT '',
  exit        INTEGER,
  duration_ms INTEGER,
  expires     INTEGER,
  version     TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS events_ts ON events (ts);
CREATE INDEX IF NOT EXISTS events_op ON events (op);
CREATE INDEX IF NOT EXISTS events_machine_vm ON events (machine, vm, ts);
CREATE INDEX IF NOT EXISTS events_owner ON events (owner, ts);
