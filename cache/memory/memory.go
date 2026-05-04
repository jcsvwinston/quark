package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
)

// Store is a professional in-memory implementation of quark.CacheStore.
var _ quark.CacheStore = (*Store)(nil)

// It supports tag-based invalidation via a reverse-index and is thread-safe.
type Store struct {
	mu         sync.RWMutex
	data       map[string]cacheEntry
	tagToIndex map[string]map[string]struct{}
	stopCh     chan struct{}
}

type cacheEntry struct {
	Value      []byte
	Expiration time.Time
	Tags       []string
}

// New creates a new in-memory cache store.
func New() *Store {
	s := &Store{
		data:       make(map[string]cacheEntry),
		tagToIndex: make(map[string]map[string]struct{}),
		stopCh:     make(chan struct{}),
	}
	// Start cleanup goroutine to evict expired entries
	go s.cleanupLoop()
	return s
}

// Close stops the background cleanup goroutine and releases resources.
func (s *Store) Close() {
	close(s.stopCh)
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.data[key]
	if !ok || time.Now().After(entry.Expiration) {
		return nil, fmt.Errorf("cache miss")
	}
	return entry.Value, nil
}

func (s *Store) Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[key] = cacheEntry{
		Value:      val,
		Expiration: time.Now().Add(ttl),
		Tags:       tags,
	}

	for _, tag := range tags {
		if _, ok := s.tagToIndex[tag]; !ok {
			s.tagToIndex[tag] = make(map[string]struct{})
		}
		s.tagToIndex[tag][key] = struct{}{}
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *Store) InvalidateTags(ctx context.Context, tags ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tag := range tags {
		if keys, ok := s.tagToIndex[tag]; ok {
			for key := range keys {
				delete(s.data, key)
			}
			delete(s.tagToIndex, tag)
		}
	}
	return nil
}

func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for key, entry := range s.data {
				if now.After(entry.Expiration) {
					delete(s.data, key)
				}
			}
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}
