package services

import (
	"sync"
	"testing"
	"time"
)

// TestFinalizeTurnConcurrentDuplicateCalls tests that FinalizeTurn with duplicate calls
// does not crash or cause data races
func TestFinalizeTurnConcurrentDuplicateCalls(t *testing.T) {
	InitCaptureManager()
	globalCaptureEnabled.Store(true)
	GlobalCaptureManager.config.Enabled = true

	turnID := "test-turn-001"
	session := NewCaptureSession("test-req-001", turnID, "test-activity-001", "")
	session.Chunks = []CaptureChunk{
		{Index: 0, Content: "data: {\"type\":\"text\",\"text\":\"hello\"}", Type: "text"},
		{Index: 1, Content: "data: [DONE]", Type: "done"},
	}

	GlobalCaptureManager.mu.Lock()
	GlobalCaptureManager.sessions[turnID] = session
	GlobalCaptureManager.mu.Unlock()

	// 3 concurrent duplicate calls (simulating the bug in providerrelay.go)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("goroutine %d panicked: %v", idx, r)
				}
			}()
			GlobalCaptureManager.FinalizeTurn(turnID, "error", 502, map[string]string{"Content-Type": "text/event-stream"})
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Test timed out - possible deadlock")
	}

	globalCaptureEnabled.Store(false)
}

// TestFinalizeTurnNilRespHeaders tests that passing nil respHeaders does not crash
func TestFinalizeTurnNilRespHeaders(t *testing.T) {
	InitCaptureManager()
	globalCaptureEnabled.Store(true)
	GlobalCaptureManager.config.Enabled = true

	turnID := "test-turn-nil-headers"
	session := NewCaptureSession("test-req-002", turnID, "test-activity-002", "")
	session.Chunks = []CaptureChunk{
		{Index: 0, Content: "data: hello", Type: "event"},
	}

	GlobalCaptureManager.mu.Lock()
	GlobalCaptureManager.sessions[turnID] = session
	GlobalCaptureManager.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panicked with nil headers: %v", r)
		}
	}()

	GlobalCaptureManager.FinalizeTurn(turnID, "success", 200, nil)
	globalCaptureEnabled.Store(false)
}

// TestFinalizeTurnNilSession tests that FinalizeTurn with non-existent turnID does not crash
func TestFinalizeTurnNilSession(t *testing.T) {
	InitCaptureManager()
	globalCaptureEnabled.Store(true)
	GlobalCaptureManager.config.Enabled = true

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panicked with non-existent turn: %v", r)
		}
	}()

	GlobalCaptureManager.FinalizeTurn("non-existent-turn", "error", 502, nil)
	globalCaptureEnabled.Store(false)
}
