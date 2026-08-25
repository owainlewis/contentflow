package content

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/owainlewis/contentflow/apps/api/internal/database"
)

// newPostgresStore connects to the database named by CONTENTFLOW_TEST_DATABASE_URL
// and truncates it. Without that variable the Postgres tests are skipped so the
// suite still runs on a machine with no database.
func newPostgresStore(t *testing.T) (*PostgresStore, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("CONTENTFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CONTENTFLOW_TEST_DATABASE_URL is not set")
	}
	pool, err := database.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), "truncate content_items, mutation_receipts"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewPostgresStore(pool), pool
}

func TestPostgresStorePersistsEveryTypeAndEnforcesExpiry(t *testing.T) {
	store, _ := newPostgresStore(t)
	ctx := context.Background()
	service := NewService(store)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	cases := []struct {
		kind    Type
		content any
	}{
		{TypeYouTube, YouTubeContent{Transcript: "spoken", Sections: []Section{{Position: 0, Title: "Intro", Body: "opening"}}}},
		{TypeLinkedIn, LinkedInContent{Body: "body"}},
		{TypeX, XContent{Body: "body"}},
		{TypeInstagram, InstagramContent{Script: "script"}},
		{TypeTikTok, TikTokContent{Script: "script"}},
		{TypeEmail, EmailContent{Subject: "subject", Body: "body"}},
		{TypeSubstack, SubstackContent{Headline: "headline", Subheadline: "sub", Body: "body"}},
	}
	ids := make(map[Type]string)
	for _, test := range cases {
		result, err := service.Create(ctx, "workspace-a", CreateRequest{
			Type: test.kind, WorkingTitle: "ＦＯＯ Guide", Status: StatusDraft,
			OperationID: testOperationID(), Content: test.content,
		}, "hash-"+string(test.kind))
		if err != nil {
			t.Fatalf("create %s: %v", test.kind, err)
		}
		ids[test.kind] = result.ItemIDs[0]
	}

	for _, test := range cases {
		item, err := service.Get(ctx, "workspace-a", ids[test.kind])
		if err != nil || item.Type != test.kind {
			t.Fatalf("read back %s: %#v, %v", test.kind, item, err)
		}
		if _, err := service.Get(ctx, "workspace-b", ids[test.kind]); err == nil {
			t.Fatalf("%s escaped its workspace", test.kind)
		}
	}

	youtube, err := service.Get(ctx, "workspace-a", ids[TypeYouTube])
	if err != nil {
		t.Fatal(err)
	}
	sections := youtube.Content.(YouTubeContent).Sections
	if len(sections) != 1 || sections[0].Title != "Intro" || sections[0].ID == "" {
		t.Fatalf("sections did not round trip through jsonb: %#v", sections)
	}

	listed, err := service.List(ctx, "workspace-a", ListQuery{Type: TypeYouTube, Status: StatusDraft, TitlePrefix: "foo"})
	if err != nil || len(listed) != 1 || listed[0].ID != ids[TypeYouTube] {
		t.Fatalf("filtered prefix search returned %#v, %v", listed, err)
	}

	now = now.Add(ContentLifetime)
	if _, err := service.Get(ctx, "workspace-a", ids[TypeX]); err == nil {
		t.Fatal("expired item stayed readable at its deadline")
	}
	if expired, err := service.List(ctx, "workspace-a", ListQuery{}); err != nil || len(expired) != 0 {
		t.Fatalf("expired items stayed listed: %#v, %v", expired, err)
	}
}

