package itdog

import (
	"strings"
	"sync"
)

type cookieStore struct {
	mu sync.RWMutex
	m  map[string]string
}

func newCookieStore() *cookieStore {
	return &cookieStore{m: make(map[string]string)}
}

func (s *cookieStore) set(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
}

func (s *cookieStore) get(k string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[k]
}

func (s *cookieStore) len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

func (s *cookieStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[string]string)
}

func (s *cookieStore) cookieString() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	parts := make([]string, 0, len(s.m))
	for k, v := range s.m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ";")
}
