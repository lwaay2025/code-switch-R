package services

import "sync"

// ProviderConcurrencyManager enforces a max in-flight request limit per provider.
// Key format is decided by the caller (recommended: "{kind}::{providerName}").
type ProviderConcurrencyManager struct {
	mu       sync.Mutex
	inflight map[string]int
}

func NewProviderConcurrencyManager() *ProviderConcurrencyManager {
	return &ProviderConcurrencyManager{
		inflight: make(map[string]int),
	}
}

// TryAcquire tries to acquire one in-flight slot for key.
// If limit <= 0, it always succeeds (no limit).
// The returned release must be called exactly once when ok=true.
func (m *ProviderConcurrencyManager) TryAcquire(key string, limit int) (release func(), ok bool) {
	if limit <= 0 {
		return func() {}, true
	}
	if key == "" {
		return func() {}, true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.inflight == nil {
		m.inflight = make(map[string]int)
	}
	if m.inflight[key] >= limit {
		return nil, false
	}

	m.inflight[key]++
	released := false

	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		if released {
			return
		}
		released = true

		if m.inflight == nil {
			return
		}
		if m.inflight[key] > 0 {
			m.inflight[key]--
		}
		if m.inflight[key] <= 0 {
			delete(m.inflight, key)
		}
	}, true
}
