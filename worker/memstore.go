package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// memStore is a Store in memory: the bucket of the host build
// (platform_other.go) and of the tests.
type memStore struct {
	mu sync.Mutex
	m  map[string]memObject
}

type memObject struct {
	body        []byte
	contentType string
}

func newMemStore() *memStore { return &memStore{m: map[string]memObject{}} }

func (s *memStore) Get(key string) ([]byte, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.m[key]
	return o.body, o.contentType, ok, nil
}

func (s *memStore) Put(key string, body []byte, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = memObject{append([]byte(nil), body...), contentType}
	return nil
}

// memBlobs is Blobs in memory, behaving as R2 does where the API depends on
// it: a put of the wrong length or hash stores nothing, and List pages.
type memBlobs struct {
	mu       sync.Mutex
	m        map[string]memBlob
	pageSize int // keys per List page; 1000, as R2, unless a test lowers it
}

type memBlob struct {
	body   []byte
	sha256 string
}

func newMemBlobs() *memBlobs { return &memBlobs{m: map[string]memBlob{}, pageSize: 1000} }

func (s *memBlobs) Head(key string) (BlobInfo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.m[key]
	return BlobInfo{Key: key, Size: int64(len(o.body)), SHA256: o.sha256}, ok, nil
}

func (s *memBlobs) Get(key string, rng *byteRange) (BlobInfo, io.ReadCloser, bool, error) {
	s.mu.Lock()
	o, ok := s.m[key]
	s.mu.Unlock()
	info := BlobInfo{Key: key, Size: int64(len(o.body)), SHA256: o.sha256}
	if !ok {
		return info, nil, false, nil
	}
	b := o.body
	if rng != nil {
		if rng.Offset+rng.Length > int64(len(b)) {
			return info, nil, false, fmt.Errorf("range %d+%d is past the end of %d bytes", rng.Offset, rng.Length, len(b))
		}
		b = b[rng.Offset : rng.Offset+rng.Length]
	}
	return info, io.NopCloser(bytes.NewReader(b)), true, nil
}

func (s *memBlobs) Put(key string, body io.Reader, size int64, sum string) (BlobInfo, error) {
	b, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return BlobInfo{}, err
	}
	if int64(len(b)) != size {
		return BlobInfo{}, fmt.Errorf("%d bytes arrived, %d were declared", len(b), size)
	}
	if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != sum {
		return BlobInfo{}, errDigestMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = memBlob{b, sum}
	return BlobInfo{Key: key, Size: size, SHA256: sum}, nil
}

func (s *memBlobs) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// List pages by key: the cursor is the last key of the previous page.
func (s *memBlobs) List(prefix, cursor string) ([]BlobInfo, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.m {
		if strings.HasPrefix(k, prefix) && k > cursor {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	next := ""
	if len(keys) > s.pageSize {
		keys = keys[:s.pageSize]
		next = keys[len(keys)-1]
	}
	out := make([]BlobInfo, len(keys))
	for i, k := range keys {
		out[i] = BlobInfo{Key: k, Size: int64(len(s.m[k].body)), SHA256: s.m[k].sha256}
	}
	return out, next, nil
}
