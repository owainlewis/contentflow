package content

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestServicePersistsEveryTypeAndReturnsSummaryOnlySearch(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	types := []struct {
		kind    Type
		content any
	}{
		{TypeYouTube, YouTubeContent{Transcript: "spoken", Sections: []Section{}}},
		{TypeLinkedIn, LinkedInContent{Body: "body"}},
		{TypeX, XContent{Body: "body"}},
		{TypeInstagram, InstagramContent{Script: "script"}},
		{TypeTikTok, TikTokContent{Script: "script"}},
		{TypeEmail, EmailContent{Subject: "subject", Body: "body"}},
		{TypeSubstack, SubstackContent{Headline: "headline", Subheadline: "subheadline", Body: "body"}},
	}
	ids := make(map[Type]string)
	for index, test := range types {
		title := "Other"
		if index == 0 {
			title = "ＦＯＯ Guide"
		}
		result, err := service.Create(context.Background(), "workspace-a", CreateRequest{Type: test.kind, WorkingTitle: title, Status: StatusDraft, OperationID: testOperationID(), Content: test.content}, "hash-"+string(test.kind))
		if err != nil {
			t.Fatalf("create %s: %v", test.kind, err)
		}
		ids[test.kind] = result.ItemIDs[0]
	}

	restarted := NewService(store)
	restarted.now = service.now
	for _, test := range types {
		item, err := restarted.Get(context.Background(), "workspace-a", ids[test.kind])
		if err != nil || item.Type != test.kind {
			t.Fatalf("persisted %s item: %#v, %v", test.kind, item, err)
		}
		if _, err := restarted.Get(context.Background(), "workspace-b", ids[test.kind]); err == nil {
			t.Fatalf("%s escaped workspace scope", test.kind)
		}
	}

	items, err := restarted.List(context.Background(), "workspace-a", ListQuery{Type: TypeYouTube, Status: StatusDraft, TitlePrefix: "foo"})
	if err != nil || len(items) != 1 || items[0].ID != ids[TypeYouTube] {
		t.Fatalf("filtered prefix search returned %#v, %v", items, err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"content", "transcript", "sections", "body", "script", "signed"} {
		if strings.Contains(string(encoded), `"`+forbidden+`"`) {
			t.Fatalf("summary leaked %s: %s", forbidden, encoded)
		}
	}
}

func TestFullReplacementSectionsTranscriptConflictsAndReceipts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	createOperation := testOperationID()
	create := CreateRequest{
		Type: TypeYouTube, WorkingTitle: "Video", Status: StatusDraft, OperationID: createOperation,
		Content: YouTubeContent{Transcript: "spoken one", Sections: []Section{{Position: 0, Title: "Intro", Body: "script one"}, {Position: 1, Title: "Body", Body: "script two"}}},
	}
	created, err := service.Create(ctx, "workspace", create, "create-hash")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Create(ctx, "workspace", create, "create-hash")
	if err != nil || replayed.ItemIDs[0] != created.ItemIDs[0] {
		t.Fatalf("create replay: %#v, %v", replayed, err)
	}
	_, err = service.Create(ctx, "workspace", create, "different-bytes")
	assertErrorCode(t, err, "operation_id_conflict")

	id := created.ItemIDs[0]
	item, err := service.Get(ctx, "workspace", id)
	if err != nil {
		t.Fatal(err)
	}
	original := item.Content.(YouTubeContent).Sections
	if original[0].ID == "" || original[1].ID == "" || original[0].ID >= original[1].ID {
		t.Fatalf("section IDs are not stable monotonic ULIDs: %#v", original)
	}
	expiresAt := item.ExpiresAt

	replaceOperation := testOperationID()
	replacement := ReplaceRequest{CreateRequest: CreateRequest{
		Type: TypeYouTube, WorkingTitle: "Video revised", Status: StatusReady, OperationID: replaceOperation,
		Content: YouTubeContent{Transcript: "spoken two", Sections: []Section{{ID: original[1].ID, Position: 0, Title: "Body", Body: "script two"}, {ID: original[0].ID, Position: 1, Title: "Intro", Body: "script one"}, {Position: 2, Title: "End", Body: "script three"}}},
	}, Revision: 1}
	updated, err := service.Replace(ctx, "workspace", id, replacement, "replace-hash")
	if err != nil || updated.Revisions[0] != 2 {
		t.Fatalf("replace: %#v, %v", updated, err)
	}
	item, err = service.Get(ctx, "workspace", id)
	if err != nil {
		t.Fatal(err)
	}
	after := item.Content.(YouTubeContent)
	if after.Sections[0].ID != original[1].ID || after.Sections[1].ID != original[0].ID || after.Sections[2].ID == "" {
		t.Fatalf("replacement lost section identities: %#v", after.Sections)
	}
	if after.Sections[0].Body != "script two" || after.Transcript != "spoken two" {
		t.Fatalf("transcript and script changed incorrectly: %#v", after)
	}
	if !item.ExpiresAt.Equal(expiresAt) {
		t.Fatal("replacement extended expiry")
	}

	stale := replacement
	stale.OperationID = testOperationID()
	_, err = service.Replace(ctx, "workspace", id, stale, "stale")
	contentError, ok := err.(*Error)
	if !ok || contentError.Code != "revision_conflict" || contentError.Current == nil || contentError.Current.Revision != 2 {
		t.Fatalf("stale replacement returned %v", err)
	}

	transcript, err := service.Transcript(ctx, "workspace", id)
	if err != nil || transcript.Transcript != "spoken two" {
		t.Fatalf("transcript endpoint: %#v, %v", transcript, err)
	}
	clear := ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Video revised", Status: StatusReady, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "\u2003\n", Sections: after.Sections}}, Revision: 2}
	if _, err := service.Replace(ctx, "workspace", id, clear, "clear"); err != nil {
		t.Fatal(err)
	}
	_, err = service.Transcript(ctx, "workspace", id)
	assertErrorCode(t, err, "transcript_missing")
	cleared, err := service.Get(ctx, "workspace", id)
	if err != nil {
		t.Fatal(err)
	}
	clearedSections := cleared.Content.(YouTubeContent).Sections
	for index := range after.Sections {
		if clearedSections[index].ID != after.Sections[index].ID || clearedSections[index].Body != after.Sections[index].Body {
			t.Fatalf("clearing transcript changed sections")
		}
	}

	archiveOperation := testOperationID()
	archived, err := service.SetArchived(ctx, "workspace", id, RevisionRequest{OperationID: archiveOperation, Revision: 3}, true, "archive")
	if err != nil || archived.Revisions[0] != 4 {
		t.Fatalf("archive: %#v, %v", archived, err)
	}
	archivedItem, err := service.Get(ctx, "workspace", id)
	if err != nil || archivedItem.ArchivedAt == nil || !archivedItem.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("archive changed expiry: %#v, %v", archivedItem, err)
	}
	now = expiresAt
	if _, err := service.Get(ctx, "workspace", id); err == nil {
		t.Fatal("expired item remained readable at its deadline")
	}
}

