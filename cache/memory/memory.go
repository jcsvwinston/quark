package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
)

// Store is a professional in-memory implementation of quark.CacheStore.
var (
	_ quark.CacheStore  = (*Store)(nil)
	_ quark.CacheLocker = (*Store)(nil)
)

// It supports tag-based invalidation via a reverse-index and is thread-safe.
type Store struct {
	mu         sync.RWMutex
	data       map[string]cacheEntry
	tagToIndex map[string]map[string]struct{}
	stopCh     chan struct{}

	// In-process recompute locks for cross-instance stampede coordination
	// (quark.CacheLocker, ADR-0020). Single-process, so this is the degenerate
	// case — but it gives the stampede wrapper a correct, non-blocking per-key
	// try-lock to exercise the coordination path against.
	lockMu  sync.Mutex
	locks   map[string]memLock
	lockSeq uint64
}

type memLock struct {
	token  uint64
	expiry time.Time
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
		locks:      make(map[string]memLock),
	}
	// Start cleanup goroutine to evict expired entries
	go s.cleanupLoop()
	return s
}

// Close stops the background cleanup goroutine and releases resources.
func (s *Store) Close() {
	close(s.stopCh)
}

// AcquireLock implements quark.CacheLocker — a non-blocking, per-key,
// TTL-bounded try-lock (ADR-0020). Exactly one caller gets acquired=true and a
// release func; others get false until release or expiry. In a single process
// the lock is degenerate (no real cross-instance race), but the contract is
// honoured so the stampede wrapper's coordination path is exercisable in-process.
func (s *Store) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, func() error, error) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	now := time.Now()
	if e, held := s.locks[key]; held && now.Before(e.expiry) {
		return false, nil, nil
	}
	s.lockSeq++
	token := s.lockSeq
	s.locks[key] = memLock{token: token, expiry: now.Add(ttl)}
	release := func() error {
		s.lockMu.Lock()
		defer s.lockMu.Unlock()
		if e, held := s.locks[key]; held && e.token == token {
			delete(s.locks, key)
		}
		return nil
	}
	return true, release, nil
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
