package content_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/content"
	"github.com/owainlewis/contentflow/apps/api/internal/server"
)

type httpChecker struct{}

func (httpChecker) Check(context.Context) map[string]error { return map[string]error{} }

type httpOAuth struct{}

func (httpOAuth) AuthorizationURL(string, string) string { return "https://accounts.example/authorize" }
func (httpOAuth) ExchangeIdentity(context.Context, string, string) (auth.Identity, error) {
	return auth.Identity{}, nil
}

func newContentAPI(t *testing.T) (http.Handler, string) {
	t.Helper()
	store := auth.NewMemoryStore()
	rawToken := "cf_http_test_token"
	hash := sha256.Sum256([]byte(rawToken))
	if err := store.SaveToken(t.Context(), auth.Token{ID: ulid.Make().String(), WorkspaceID: "workspace", Prefix: "cf_http_test", Hash: hash, Scopes: []auth.Scope{auth.ScopeContentRead, auth.ScopeContentWrite}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	authentication, err := auth.New(auth.Config{PublicOrigin: "https://contentflow.example", OwnerIssuer: "issuer", OwnerSubject: "owner", WorkspaceID: "workspace", CredentialKey: make([]byte, 32)}, httpOAuth{}, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := content.NewHTTPHandler(content.NewService(content.NewMemoryStore()))
	return server.NewAPIWithContent(httpChecker{}, authentication, handler), rawToken
}

func performAPI(api http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func TestAuthenticatedHTTPContentContract(t *testing.T) {
	api, token := newContentAPI(t)
	operation := ulid.Make().String()
	createBody := `{"type":"youtube","working_title":"Ｆｏｏ Video","status":"draft","operation_id":"` + operation + `","content":{"topic":"topic","icp":"icp","angle":"angle","cta":"cta","publishing_title":"publish","description":"description","transcript":"","sections":[{"position":0,"title":"Intro","body":"script"}]}}`
	created := performAPI(api, token, http.MethodPost, "/api/v1/content", createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", created.Code, created.Body.String())
	}
	createdBody := created.Body.String()
	var result content.MutationResult
	if err := json.Unmarshal([]byte(createdBody), &result); err != nil {
		t.Fatal(err)
	}
	id := result.ItemIDs[0]

	replay := performAPI(api, token, http.MethodPost, "/api/v1/content", createBody)
	if replay.Code != http.StatusCreated || replay.Body.String() != createdBody {
		t.Fatalf("exact replay returned %d %s, want %s", replay.Code, replay.Body.String(), createdBody)
	}
	differentBytes := performAPI(api, token, http.MethodPost, "/api/v1/content", strings.Replace(createBody, `"topic":"topic"`, `"topic":"changed"`, 1))
	if differentBytes.Code != http.StatusConflict || !strings.Contains(differentBytes.Body.String(), "operation_id_conflict") {
		t.Fatalf("different-byte replay returned %d: %s", differentBytes.Code, differentBytes.Body.String())
	}

	missing := performAPI(api, token, http.MethodGet, "/api/v1/content/"+id+"/transcript", "")
	if missing.Code != http.StatusConflict || !strings.Contains(missing.Body.String(), "transcript_missing") {
		t.Fatalf("empty transcript returned %d: %s", missing.Code, missing.Body.String())
	}

	itemResponse := performAPI(api, token, http.MethodGet, "/api/v1/content/"+id, "")
	if itemResponse.Code != http.StatusOK {
		t.Fatalf("get returned %d: %s", itemResponse.Code, itemResponse.Body.String())
	}
	var item map[string]any
	if err := json.NewDecoder(itemResponse.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	sections := item["content"].(map[string]any)["sections"].([]any)
	sectionID := sections[0].(map[string]any)["id"].(string)

	replaceOperation := ulid.Make().String()
	replaceBody := `{"type":"youtube","working_title":"Foo Video","status":"ready","operation_id":"` + replaceOperation + `","revision":1,"content":{"topic":"topic","icp":"icp","angle":"angle","cta":"cta","publishing_title":"publish","description":"description","transcript":"canonical transcript","sections":[{"id":"` + sectionID + `","position":0,"title":"Intro","body":"script"}]}}`
	replaced := performAPI(api, token, http.MethodPut, "/api/v1/content/"+id, replaceBody)
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace returned %d: %s", replaced.Code, replaced.Body.String())
	}
	staleBody := strings.Replace(strings.Replace(replaceBody, replaceOperation, ulid.Make().String(), 1), `"transcript":"canonical transcript"`, `"transcript":"stale"`, 1)
	stale := performAPI(api, token, http.MethodPut, "/api/v1/content/"+id, staleBody)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"revision":2`) || !strings.Contains(stale.Body.String(), "revision_conflict") {
		t.Fatalf("stale replacement returned %d: %s", stale.Code, stale.Body.String())
	}

	transcript := performAPI(api, token, http.MethodGet, "/api/v1/content/"+id+"/transcript", "")
	if transcript.Code != http.StatusOK {
		t.Fatalf("transcript returned %d: %s", transcript.Code, transcript.Body.String())
	}
	for _, forbidden := range []string{"sections", "description", "topic", "working_title"} {
		if strings.Contains(transcript.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("transcript response leaked %s: %s", forbidden, transcript.Body.String())
		}
	}

	list := performAPI(api, token, http.MethodGet, "/api/v1/content?type=youtube&status=ready&q=foo", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), id) {
		t.Fatalf("list returned %d: %s", list.Code, list.Body.String())
	}
	for _, forbidden := range []string{"content", "transcript", "sections", "description", "script"} {
		if strings.Contains(list.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("summary response leaked %s: %s", forbidden, list.Body.String())
		}
	}

	deleteWithToken := performAPI(api, token, http.MethodDelete, "/api/v1/content/"+id, `{"operation_id":"`+ulid.Make().String()+`","revision":2}`)
	if deleteWithToken.Code != http.StatusForbidden || !strings.Contains(deleteWithToken.Body.String(), "owner_session_required") {
		t.Fatalf("token delete returned %d: %s", deleteWithToken.Code, deleteWithToken.Body.String())
	}
}

func TestHTTPRejectsDiscriminatorsAndEveryByteLimit(t *testing.T) {
	api, token := newContentAPI(t)
	cases := []struct {
		name, body, code string
		status           int
	}{
		{"mismatch", `{"type":"email","working_title":"Title","status":"draft","operation_id":"` + ulid.Make().String() + `","content":{"script":"wrong"}}`, "invalid_content", http.StatusBadRequest},
		{"omitted transcript", `{"type":"youtube","working_title":"Title","status":"draft","operation_id":"` + ulid.Make().String() + `","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","sections":[]}}`, "transcript_required", http.StatusBadRequest},
		{"client expiry", `{"type":"x","working_title":"Title","status":"draft","operation_id":"` + ulid.Make().String() + `","expires_at":"2099-01-01T00:00:00Z","content":{"body":"post"}}`, "invalid_request", http.StatusBadRequest},
		{"text field", `{"type":"x","working_title":"Title","status":"draft","operation_id":"` + ulid.Make().String() + `","content":{"body":"` + strings.Repeat("x", content.MaxTextBytes+1) + `"}}`, "text_field_too_large", http.StatusRequestEntityTooLarge},
		{"parent document", `{"type":"youtube","working_title":"Title","status":"draft","operation_id":"` + ulid.Make().String() + `","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"` + strings.Repeat("x", 460<<10) + `","transcript":"` + strings.Repeat("y", 460<<10) + `","sections":[]}}`, "content_document_too_large", http.StatusRequestEntityTooLarge},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := performAPI(api, token, http.MethodPost, "/api/v1/content", test.body)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	overRequestLimit := `{"padding":"` + strings.Repeat("x", content.MaxRequestBytes) + `"}`
	response := performAPI(api, token, http.MethodPost, "/api/v1/content", overRequestLimit)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "request_too_large") {
		t.Fatalf("oversized request returned %d: %s", response.Code, response.Body.String())
	}
}

func TestContentRoutesRequireIdentity(t *testing.T) {
	api, _ := newContentAPI(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unauthenticated list returned %d: %s", response.Code, body)
	}
}