func TestReplacementClearsOmittedOptionalFieldsAndDeleteIsRevisionAware(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore())
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created, err := service.Create(ctx, "workspace", CreateRequest{Type: TypeEmail, WorkingTitle: "Email", Status: StatusDraft, OperationID: testOperationID(), Content: EmailContent{Subject: "Subject", Body: "Body"}}, "create")
	if err != nil {
		t.Fatal(err)
	}
	id := created.ItemIDs[0]
	raw := []byte(`{"type":"email","working_title":"Email","status":"ready","operation_id":"` + testOperationID() + `","revision":1,"content":{}}`)
	replacement, err := DecodeReplace(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Replace(ctx, "workspace", id, replacement, "replace"); err != nil {
		t.Fatal(err)
	}
	item, err := service.Get(ctx, "workspace", id)
	if err != nil {
		t.Fatal(err)
	}
	email := item.Content.(EmailContent)
	if email.Subject != "" || email.Body != "" {
		t.Fatalf("omitted fields were not cleared: %#v", email)
	}
	_, err = service.Delete(ctx, "workspace", id, RevisionRequest{OperationID: testOperationID(), Revision: 1}, "stale-delete")
	assertErrorCode(t, err, "revision_conflict")
	deleted, err := service.Delete(ctx, "workspace", id, RevisionRequest{OperationID: testOperationID(), Revision: 2}, "delete")
	if err != nil || deleted.Status != "deleted" {
		t.Fatalf("delete: %#v, %v", deleted, err)
	}
	if _, err := service.Get(ctx, "workspace", id); err == nil {
		t.Fatal("deleted item remained readable")
	}
}

func TestStaleReplacementWithRemovedSectionReturnsRevisionConflict(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore())
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created, err := service.Create(ctx, "workspace", CreateRequest{Type: TypeYouTube, WorkingTitle: "Video", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "spoken", Sections: []Section{{Position: 0, Title: "Removed", Body: "old"}}}}, "create")
	if err != nil {
		t.Fatal(err)
	}
	id := created.ItemIDs[0]
	item, err := service.Get(ctx, "workspace", id)
	if err != nil {
		t.Fatal(err)
	}
	removed := item.Content.(YouTubeContent).Sections[0]
	if _, err := service.Replace(ctx, "workspace", id, ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Video", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "spoken", Sections: []Section{}}}, Revision: 1}, "remove"); err != nil {
		t.Fatal(err)
	}
	stale := ReplaceRequest{CreateRequest: CreateRequest{Type: TypeYouTube, WorkingTitle: "Video", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: "stale", Sections: []Section{removed}}}, Revision: 1}
	_, err = service.Replace(ctx, "workspace", id, stale, "stale")
	contentError, ok := err.(*Error)
	if !ok || contentError.Code != "revision_conflict" || contentError.Current == nil || contentError.Current.Revision != 2 {
		t.Fatalf("stale removed section returned %v", err)
	}
}
