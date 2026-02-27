package heartbeat

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRunnerValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewRunner("", "Asia/Tokyo", func(context.Context) error { return nil }); err == nil {
		t.Fatal("NewRunner() error = nil for empty spec")
	}
	if _, err := NewRunner("0 */1 * * * *", "", func(context.Context) error { return nil }); err == nil {
		t.Fatal("NewRunner() error = nil for empty timezone")
	}
	if _, err := NewRunner("0 */1 * * * *", "Asia/Tokyo", nil); err == nil {
		t.Fatal("NewRunner() error = nil for nil handler")
	}
}

func TestRunnerStart(t *testing.T) {
	t.Parallel()

	var called atomic.Int32
	r, err := NewRunner("*/1 * * * * *", "UTC", func(context.Context) error {
		called.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	t.Cleanup(cancel)

	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if called.Load() > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("heartbeat handler was not called")
}

func TestExecuteUsesStartContext(t *testing.T) {
	t.Parallel()

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called atomic.Int32
	var sawCanceled atomic.Bool

	r := &Runner{
		handler: func(ctx context.Context) error {
			called.Add(1)
			select {
			case <-ctx.Done():
				sawCanceled.Store(true)
			default:
			}
			return nil
		},
		timezone: "UTC",
		rootCtx:  parentCtx,
	}

	r.execute()
	if called.Load() != 1 {
		t.Fatalf("execute() call count = %d, want 1", called.Load())
	}
	if sawCanceled.Load() {
		t.Fatal("execute() saw canceled context before cancel")
	}

	cancel()
	r.execute()
	if called.Load() != 2 {
		t.Fatalf("execute() call count after cancel = %d, want 2", called.Load())
	}
	if !sawCanceled.Load() {
		t.Fatal("execute() should see canceled context after cancel")
	}
}
