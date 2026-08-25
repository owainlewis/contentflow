package database

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConcurrentMigrateCallsAreSerialized(t *testing.T) {
	url := os.Getenv("CONTENTFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CONTENTFLOW_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const callers = 4
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByCaller <- Migrate(context.Background(), pool)
		}()
	}
	group.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent migrate failed: %v", err)
		}
	}
}
