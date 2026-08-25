package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestCleanupWorkerRunsImmediatelyAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		runCleanupWorker(ctx, time.Hour, func(context.Context, time.Time) (int64, error) {
			calls.Add(1)
			called <- struct{}{}
			return 0, nil
		})
		close(done)
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run on startup")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not stop")
	}
	if calls.Load() != 1 {
		t.Fatalf("cleanup ran %d times", calls.Load())
	}
}

func TestCleanupWorkerStopDoesNotRequireParentCancellation(t *testing.T) {
	parent := context.Background()
	called := make(chan struct{}, 1)
	stop := startCleanupWorker(parent, time.Hour, func(context.Context, time.Time) (int64, error) {
		called <- struct{}{}
		return 0, nil
	})
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run on startup")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not join without parent cancellation")
	}
}
