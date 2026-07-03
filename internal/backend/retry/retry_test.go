package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDo_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), func(ctx context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestDo_RetriesUpToLimit(t *testing.T) {
	calls := 0
	wantErr := errors.New("boom")
	err := Do(context.Background(), func(ctx context.Context) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wantErr, got %v", err)
	}
	if calls != DefaultAttempts {
		t.Fatalf("expected %d calls, got %d", DefaultAttempts, calls)
	}
}

func TestDo_SucceedsAfterFailures(t *testing.T) {
	calls := 0
	err := Do(context.Background(), func(ctx context.Context) error {
		calls++
		if calls < 2 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

func TestDo_ContextCancelledBeforeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := Do(ctx, func(ctx context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls after cancellation, got %d", calls)
	}
}

func TestDo_ContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := Do(ctx, func(ctx context.Context) error {
			calls++
			return errors.New("always fails")
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	}()
	// Let the first attempt happen, then cancel during the backoff sleep.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Do did not return promptly after context cancellation")
	}
	if calls < 1 || calls >= DefaultAttempts {
		t.Fatalf("expected cancellation to cut retries short, got %d calls", calls)
	}
}

func TestDoN_CustomAttempts(t *testing.T) {
	calls := 0
	err := DoN(context.Background(), 5, func(ctx context.Context) error {
		calls++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 5 {
		t.Fatalf("expected 5 calls, got %d", calls)
	}
}

func TestDoN_MinimumOneAttempt(t *testing.T) {
	calls := 0
	err := DoN(context.Background(), 0, func(ctx context.Context) error {
		calls++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (minimum), got %d", calls)
	}
}

func TestDo_PermanentErrorStopsImmediately(t *testing.T) {
	calls := 0
	sentinel := errors.New("not found")
	err := Do(context.Background(), func(ctx context.Context) error {
		calls++
		return Permanent(sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call for permanent error, got %d", calls)
	}
}

func TestPermanent_Nil(t *testing.T) {
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) should be nil")
	}
}

func TestBackoff_Increases(t *testing.T) {
	d0 := backoff(0)
	d1 := backoff(1)
	if d0 < baseDelay {
		t.Fatalf("backoff(0) = %v, want >= %v", d0, baseDelay)
	}
	if d1 < baseDelay*2 {
		t.Fatalf("backoff(1) = %v, want >= %v", d1, baseDelay*2)
	}
}
