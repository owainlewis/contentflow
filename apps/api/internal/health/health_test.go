package health

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDependenciesReportAssetAndDatabaseState(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	checks := (Dependencies{
		AssetDirectory: t.TempDir(),
		DatabaseCheck:  func(context.Context) error { return nil },
		DialTimeout:    time.Second,
	}).Check(context.Background())

	for _, dependency := range []string{"assets", "database"} {
		if err := checks[dependency]; err != nil {
			t.Fatalf("expected %s to be ready: %v", dependency, err)
		}
	}
}

func TestDependenciesUseBoundedProductionDatabaseCheck(t *testing.T) {
	t.Parallel()
	want := errors.New("database unavailable")
	checks := (Dependencies{
		AssetDirectory: t.TempDir(),
		DialTimeout:    time.Second,
		DatabaseCheck: func(ctx context.Context) error {
			if _, hasDeadline := ctx.Deadline(); !hasDeadline {
				t.Error("production database check has no deadline")
			}
			return want
		},
	}).Check(context.Background())
	if !errors.Is(checks["database"], want) {
		t.Fatalf("readiness did not report database failure: %v", checks["database"])
	}
}

func TestDependenciesReportMissingAssetDirectory(t *testing.T) {
	t.Parallel()

	checks := (Dependencies{AssetDirectory: t.TempDir() + "/missing"}).Check(context.Background())
	if checks["assets"] == nil {
		t.Fatal("expected missing asset directory to fail readiness")
	}
}

func TestCacheCheckCachesAndCoalescesBoundedDependencyResults(t *testing.T) {
	t.Parallel()
	want := errors.New("database unavailable")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	cached := CacheCheck(func(ctx context.Context) error {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			t.Error("cached dependency check has no deadline")
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return want
	}, time.Minute, time.Second)

	results := make(chan error, 20)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		results <- cached(context.Background())
	}()
	<-started
	for range 19 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- cached(context.Background())
		}()
	}
	close(release)
	wait.Wait()
	close(results)
	for result := range results {
		if !errors.Is(result, want) {
			t.Fatalf("cached dependency returned %v", result)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("coalesced dependency ran %d times, want 1", got)
	}
	if result := cached(context.Background()); !errors.Is(result, want) || calls.Load() != 1 {
		t.Fatalf("cached dependency returned %v after %d calls", result, calls.Load())
	}
}

func TestCacheCheckRefreshesAfterTTL(t *testing.T) {
	t.Parallel()
	now := time.Now()
	calls := 0
	cache := newCheckCache(func(context.Context) error {
		calls++
		return nil
	}, time.Minute, time.Second, func() time.Time { return now })
	if err := cache.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cache.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fresh cached dependency ran %d times, want 1", calls)
	}
	now = now.Add(time.Minute)
	if err := cache.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expired cached dependency ran %d times, want 2", calls)
	}
}
