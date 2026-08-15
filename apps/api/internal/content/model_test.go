package content

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
)

func testOperationID() string { return ulid.Make().String() }

func TestDecodeCreateAcceptsEveryTypedPayloadAndRejectsMismatches(t *testing.T) {
	tests := []struct {
		contentType Type
		body        string
	}{
		{TypeYouTube, `{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[]}`},
		{TypeLinkedIn, `{"body":"post"}`},
		{TypeX, `{"body":"post"}`},
		{TypeInstagram, `{"script":"script"}`},
		{TypeTikTok, `{"script":"script"}`},
		{TypeEmail, `{"subject":"subject","body":"body"}`},
		{TypeSubstack, `{"headline":"headline","subheadline":"subheadline","body":"body"}`},
	}
	for _, test := range tests {
		t.Run(string(test.contentType), func(t *testing.T) {
			raw := []byte(`{"type":"` + string(test.contentType) + `","working_title":"Title","status":"draft","operation_id":"` + testOperationID() + `","content":` + test.body + `}`)
			request, err := DecodeCreate(raw)
			if err != nil {
				t.Fatalf("decode %s: %v", test.contentType, err)
			}
			if request.Type != test.contentType {
				t.Fatalf("decoded type %q", request.Type)
			}
		})
	}

	invalid := []struct{ name, raw, code string }{
		{"mismatch", `{"type":"email","working_title":"Title","status":"draft","operation_id":"` + testOperationID() + `","content":{"script":"wrong"}}`, "invalid_content"},
		{"unknown discriminator", `{"type":"blog","working_title":"Title","status":"draft","operation_id":"` + testOperationID() + `","content":{"body":"wrong"}}`, "invalid_discriminator"},
		{"unknown typed field", `{"type":"x","working_title":"Title","status":"draft","operation_id":"` + testOperationID() + `","content":{"body":"post","subject":"wrong"}}`, "invalid_content"},
		{"server expiry", `{"type":"x","working_title":"Title","status":"draft","operation_id":"` + testOperationID() + `","expires_at":"2099-01-01T00:00:00Z","content":{"body":"post"}}`, "invalid_request"},
		{"missing transcript", `{"type":"youtube","working_title":"Title","status":"draft","operation_id":"` + testOperationID() + `","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","sections":[]}}`, "transcript_required"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeCreate([]byte(test.raw))
			assertErrorCode(t, err, test.code)
		})
	}
}

func TestTextAndEncodedDocumentLimits(t *testing.T) {
	maxText := strings.Repeat("x", MaxTextBytes)
	request := CreateRequest{Type: TypeYouTube, WorkingTitle: "Title", Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: maxText, Sections: []Section{}}}
	if err := validateRequest(request); err != nil {
		t.Fatalf("500 KiB field was rejected: %v", err)
	}

	request.Content = YouTubeContent{Transcript: maxText + "x", Sections: []Section{}}
	assertErrorCode(t, validateRequest(request), "text_field_too_large")

	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	large := strings.Repeat("x", 460<<10)
	item := Item{
		ID: testOperationID(), WorkspaceID: "workspace", Type: TypeYouTube, Status: StatusDraft,
		WorkingTitle: "Title", NormalizedWorkingTitle: "title", Revision: 1,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(ContentLifetime),
		Content: YouTubeContent{Description: large, Transcript: large, Sections: []Section{}},
	}
	assertErrorCode(t, validateEncodedSizes(item), "content_document_too_large")

	item.Content = YouTubeContent{Transcript: strings.Repeat("x", 400<<10), Sections: []Section{{ID: testOperationID(), Position: 0, Title: maxText, Body: maxText}}}
	if err := validateEncodedSizes(item); err != nil {
		t.Fatalf("valid bounded parent and section were rejected: %v", err)
	}
	size, err := encodedFirestoreSize(sectionDocument{Position: 0, Title: maxText, Body: maxText, WorkspaceID: "workspace", ExpiresAt: item.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if size >= MaxFirestoreBytes {
		t.Fatalf("text limits produced an oversized section: %d", size)
	}
}

func TestTitleNormalizationAndMonotonicIDs(t *testing.T) {
	if got := NormalizeTitle("ＦＯＯ Straße"); got != "foo strasse" {
		t.Fatalf("normalized title is %q", got)
	}
	generator := newIDGenerator()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	first, err := generator.New(now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.New(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("IDs are not monotonic: %s then %s", first, second)
	}
}

func TestSearchableTitleUsesSafeUTF8BoundaryAndTrueSuccessor(t *testing.T) {
	longASCII := strings.Repeat("a", MaxIndexedStringBytes) + "tail"
	if got := SearchableTitle(longASCII); got != strings.Repeat("a", MaxIndexedStringBytes) {
		t.Fatalf("ASCII searchable title has %d bytes", len(got))
	}
	multibyte := strings.Repeat("a", MaxIndexedStringBytes-1) + "😀tail"
	if got := SearchableTitle(multibyte); got != strings.Repeat("a", MaxIndexedStringBytes-1) {
		t.Fatalf("multibyte searchable title split a rune: %q", got[len(got)-4:])
	}
	if successor, found := titlePrefixSuccessor("foo" + string(unicode.MaxRune)); !found || successor != "fop" {
		t.Fatalf("max-rune successor is %q, %v", successor, found)
	}
	if successor, found := titlePrefixSuccessor(string(unicode.MaxRune)); found || successor != "" {
		t.Fatalf("all-max prefix has successor %q", successor)
	}
	if successor, found := titlePrefixSuccessor(string(rune(0xD7FF))); !found || successor != string(rune(0xE000)) {
		t.Fatalf("successor entered surrogate range: %q, %v", successor, found)
	}
	widthBoundary := strings.Repeat("a", MaxIndexedStringBytes-1) + string(rune(0x7F))
	successor, found := titlePrefixSuccessor(widthBoundary)
	if !found || len(successor) >= 1500 {
		t.Fatalf("UTF-8 width successor has %d bytes, found=%v", len(successor), found)
	}
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	contentError, ok := err.(*Error)
	if !ok || contentError.Code != code {
		t.Fatalf("error is %v, want %s", err, code)
	}
}