func TestPostgresStoreEnforcesRevisionsAndReplaysOperations(t *testing.T) {
	store, _ := newPostgresStore(t)
	ctx := context.Background()
	service := NewService(store)

	operation := testOperationID()
	created, err := service.Create(ctx, "workspace", CreateRequest{
		Type: TypeX, WorkingTitle: "First", Status: StatusDraft, OperationID: operation, Content: XContent{Body: "one"},
	}, "same-bytes")
	if err != nil {
		t.Fatal(err)
	}
	id := created.ItemIDs[0]

	replay, err := service.Create(ctx, "workspace", CreateRequest{
		Type: TypeX, WorkingTitle: "First", Status: StatusDraft, OperationID: operation, Content: XContent{Body: "one"},
	}, "same-bytes")
	if err != nil || replay.ItemIDs[0] != id {
		t.Fatalf("retry created a second item: %#v, %v", replay, err)
	}
	if _, err := service.Create(ctx, "workspace", CreateRequest{
		Type: TypeX, WorkingTitle: "First", Status: StatusDraft, OperationID: operation, Content: XContent{Body: "changed"},
	}, "different-bytes"); err == nil {
		t.Fatal("reused operation ID with different bytes was accepted")
	} else {
		assertErrorCode(t, err, "operation_id_conflict")
	}

	if _, err := service.Replace(ctx, "workspace", id, ReplaceRequest{
		CreateRequest: CreateRequest{Type: TypeX, WorkingTitle: "Stale", Status: StatusDraft, OperationID: testOperationID(), Content: XContent{Body: "stale"}},
		Revision:      99,
	}, "stale"); err == nil {
		t.Fatal("stale revision was accepted")
	} else {
		assertErrorCode(t, err, "revision_conflict")
	}

	if _, err := service.Replace(ctx, "workspace", id, ReplaceRequest{
		CreateRequest: CreateRequest{Type: TypeX, WorkingTitle: "Second", Status: StatusReady, OperationID: testOperationID(), Content: XContent{Body: "two"}},
		Revision:      1,
	}, "second"); err != nil {
		t.Fatalf("replace failed: %v", err)
	}
	updated, err := service.Get(ctx, "workspace", id)
	if err != nil || updated.Revision != 2 || updated.Status != StatusReady {
		t.Fatalf("replacement did not apply: %#v, %v", updated, err)
	}

	if _, err := service.Delete(ctx, "workspace", id, RevisionRequest{OperationID: testOperationID(), Revision: 2}, "delete"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := service.Get(ctx, "workspace", id); err == nil {
		t.Fatal("deleted item stayed readable")
	}
}

func TestPostgresStoreReplaysConcurrentIdenticalOperations(t *testing.T) {
	store, _ := newPostgresStore(t)
	service := NewService(store)
	operationID := testOperationID()
	request := CreateRequest{
		Type: TypeX, WorkingTitle: "Concurrent", Status: StatusDraft,
		OperationID: operationID, Content: XContent{Body: "same body"},
	}
	const callers = 8
	results := make(chan MutationResult, callers)
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.Create(context.Background(), "workspace", request, "same-bytes")
			results <- result
			errorsByCaller <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent create failed: %v", err)
		}
	}
	var itemID string
	for result := range results {
		if len(result.ItemIDs) != 1 {
			t.Fatalf("unexpected result: %#v", result)
		}
		if itemID == "" {
			itemID = result.ItemIDs[0]
		} else if result.ItemIDs[0] != itemID {
			t.Fatalf("operation created both %s and %s", itemID, result.ItemIDs[0])
		}
	}
}

func TestPostgresCleanupRemovesExpiredRows(t *testing.T) {
	store, pool := newPostgresStore(t)
	ctx := context.Background()
	service := NewService(store)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	if _, err := service.Create(ctx, "workspace", CreateRequest{
		Type: TypeX, WorkingTitle: "Temporary", Status: StatusDraft, OperationID: testOperationID(), Content: XContent{Body: "body"},
	}, "cleanup"); err != nil {
		t.Fatal(err)
	}

	removed, err := database.Cleanup(ctx, pool, now)
	if err != nil {
		t.Fatalf("cleanup before expiry: %v", err)
	}
	if removed != 0 {
		t.Fatalf("cleanup removed %d live rows", removed)
	}

	removed, err = database.Cleanup(ctx, pool, now.Add(ContentLifetime+time.Hour))
	if err != nil {
		t.Fatalf("cleanup after expiry: %v", err)
	}
	if removed == 0 {
		t.Fatal("cleanup left expired rows behind")
	}
	var remaining int
	if err := pool.QueryRow(ctx, "select count(*) from content_items").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d expired items survived cleanup", remaining)
	}
}
