package services

import (
	"errors"
	"testing"
)

func TestRetrySameProviderStopsAfterSuccess(t *testing.T) {
	attempts := 0
	result := retrySameProvider(3, 0, func(attempt int) (bool, error, bool) {
		attempts++
		if attempt == 1 {
			return true, nil, true
		}
		return false, errTokenZero, false
	})

	if !result.ok {
		t.Fatalf("expected success, got err=%v", result.err)
	}
	if result.attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.attempts)
	}
	if attempts != 2 {
		t.Fatalf("callback attempts = %d, want 2", attempts)
	}
}

func TestRetrySameProviderStopsAfterResponseWrittenFailure(t *testing.T) {
	attempts := 0
	result := retrySameProvider(3, 0, func(attempt int) (bool, error, bool) {
		attempts++
		return false, errTokenZero, true
	})

	if result.ok {
		t.Fatal("expected failure")
	}
	if !result.responseWritten {
		t.Fatal("expected responseWritten=true")
	}
	if result.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", result.attempts)
	}
	if attempts != 1 {
		t.Fatalf("callback attempts = %d, want 1", attempts)
	}
}

func TestRetrySameProviderStopsAfterClientAbort(t *testing.T) {
	attempts := 0
	abortErr := errors.New("boom")
	result := retrySameProvider(3, 0, func(attempt int) (bool, error, bool) {
		attempts++
		return false, errors.Join(errClientAbort, abortErr), false
	})

	if result.ok {
		t.Fatal("expected failure")
	}
	if !errors.Is(result.err, errClientAbort) {
		t.Fatalf("expected errClientAbort, got %v", result.err)
	}
	if result.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", result.attempts)
	}
	if attempts != 1 {
		t.Fatalf("callback attempts = %d, want 1", attempts)
	}
}

func TestRetrySameProviderStopsAfterProviderBusy(t *testing.T) {
	attempts := 0
	result := retrySameProvider(3, 0, func(attempt int) (bool, error, bool) {
		attempts++
		return false, errProviderBusy, false
	})

	if result.ok {
		t.Fatal("expected failure")
	}
	if !errors.Is(result.err, errProviderBusy) {
		t.Fatalf("expected errProviderBusy, got %v", result.err)
	}
	if result.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", result.attempts)
	}
	if attempts != 1 {
		t.Fatalf("callback attempts = %d, want 1", attempts)
	}
}

func TestRetrySameProviderExhaustsRetries(t *testing.T) {
	attempts := 0
	result := retrySameProvider(3, 0, func(attempt int) (bool, error, bool) {
		attempts++
		return false, errTokenZero, false
	})

	if result.ok {
		t.Fatal("expected failure")
	}
	if !errors.Is(result.err, errTokenZero) {
		t.Fatalf("expected errTokenZero, got %v", result.err)
	}
	if result.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", result.attempts)
	}
	if attempts != 3 {
		t.Fatalf("callback attempts = %d, want 3", attempts)
	}
}
