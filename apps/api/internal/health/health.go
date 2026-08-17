package health

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

type Checker interface {
	Check(context.Context) map[string]error
}

type Dependencies struct {
	AssetDirectory string
	DatabaseCheck  func(context.Context) error
	DialTimeout    time.Duration
}

type checkCache struct {
	mu        sync.Mutex
	check     func(context.Context) error
	cacheTTL  time.Duration
	timeout   time.Duration
	now       func() time.Time
	checkedAt time.Time
	result    error
	running   bool
	done      chan struct{}
}

func CacheCheck(check func(context.Context) error, cacheTTL, timeout time.Duration) func(context.Context) error {
	cache := newCheckCache(check, cacheTTL, timeout, time.Now)
	return cache.Check
}

func newCheckCache(check func(context.Context) error, cacheTTL, timeout time.Duration, now func() time.Time) *checkCache {
	return &checkCache{check: check, cacheTTL: cacheTTL, timeout: timeout, now: now}
}

func (c *checkCache) Check(ctx context.Context) error {
	c.mu.Lock()
	if !c.checkedAt.IsZero() && c.now().Sub(c.checkedAt) < c.cacheTTL {
		result := c.result
		c.mu.Unlock()
		return result
	}
	if c.running {
		done := c.done
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			c.mu.Lock()
			result := c.result
			c.mu.Unlock()
			return result
		}
	}
	c.running = true
	c.done = make(chan struct{})
	c.mu.Unlock()

	checkContext, cancel := context.WithTimeout(context.Background(), c.timeout)
	result := c.check(checkContext)
	cancel()

	c.mu.Lock()
	c.result = result
	c.checkedAt = c.now()
	c.running = false
	close(c.done)
	c.mu.Unlock()
	return result
}

func (d Dependencies) Check(ctx context.Context) map[string]error {
	checks := map[string]error{
		"assets": checkAssetDirectory(d.AssetDirectory),
	}
	timeout := d.DialTimeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	if d.DatabaseCheck != nil {
		checkContext, cancel := context.WithTimeout(ctx, timeout)
		checks["database"] = d.DatabaseCheck(checkContext)
		cancel()
	}
	return checks
}

func checkAssetDirectory(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("asset path is not a directory")
	}
	probe, err := os.CreateTemp(directory, ".readiness-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		return closeErr
	}
	return os.Remove(name)
}
