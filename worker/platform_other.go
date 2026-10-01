//go:build !js

package main

import (
	"database/sql"
	_ "embed"
	"os"
	"sync"

	_ "modernc.org/sqlite" // the host's stand-in for D1, which is SQLite
)

// getenv on the host is the process environment.
func getenv(name string) string { return os.Getenv(name) }

var (
	hostBucket = newMemStore()
	hostGolden = newMemBlobs()
	hostLedger = sync.OnceValues(openMemLedger)
)

// siteBucket on the host is in memory, gone when the process exits.
func siteBucket() (Store, error) { return hostBucket, nil }

// goldenBucket on the host is in memory too.
func goldenBucket() (Blobs, error) { return hostGolden, nil }

// ledgerDB on the host is an in-memory SQLite with the D1 migration applied.
func ledgerDB() (*sql.DB, error) { return hostLedger() }

//go:embed migrations/0001_ledger.sql
var ledgerSchema string

// openMemLedger is a fresh in-memory database holding the ledger's schema,
// exactly the file wrangler applies to D1. One connection, because each
// connection to ":memory:" is a database of its own.
func openMemLedger() (*sql.DB, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ledgerSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
