package content_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/content"
	"github.com/owainlewis/contentflow/apps/api/internal/server"
)

func TestThumbnailHTTPAuthorizationAndLifecycle(t *testing.T) {
	authStore := auth.NewMemoryStore()
	for token, scopes := range map[string][]auth.Scope{"all": {auth.ScopeContentRead, auth.ScopeContentWrite, auth.ScopeAssetsWrite}, "read": {auth.ScopeContentRead}, "write": {auth.ScopeContentWrite}, "assets": {auth.ScopeAssetsWrite}} {
		hash := sha256.Sum256([]byte(token))
		if err := authStore.SaveToken(t.Context(), auth.Token{ID: ulid.Make().String(), WorkspaceID: "workspace", Hash: hash, Scopes: scopes, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	authentication, err := auth.New(auth.Config{PublicOrigin: "https://contentflow.example", OwnerIssuer: "issuer", OwnerSubject: "owner", WorkspaceID: "workspace", CredentialKey: make([]byte, 32)}, httpOAuth{}, authStore)
	if err != nil {
		t.Fatal(err)
	}
	api := server.NewAPIWithContent(httpChecker{}, authentication, content.NewHTTPHandler(content.NewService(content.NewMemoryStore())))
	created := performAPI(api, "all", "POST", "/api/v1/content", `{"type":"youtube","status":"draft","operation_id":"`+ulid.Make().String()+`","content":{"transcript":""}}`)
	var result content.MutationResult
	if err = json.Unmarshal(created.Body.Bytes(), &result); err != nil || len(result.ItemIDs) != 1 {
		t.Fatalf("create %s", created.Body.String())
	}
	path := "/api/v1/content/" + result.ItemIDs[0] + "/thumbnail"
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 16, 9)))
	request := func(token, method string, data []byte, mime string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", mime)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, test := range []struct {
		token, method string
		status        int
	}{{"", "GET", 401}, {"read", "PUT", 403}, {"write", "PUT", 403}, {"assets", "GET", 403}, {"read", "GET", 404}} {
		if r := request(test.token, test.method, imageData.Bytes(), "image/png"); r.Code != test.status {
			t.Fatalf("%s %s got %d %s", test.token, test.method, r.Code, r.Body.String())
		}
	}
	if r := request("assets", "PUT", imageData.Bytes(), "image/png"); r.Code != 200 {
		t.Fatalf("upload %d %s", r.Code, r.Body.String())
	}
	r := request("read", "GET", nil, "")
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || r.Header().Get("ETag") == "" || r.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("get %d %#v", r.Code, r.Header())
	}
	for _, test := range []struct {
		data   []byte
		mime   string
		status int
	}{{[]byte("<svg></svg>"), "image/svg+xml", 400}, {[]byte("garbage"), "image/png", 400}, {make([]byte, content.MaxThumbnailBytes+1), "image/png", 413}} {
		if r := request("assets", "PUT", test.data, test.mime); r.Code != test.status {
			t.Fatalf("invalid upload got %d", r.Code)
		}
	}
	if r := request("read", "GET", nil, ""); r.Code != 200 {
		t.Fatal("failed upload destroyed thumbnail")
	}
	if r := request("read", "DELETE", nil, ""); r.Code != 403 {
		t.Fatal("read token deleted image")
	}
	if r := request("assets", "DELETE", nil, ""); r.Code != 204 {
		t.Fatal("delete failed")
	}
	if r := request("read", "GET", nil, ""); r.Code != 404 {
		t.Fatal("deleted image readable")
	}
}
