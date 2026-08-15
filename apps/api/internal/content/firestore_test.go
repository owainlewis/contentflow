package content

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"cloud.google.com/go/firestore"
)

const contentEmulatorTimeout = 90 * time.Second

func contentEmulatorContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), contentEmulatorTimeout)
	t.Cleanup(cancel)
	return ctx
}

func TestFirestoreContentContract(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := contentEmulatorContext(t)
	client, err := firestore.NewClient(ctx, "contentflow-content-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := NewFirestoreStore(client)
	service := NewService(store)
	now := time.Date(2026, 8, 15, 12, 0, 0, 123456789, time.UTC)
	service.now = func() time.Time { return now }
	workspace := "workspace-" + testOperationID()

	allTypes := []struct {
		kind    Type
		content any
	}{
		{TypeYouTube, YouTubeContent{Transcript: "spoken", Sections: []Section{{Position: 0, Title: "Intro", Body: "planned"}, {Position: 1, Title: "Body", Body: "planned body"}}}},
		{TypeLinkedIn, LinkedInContent{Body: "linkedin"}},
		{TypeX, XContent{Body: "x"}},
		{TypeInstagram, InstagramContent{Script: "instagram"}},
		{TypeTikTok, TikTokContent{Script: "tiktok"}},
		{TypeEmail, EmailContent{Subject: "subject", Body: "email"}},
		{TypeSubstack, SubstackContent{Headline: "headline", Subheadline: "subheadline", Body: "substack"}},
	}
	ids := make(map[Type]string)
	for index, test := range allTypes {
		title := "Other " + string(test.kind)
		if test.kind == TypeYouTube {
			title = "ＦＯＯ Emulator"
		}
		request := CreateRequest{Type: test.kind, WorkingTitle: title, Status: StatusDraft, OperationID: testOperationID(), Content: test.content}
		requestHash := "create-" + string(test.kind)
		result, err := service.Create(ctx, workspace, request, requestHash)
		if err != nil {
			t.Fatalf("create %s: %v", test.kind, err)
		}
		if result.Revisions[0] != 1 || !result.ExpiresAt[0].Equal(now.Truncate(time.Microsecond).Add(ContentLifetime)) {
			t.Fatalf("%s result has wrong revision or expiry: %#v", test.kind, result)
		}
		if test.kind == TypeYouTube {
			replayed, err := service.Create(ctx, workspace, request, requestHash)
			if err != nil || !reflect.DeepEqual(replayed, result) {
				t.Fatalf("create receipt changed after Firestore round trip: %#v then %#v, %v", result, replayed, err)
			}
		}
		ids[test.kind] = result.ItemIDs[0]
		if index > 0 && ids[allTypes[index-1].kind] >= ids[test.kind] {
			t.Fatalf("content ULIDs are not monotonic")
		}
	}

	secondClient, err := firestore.NewClient(ctx, "contentflow-content-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondClient.Close() })
	restarted := NewService(NewFirestoreStore(secondClient))
	restarted.now = service.now
	for _, test := range allTypes {
		item, err := restarted.Get(ctx, workspace, ids[test.kind])
		if err != nil || item.Type != test.kind || item.Revision != 1 {
			t.Fatalf("restart read %s: %#v, %v", test.kind, item, err)
		}
		if _, err := restarted.Get(ctx, workspace+"-other", ids[test.kind]); err == nil {
			t.Fatalf("%s escaped workspace scope", test.kind)
		}
	}

	youtubeID := ids[TypeYouTube]
	youtubeItem, err := service.Get(ctx, workspace, youtubeID)
	if err != nil {
		t.Fatal(err)
	}
	youtube := youtubeItem.Content.(YouTubeContent)
	if len(youtube.Sections) != 2 || youtube.Sections[0].ID == "" || youtube.Sections[0].ID >= youtube.Sections[1].ID {
		t.Fatalf("initial sections lack ordered stable IDs: %#v", youtube.Sections)
	}
	parentSnapshot, err := client.Collection(contentItemsCollection).Doc(youtubeID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, inline := parentSnapshot.Data()["sections"]; inline {
		t.Fatal("sections were stored on the parent")
	}
	storedContent := parentSnapshot.Data()["content"].(map[string]any)
	if storedContent["transcript"] != "spoken" {
		t.Fatalf("transcript was not independently stored on parent: %#v", storedContent)
	}

	mismatched := CreateRequest{Type: TypeEmail, WorkingTitle: "Mismatch", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "wrong", Sections: []Section{}}}
	_, err = service.Create(ctx, workspace, mismatched, "mismatch")
	assertErrorCode(t, err, "invalid_discriminator")

	staleCreated, err := service.Create(ctx, workspace, CreateRequest{Type: TypeYouTube, WorkingTitle: "Conflict Video", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "spoken", Sections: []Section{{Position: 0, Title: "Removed", Body: "old"}}}}, "stale-create")
	if err != nil {
		t.Fatal(err)
	}
	staleID := staleCreated.ItemIDs[0]
	staleOriginal, err := service.Get(ctx, workspace, staleID)
	if err != nil {
		t.Fatal(err)
	}
	removedSection := staleOriginal.Content.(YouTubeContent).Sections[0]
	if _, err := service.Replace(ctx, workspace, staleID, ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Conflict Video", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "spoken", Sections: []Section{}}}, Revision: 1}, "remove-section"); err != nil {
		t.Fatal(err)
	}
	_, err = service.Replace(ctx, workspace, staleID, ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Conflict Video", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "stale", Sections: []Section{removedSection}}}, Revision: 1}, "stale-removed-section")
	contentError, ok := err.(*Error)
	if !ok || contentError.Code != "revision_conflict" || contentError.Current == nil || contentError.Current.Revision != 2 {
		t.Fatalf("stale removed section returned %v", err)
	}

	firstID, secondID := youtube.Sections[0].ID, youtube.Sections[1].ID
	replaceOperation := testOperationID()
	replacement := ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Foo Emulator", Status: StatusReady, OperationID: replaceOperation, Content: YouTubeContent{Transcript: "spoken changed", Sections: []Section{{ID: secondID, Position: 0, Title: "Body", Body: "planned body"}, {ID: firstID, Position: 1, Title: "Intro", Body: "planned"}, {Position: 2, Title: "End", Body: "planned end"}}}}, Revision: 1}
	replaced, err := service.Replace(ctx, workspace, youtubeID, replacement, "replace-youtube")
	if err != nil || replaced.Revisions[0] != 2 {
		t.Fatalf("replace YouTube: %#v, %v", replaced, err)
	}
	replayed, err := restarted.Replace(ctx, workspace, youtubeID, replacement, "replace-youtube")
	if err != nil || replayed.Revisions[0] != 2 {
		t.Fatalf("cross-instance retry: %#v, %v", replayed, err)
	}
	_, err = restarted.Replace(ctx, workspace, youtubeID, replacement, "different-request-bytes")
	assertErrorCode(t, err, "operation_id_conflict")

	after, err := restarted.Get(ctx, workspace, youtubeID)
	if err != nil {
		t.Fatal(err)
	}
	afterYouTube := after.Content.(YouTubeContent)
	if afterYouTube.Sections[0].ID != secondID || afterYouTube.Sections[1].ID != firstID || afterYouTube.Sections[2].ID == "" {
		t.Fatalf("section replacement lost identity/order: %#v", afterYouTube.Sections)
	}
	if afterYouTube.Transcript != "spoken changed" || afterYouTube.Sections[0].Body != "planned body" {
		t.Fatalf("transcript and script were coupled: %#v", afterYouTube)
	}

	invalidOrder := replacement
	invalidOrder.OperationID = testOperationID()
	invalidOrder.Revision = 2
	invalidContent := invalidOrder.Content.(YouTubeContent)
	invalidContent.Sections = append([]Section(nil), invalidContent.Sections...)
	invalidContent.Sections[0].Position = 1
	invalidOrder.Content = invalidContent
	_, err = service.Replace(ctx, workspace, youtubeID, invalidOrder, "invalid-order")
	assertErrorCode(t, err, "invalid_section_order")

	stale := replacement
	stale.OperationID = testOperationID()
	_, err = service.Replace(ctx, workspace, youtubeID, stale, "stale-revision")
	contentError, ok = err.(*Error)
	if !ok || contentError.Code != "revision_conflict" || contentError.Current == nil || contentError.Current.Revision != 2 {
		t.Fatalf("stale revision returned %v", err)
	}

	clear := ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Foo Emulator", Status: StatusReady, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "\u2003\n", Sections: afterYouTube.Sections}}, Revision: 2}
	if _, err := service.Replace(ctx, workspace, youtubeID, clear, "clear-transcript"); err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Transcript(ctx, workspace, youtubeID)
	assertErrorCode(t, err, "transcript_missing")
	cleared, err := restarted.Get(ctx, workspace, youtubeID)
	if err != nil {
		t.Fatal(err)
	}
	clearedYouTube := cleared.Content.(YouTubeContent)
	for index := range afterYouTube.Sections {
		if clearedYouTube.Sections[index].ID != afterYouTube.Sections[index].ID || clearedYouTube.Sections[index].Body != afterYouTube.Sections[index].Body {
			t.Fatal("transcript clear changed script sections")
		}
	}

	archiveOperation := testOperationID()
	archived, err := service.SetArchived(ctx, workspace, youtubeID, RevisionRequest{OperationID: archiveOperation, Revision: 3}, true, "archive")
	if err != nil || archived.Revisions[0] != 4 {
		t.Fatalf("archive: %#v, %v", archived, err)
	}
	archivedItem, err := restarted.Get(ctx, workspace, youtubeID)
	if err != nil || archivedItem.ArchivedAt == nil || !archivedItem.ExpiresAt.Equal(youtubeItem.ExpiresAt) {
		t.Fatalf("archive extended expiry: %#v, %v", archivedItem, err)
	}
	restored, err := service.SetArchived(ctx, workspace, youtubeID, RevisionRequest{OperationID: testOperationID(), Revision: 4}, false, "restore")
	if err != nil || restored.Revisions[0] != 5 {
		t.Fatalf("restore: %#v, %v", restored, err)
	}
	restoredItem, err := restarted.Get(ctx, workspace, youtubeID)
	if err != nil || restoredItem.ArchivedAt != nil || !restoredItem.ExpiresAt.Equal(youtubeItem.ExpiresAt) {
		t.Fatalf("restore extended expiry: %#v, %v", restoredItem, err)
	}

	for _, filter := range []ListQuery{{}, {Type: TypeYouTube}, {Status: StatusDraft}, {TitlePrefix: "foo"}, {Type: TypeYouTube, Status: StatusReady, TitlePrefix: "foo"}} {
		items, err := restarted.List(ctx, workspace, filter)
		if err != nil {
			t.Fatalf("indexed list %#v: %v", filter, err)
		}
		if filter.TitlePrefix != "" && (len(items) != 1 || items[0].ID != youtubeID) {
			t.Fatalf("prefix list %#v returned %#v", filter, items)
		}
	}

	longTitle := strings.Repeat("a", MaxIndexedStringBytes+400) + " suffix"
	longResult, err := service.Create(ctx, workspace, CreateRequest{Type: TypeX, WorkingTitle: longTitle, Status: StatusDraft, OperationID: testOperationID(), Content: XContent{Body: "long title"}}, "long-title")
	if err != nil {
		t.Fatal(err)
	}
	longMatches, err := restarted.List(ctx, workspace, ListQuery{TitlePrefix: strings.Repeat("a", MaxIndexedStringBytes+200)})
	if err != nil || len(longMatches) != 1 || longMatches[0].ID != longResult.ItemIDs[0] {
		t.Fatalf("long indexed-prefix search returned %#v, %v", longMatches, err)
	}
	maxRuneTitle := string(unicode.MaxRune) + " tail"
	maxRuneResult, err := service.Create(ctx, workspace, CreateRequest{Type: TypeX, WorkingTitle: maxRuneTitle, Status: StatusDraft, OperationID: testOperationID(), Content: XContent{Body: "max rune"}}, "max-rune-title")
	if err != nil {
		t.Fatal(err)
	}
	maxRuneMatches, err := restarted.List(ctx, workspace, ListQuery{TitlePrefix: string(unicode.MaxRune)})
	if err != nil || len(maxRuneMatches) != 1 || maxRuneMatches[0].ID != maxRuneResult.ItemIDs[0] {
		t.Fatalf("max-rune prefix search returned %#v, %v", maxRuneMatches, err)
	}
	widthBoundaryPrefix := strings.Repeat("b", MaxIndexedStringBytes-1) + string(rune(0x7F))
	widthBoundaryResult, err := service.Create(ctx, workspace, CreateRequest{Type: TypeX, WorkingTitle: widthBoundaryPrefix + " tail", Status: StatusDraft, OperationID: testOperationID(), Content: XContent{Body: "width boundary"}}, "width-boundary-title")
	if err != nil {
		t.Fatal(err)
	}
	widthBoundaryMatches, err := restarted.List(ctx, workspace, ListQuery{TitlePrefix: widthBoundaryPrefix})
	if err != nil || len(widthBoundaryMatches) != 1 || widthBoundaryMatches[0].ID != widthBoundaryResult.ItemIDs[0] {
		t.Fatalf("width-boundary prefix search returned %#v, %v", widthBoundaryMatches, err)
	}

	oversizedText := CreateRequest{Type: TypeX, WorkingTitle: "Large", Status: StatusDraft, OperationID: testOperationID(), Content: XContent{Body: strings.Repeat("x", MaxTextBytes+1)}}
	_, err = service.Create(ctx, workspace, oversizedText, "oversized-text")
	assertErrorCode(t, err, "text_field_too_large")
	large := strings.Repeat("x", 460<<10)
	oversizedParent := CreateRequest{Type: TypeYouTube, WorkingTitle: "Large", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Description: large, Transcript: large, Sections: []Section{}}}
	_, err = service.Create(ctx, workspace, oversizedParent, "oversized-parent")
	assertErrorCode(t, err, "content_document_too_large")

	receiptSnapshot, err := store.receiptRef(workspace, replaceOperation).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiptData := receiptSnapshot.Data()
	for _, forbidden := range []string{"content", "transcript", "sections", "body", "response"} {
		if _, present := receiptData[forbidden]; present {
			t.Fatalf("receipt stores forbidden %s: %#v", forbidden, receiptData)
		}
	}
	if size, err := encodedFirestoreSize(receiptToDocument(Receipt{WorkspaceID: workspace, RequestHash: "hash", Operation: "replace", HTTPStatus: 200, MutationResult: replaced, Expires: now.Add(ReceiptLifetime)})); err != nil || size >= MaxFirestoreBytes {
		t.Fatalf("receipt is not bounded: %d, %v", size, err)
	}

	now = youtubeItem.ExpiresAt
	if _, err := restarted.Get(ctx, workspace, youtubeID); err == nil {
		t.Fatal("expired item remained readable at deadline")
	}
	items, err := restarted.List(ctx, workspace, ListQuery{TitlePrefix: "foo"})
	if err != nil || len(items) != 0 {
		t.Fatalf("expired item remained in search: %#v, %v", items, err)
	}
}

func TestFirestoreBatchContract(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := contentEmulatorContext(t)
	client, err := firestore.NewClient(ctx, "contentflow-batch-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := NewFirestoreStore(client)
	service := NewService(store)
	now := time.Date(2026, 8, 15, 12, 0, 0, 123456789, time.UTC)
	service.now = func() time.Time { return now }
	workspace := "batch-workspace-" + testOperationID()

	oneOperation := testOperationID()
	one, err := service.BatchCreate(ctx, workspace, batchRequest(oneOperation, 1, "one"), "one-bytes")
	if err != nil || len(one.ItemIDs) != 1 {
		t.Fatalf("one-item boundary: %#v, %v", one, err)
	}
	fiftyOperation := testOperationID()
	fifty, err := service.BatchCreate(ctx, workspace, batchRequest(fiftyOperation, MaxBatchItems, "fifty"), "fifty-bytes")
	if err != nil || len(fifty.ItemIDs) != MaxBatchItems {
		t.Fatalf("50-item boundary: %d items, %v", len(fifty.ItemIDs), err)
	}
	listed, err := service.List(ctx, workspace, ListQuery{})
	if err != nil || len(listed) != MaxBatchItems+1 {
		t.Fatalf("atomic boundaries stored %d items: %v", len(listed), err)
	}

	fiftyReceipt, err := store.receiptRef(workspace, fiftyOperation).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiptData := fiftyReceipt.Data()
	for _, forbidden := range []string{"content", "transcript", "sections", "body", "source", "provenance", "response"} {
		if _, present := receiptData[forbidden]; present {
			t.Fatalf("batch receipt stores forbidden %s: %#v", forbidden, receiptData)
		}
	}
	if size, err := encodedFirestoreSize(receiptData); err != nil || size >= MaxFirestoreBytes {
		t.Fatalf("50-item receipt is not bounded: %d, %v", size, err)
	}
	receiptExpiry := receiptData["expires_at"].(time.Time)
	if !receiptExpiry.Equal(now.Truncate(time.Microsecond).Add(ReceiptLifetime)) {
		t.Fatalf("receipt expires at %s", receiptExpiry)
	}

	invalidOperation := testOperationID()
	invalid := BatchRequest{OperationID: invalidOperation, Items: []BatchItemRequest{
		{Type: TypeX, WorkingTitle: "Would roll back", Status: StatusDraft, Content: XContent{Body: "first"}},
		{Type: TypeX, WorkingTitle: "", Status: StatusDraft, Content: XContent{Body: "invalid"}},
	}}
	_, err = service.BatchCreate(ctx, workspace, invalid, "invalid-bytes")
	assertErrorCode(t, err, "working_title_required")
	if _, err := store.receiptRef(workspace, invalidOperation).Get(ctx); !firestoreNotFound(err) {
		t.Fatalf("validation failure created a receipt: %v", err)
	}
	tooManyOperation := testOperationID()
	_, err = service.BatchCreate(ctx, workspace, batchRequest(tooManyOperation, MaxBatchItems+1, "too-many"), "too-many-bytes")
	assertErrorCode(t, err, "invalid_batch_size")
	if _, err := store.receiptRef(workspace, tooManyOperation).Get(ctx); !firestoreNotFound(err) {
		t.Fatalf("51-item rejection created a receipt: %v", err)
	}
	listed, err = service.List(ctx, workspace, ListQuery{})
	if err != nil || len(listed) != MaxBatchItems+1 {
		t.Fatalf("validation failure changed item count to %d: %v", len(listed), err)
	}

	concurrentOperation := testOperationID()
	concurrentRequest := batchRequest(concurrentOperation, 3, "concurrent")
	const retries = 10
	results := make([]MutationResult, retries)
	errorsByRetry := make([]error, retries)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], errorsByRetry[index] = service.BatchCreate(ctx, workspace, concurrentRequest, "matching-bytes")
		}()
	}
	wait.Wait()
	for index := range results {
		if errorsByRetry[index] != nil || !reflect.DeepEqual(results[index], results[0]) {
			t.Fatalf("concurrent retry %d returned %#v, %v; first %#v", index, results[index], errorsByRetry[index], results[0])
		}
	}
	listed, err = service.List(ctx, workspace, ListQuery{TitlePrefix: "concurrent"})
	if err != nil || len(listed) != 3 {
		t.Fatalf("matching retries stored %d items: %v", len(listed), err)
	}
	_, err = service.BatchCreate(ctx, workspace, concurrentRequest, "conflicting-bytes")
	assertErrorCode(t, err, "operation_id_conflict")

	otherWorkspace := workspace + "-other"
	other, err := service.BatchCreate(ctx, otherWorkspace, concurrentRequest, "conflicting-bytes")
	if err != nil || reflect.DeepEqual(other.ItemIDs, results[0].ItemIDs) {
		t.Fatalf("workspace-scoped operation returned %#v, %v", other, err)
	}
	if _, err := service.Get(ctx, otherWorkspace, results[0].ItemIDs[0]); err == nil {
		t.Fatal("batch content escaped workspace scope")
	}

	now = receiptExpiry
	afterExpiry := BatchRequest{OperationID: fiftyOperation, Items: []BatchItemRequest{{Type: TypeEmail, WorkingTitle: "After expiry", Status: StatusDraft, Content: EmailContent{Body: "new"}}}}
	afterExpiryResult, err := service.BatchCreate(ctx, workspace, afterExpiry, "new-bytes-after-expiry")
	if err != nil || afterExpiryResult.ItemIDs[0] == fifty.ItemIDs[0] {
		t.Fatalf("expired receipt remained binding: %#v, %v", afterExpiryResult, err)
	}

	rollbackOperation := testOperationID()
	firstID := testOperationID()
	existingID := one.ItemIDs[0]
	transactionItems := []Item{
		batchStoredItem(firstID, workspace, "Transaction first", now),
		batchStoredItem(existingID, workspace, "Collision", now),
	}
	rollbackResult := MutationResult{
		OperationID: rollbackOperation,
		ItemIDs:     []string{firstID, existingID},
		Revisions:   []int64{1, 1},
		ExpiresAt:   []time.Time{transactionItems[0].ExpiresAt, transactionItems[1].ExpiresAt},
		Status:      "created",
	}
	rollbackReceipt := newReceipt(workspace, "rollback-bytes", "batch_create", 201, rollbackResult, now)
	_, err = store.BatchCreate(ctx, transactionItems, rollbackReceipt)
	assertErrorCode(t, err, "content_unavailable")
	if _, err := store.Get(ctx, workspace, firstID, now); err == nil {
		t.Fatal("failed transaction wrote its first item")
	}
	if _, err := store.receiptRef(workspace, rollbackOperation).Get(ctx); !firestoreNotFound(err) {
		t.Fatalf("failed transaction created a receipt: %v", err)
	}
}

func batchRequest(operationID string, count int, title string) BatchRequest {
	items := make([]BatchItemRequest, count)
	for index := range items {
		items[index] = BatchItemRequest{Type: TypeX, WorkingTitle: title, Status: StatusDraft, Content: XContent{Body: "post"}}
	}
	return BatchRequest{OperationID: operationID, Items: items}
}

func batchStoredItem(id, workspace, title string, now time.Time) Item {
	item := Item{
		ID: id, WorkspaceID: workspace, Type: TypeX, Status: StatusDraft,
		WorkingTitle: title, NormalizedWorkingTitle: NormalizeTitle(title), Revision: 1,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(ContentLifetime), Content: XContent{Body: "post"},
	}
	item.SearchableWorkingTitle = SearchableTitle(item.NormalizedWorkingTitle)
	return item
}
