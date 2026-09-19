package content

import (
	"context"
	"testing"
	"time"
)

func exerciseTopicLifecycle(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	service := NewService(store)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	create := func(workspace string, req CreateRequest) (MutationResult, error) {
		req.OperationID = testOperationID()
		req.Status = StatusDraft
		return service.Create(ctx, workspace, req, req.OperationID)
	}
	group, err := create("topics", CreateRequest{Type: TypeTopic, WorkingTitle: "Code review", Content: TopicContent{Source: "One idea", SourceURL: "https://docs.google.com/document/d/source"}})
	if err != nil {
		t.Fatal(err)
	}
	groupID := group.ItemIDs[0]
	req := CreateRequest{Type: TypeInstagram, TopicID: groupID, Format: "reel", DocumentURL: "https://docs.google.com/document/d/script", VideoURL: "https://app.frame.io/review/video", Content: InstagramContent{Script: "Opening", Caption: "Caption"}}
	piece, err := create("topics", req)
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.Get(ctx, "topics", piece.ItemIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if item.TopicID != groupID || item.Format != "reel" || item.VideoURL != req.VideoURL || item.DocumentURL != req.DocumentURL || item.Content.(InstagramContent).Caption != "Caption" {
		t.Fatalf("resource failed roundtrip: %#v", item)
	}
	summary := item.Summary()
	if summary.TopicID != groupID || summary.Format != "reel" {
		t.Fatal("summary lost group or format")
	}
	if _, err := create("other", req); err == nil {
		t.Fatal("cross-workspace topic accepted")
	}
	req.TopicID = piece.ItemIDs[0]
	if _, err := create("topics", req); err == nil {
		t.Fatal("piece accepted as topic")
	}
	req.TopicID = testOperationID()
	if _, err := create("topics", req); err == nil {
		t.Fatal("dangling topic accepted")
	}

	req.OperationID = testOperationID()
	req.Status = StatusDraft
	if _, err := service.Replace(ctx, "topics", item.ID, ReplaceRequest{CreateRequest: req, Revision: 1}, "invalid-replace"); err == nil {
		t.Fatal("replacement accepted dangling topic")
	}
	before, _ := service.List(ctx, "topics", ListQuery{})
	_, err = service.BatchCreate(ctx, "topics", BatchRequest{OperationID: testOperationID(), Items: []BatchItemRequest{
		{Type: TypeX, Status: StatusDraft, Content: XContent{Body: "valid standalone"}},
		{Type: TypeX, Status: StatusDraft, TopicID: req.TopicID, Content: XContent{Body: "invalid group"}},
	}}, "invalid-batch")
	if err == nil {
		t.Fatal("batch accepted dangling topic")
	}
	after, _ := service.List(ctx, "topics", ListQuery{})
	if len(before) != len(after) {
		t.Fatal("invalid batch partially persisted")
	}
	if _, err := service.Delete(ctx, "topics", groupID, RevisionRequest{OperationID: testOperationID(), Revision: 1}, "delete-nonempty"); err == nil {
		t.Fatal("nonempty topic deleted")
	}
	now = now.AddDate(2, 0, 0)
	if _, err := service.Get(ctx, "topics", item.ID); err != nil {
		t.Fatalf("content expired: %v", err)
	}
	req.TopicID = ""
	req.OperationID = testOperationID()
	req.Status = StatusReady
	if _, err := service.Replace(ctx, "topics", item.ID, ReplaceRequest{CreateRequest: req, Revision: 1}, "detach"); err != nil {
		t.Fatal(err)
	}
	item, err = service.Get(ctx, "topics", item.ID)
	if err != nil || item.TopicID != "" {
		t.Fatalf("detach failed: %#v %v", item, err)
	}
	if _, err := service.Delete(ctx, "topics", groupID, RevisionRequest{OperationID: testOperationID(), Revision: 1}, "delete-empty"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryTopicLifecycle(t *testing.T) { exerciseTopicLifecycle(t, NewMemoryStore()) }
func TestPostgresTopicLifecycle(t *testing.T) {
	store, _ := newPostgresStore(t)
	exerciseTopicLifecycle(t, store)
}

func TestTopicResourceRequestValidation(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,test", "file:///etc/passwd", "//frame.io/video", "https://user:pass@frame.io/video"} {
		request := CreateRequest{Type: TypeInstagram, Status: StatusDraft, OperationID: testOperationID(), Content: InstagramContent{}, VideoURL: bad}
		if err := validateRequest(request); err == nil {
			t.Errorf("accepted unsafe link %q", bad)
		}
	}
	now := time.Now()
	for _, request := range []CreateRequest{
		{Type: TypeTopic, TopicID: testOperationID(), Content: TopicContent{}},
		{Type: TypeTopic, ScheduledAt: &now, Content: TopicContent{}},
		{Type: TypeTopic, Content: TopicContent{SourceURL: "javascript:alert(1)"}},
	} {
		request.OperationID = testOperationID()
		request.Status = StatusDraft
		if err := validateRequest(request); err == nil {
			t.Errorf("accepted invalid topic %#v", request)
		}
	}
	raw := []byte(`{"type":"instagram","status":"draft","operation_id":"` + testOperationID() + `","topic_id":"` + testOperationID() + `","format":"reel","video_url":"https://frame.io/video","document_url":"https://docs.google.com/document/d/script","content":{"script":"hello","caption":"caption"}}`)
	request, err := DecodeCreate(raw)
	if err != nil || request.Format != "reel" || request.TopicID == "" || request.Content.(InstagramContent).Caption != "caption" {
		t.Fatalf("decode failed: %#v %v", request, err)
	}
}
