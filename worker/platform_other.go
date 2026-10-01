//go:build !js

package main

import "os"

// getenv on the host is the process environment.
func getenv(name string) string { return os.Getenv(name) }

var (
	hostBucket = newMemStore()
	hostGolden = newMemBlobs()
)

// siteBucket on the host is in memory, gone when the process exits.
func siteBucket() (Store, error) { return hostBucket, nil }

// goldenBucket on the host is in memory too.
func goldenBucket() (Blobs, error) { return hostGolden, nil }
