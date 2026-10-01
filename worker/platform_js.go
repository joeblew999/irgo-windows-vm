//go:build js && wasm

package main

import (
	"bytes"
	"io"

	"github.com/syumai/workers-go/cloudflare"
	"github.com/syumai/workers-go/cloudflare/r2"
)

// getenv reads a var or secret of the current request's environment.
func getenv(name string) string { return cloudflare.Getenv(name) }

// siteBucket is the R2 binding SITE (wrangler.toml).
func siteBucket() (Store, error) {
	b, err := r2.NewBucket("SITE")
	if err != nil {
		return nil, err
	}
	return r2Store{b}, nil
}

type r2Store struct{ b *r2.Bucket }

func (s r2Store) Get(key string) ([]byte, string, bool, error) {
	o, err := s.b.Get(key)
	if err != nil || o == nil {
		return nil, "", false, err
	}
	body, err := io.ReadAll(o.Body)
	if err != nil {
		return nil, "", false, err
	}
	return body, o.HTTPMetadata.ContentType, true, nil
}

func (s r2Store) Put(key string, body []byte, contentType string) error {
	_, err := s.b.Put(key, io.NopCloser(bytes.NewReader(body)), &r2.PutOptions{
		HTTPMetadata: r2.HTTPMetadata{ContentType: contentType},
	})
	return err
}
