package main

import "sync"

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
