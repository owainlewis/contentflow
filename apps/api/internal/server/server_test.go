package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/owainlewis/contentflow/apps/api/internal/auth"
)

type fakeChecker map[string]error

func (f fakeChecker) Check(context.Context) map[string]error {
	return f
}

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":    {Data: []byte(`<!doctype html><title>ContentFlow</title><meta property="og:image" content="__CONTENTFLOW_SOCIAL_IMAGE__"><meta name="twitter:image" content="__CONTENTFLOW_SOCIAL_IMAGE__"><div id=root></div>`)},
		"assets/app.js": {Data: []byte("console.log('ContentFlow')")},
	}
}

func TestHealthEndpointsReportLiveAndDependencyState(t *testing.T) {
	t.Parallel()

	api := NewAPI(fakeChecker{"assets": nil, "firestore": io.EOF}, nil)
	application := NewApplication(testAssets(), api)

	live := httptest.NewRecorder()
	application.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"status":"live"`) {
		t.Fatalf("unexpected live response: %d %s", live.Code, live.Body.String())
	}

	ready := httptest.NewRecorder()
	application.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"firestore":"unavailable"`) {
		t.Fatalf("unexpected ready response: %d %s", ready.Code, ready.Body.String())
	}
}

func TestApplicationServesAssetsAndFallsBackForSPARoutes(t *testing.T) {
	t.Parallel()

	application := NewApplication(testAssets(), NewAPI(fakeChecker{"assets": nil}, nil))

	for _, route := range []string{"/", "/content/01ABC"} {
		response := httptest.NewRecorder()
		application.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ContentFlow") {
			t.Fatalf("route %s did not serve the SPA: %d %s", route, response.Code, response.Body.String())
		}
	}

	for _, route := range []string{"/assets/missing.js", "/assets/missing", "/assets"} {
		missingAsset := httptest.NewRecorder()
		application.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, route, nil))
		if missingAsset.Code != http.StatusNotFound {
			t.Fatalf("missing static route %s returned %d", route, missingAsset.Code)
		}
	}

	apiRoute := httptest.NewRecorder()
	application.ServeHTTP(apiRoute, httptest.NewRequest(http.MethodGet, "/api/v1/content", nil))
	if apiRoute.Code != http.StatusNotFound || !strings.Contains(apiRoute.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("API route fell through to the SPA: %d %s", apiRoute.Code, apiRoute.Body.String())
	}

	invalidHealth := httptest.NewRecorder()
	application.ServeHTTP(invalidHealth, httptest.NewRequest(http.MethodGet, "/health/ready/", nil))
	if invalidHealth.Code != http.StatusNotFound || !strings.Contains(invalidHealth.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("invalid health route fell through to the SPA: %d %s", invalidHealth.Code, invalidHealth.Body.String())
	}
}

func TestApplicationInjectsOriginQualifiedSocialImages(t *testing.T) {
	t.Parallel()

	application := NewApplication(testAssets(), NewAPI(fakeChecker{"assets": nil}, nil))
	request := httptest.NewRequest(http.MethodGet, "http://internal/content/01ABC", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "contentflow.example")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)

	const socialImage = "https://contentflow.example/og.png"
	if response.Code != http.StatusOK {
		t.Fatalf("nested SPA route returned %d", response.Code)
	}
	if count := strings.Count(response.Body.String(), socialImage); count != 2 {
		t.Fatalf("expected both social image tags to use %s, found %d", socialImage, count)
	}
	if strings.Contains(response.Body.String(), socialImagePlaceholder) {
		t.Fatal("social image placeholder was not replaced")
	}
}

func TestPrivateLocalAPIRequiresGeneratedProxySecret(t *testing.T) {
	t.Parallel()

	private := RequireProxySecret(NewAPI(fakeChecker{"assets": nil}, nil), "generated-secret")

	unauthorized := httptest.NewRecorder()
	private.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("direct private request returned %d", unauthorized.Code)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	authorizedRequest.Header.Set(proxySecretHeader, "generated-secret")
	authorized := httptest.NewRecorder()
	private.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authenticated proxy request returned %d", authorized.Code)
	}
}

func TestLocalPublicApplicationInjectsProxySecret(t *testing.T) {
	t.Parallel()

	private := httptest.NewServer(RequireProxySecret(NewAPI(fakeChecker{"assets": nil}, nil), "generated-secret"))
	defer private.Close()
	privateURL, err := url.Parse(private.URL)
	if err != nil {
		t.Fatal(err)
	}
	public := NewLocalPublicApplication(testAssets(), privateURL, "generated-secret")

	response := httptest.NewRecorder()
	public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("public proxy returned %d: %s", response.Code, response.Body.String())
	}
}

type serverFakeOAuth struct{}

func (serverFakeOAuth) AuthorizationURL(state, challenge string) string {
	return "https://accounts.example/authorize?state=" + url.QueryEscape(state) + "&code_challenge=" + url.QueryEscape(challenge)
}

func (serverFakeOAuth) ExchangeIdentity(context.Context, string, string) (auth.Identity, error) {
	return auth.Identity{Issuer: "https://accounts.google.com", Subject: "owner"}, nil
}

func TestConfiguredAPIRequiresAuthenticationBeforeWorkspaceRoutes(t *testing.T) {
	service, err := auth.New(auth.Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
		WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
	}, serverFakeOAuth{}, auth.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(fakeChecker{"assets": nil}, service)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/content", nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "authentication_required") {
		t.Fatalf("unauthenticated API returned %d: %s", response.Code, response.Body.String())
	}
}

func TestOAuthCallbackAcceptsFormPostWithoutQueryParameters(t *testing.T) {
	service, err := auth.New(auth.Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
		WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
	}, serverFakeOAuth{}, auth.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(fakeChecker{"assets": nil}, service)
	loginResponse := httptest.NewRecorder()
	api.ServeHTTP(loginResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
	location, err := url.Parse(loginResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	loginCookies := loginResponse.Result().Cookies()
	if len(loginCookies) == 0 {
		t.Fatal("login cookie missing")
	}
	form := url.Values{"code": {"authorization-code"}, "state": {location.Query().Get("state")}}
	callback := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", strings.NewReader(form.Encode()))
	callback.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	callback.AddCookie(loginCookies[0])
	response := httptest.NewRecorder()
	api.ServeHTTP(response, callback)
	if response.Code != http.StatusFound || callback.URL.RawQuery != "" {
		t.Fatalf("form-post callback returned %d with query %q: %s", response.Code, callback.URL.RawQuery, response.Body.String())
	}
}

func TestRequestLogsExcludeCredentialsAndSensitiveContent(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	api := NewAPI(fakeChecker{"assets": nil}, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/content?code=oauth-code&state=oauth-state", strings.NewReader(`{"transcript":"private transcript","body":"private body","upload_url":"https://signed.example/secret"}`))
	request.Header.Set("Authorization", "Bearer raw-api-token")
	request.Header.Set("Cookie", "contentflow_session=raw-session")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)

	logged := logs.String()
	for _, secret := range []string{"oauth-code", "oauth-state", "private transcript", "private body", "signed.example", "raw-api-token", "raw-session"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("request log leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, `method=POST`) || !strings.Contains(logged, `path=/api/v1/content`) {
		t.Fatalf("safe request metadata missing from log: %s", logged)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	store := auth.NewMemoryStore()
	service, err := auth.New(auth.Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
		WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
	}, serverFakeOAuth{}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), auth.Session{ID: "expired", WorkspaceID: "workspace", CSRFToken: "csrf", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: "contentflow_session", Value: "expired"})
	response := httptest.NewRecorder()
	NewAPI(fakeChecker{"assets": nil}, service).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired session returned %d", response.Code)
	}
}

func TestTokenRevocationBlocksTheNextHTTPAPIRequest(t *testing.T) {
	store := auth.NewMemoryStore()
	service, err := auth.New(auth.Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
		WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
	}, serverFakeOAuth{}, store)
	if err != nil {
		t.Fatal(err)
	}
	session := auth.Session{ID: "owner-session", WorkspaceID: "workspace", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(fakeChecker{"assets": nil}, service)

	create := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"scopes":["content:read"]}`))
	create.AddCookie(&http.Cookie{Name: "contentflow_session", Value: session.ID})
	create.Header.Set("Origin", "https://contentflow.example")
	create.Header.Set("X-CSRF-Token", session.CSRFToken)
	created := httptest.NewRecorder()
	api.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create token returned %d: %s", created.Code, created.Body.String())
	}
	var token struct {
		ID  string `json:"id"`
		Raw string `json:"token"`
	}
	if err := json.NewDecoder(created.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}

	revoke := httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+token.ID, nil)
	revoke.AddCookie(&http.Cookie{Name: "contentflow_session", Value: session.ID})
	revoke.Header.Set("Origin", "https://contentflow.example")
	revoke.Header.Set("X-CSRF-Token", session.CSRFToken)
	revoked := httptest.NewRecorder()
	api.ServeHTTP(revoked, revoke)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke token returned %d: %s", revoked.Code, revoked.Body.String())
	}

	next := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	next.Header.Set("Authorization", "Bearer "+token.Raw)
	nextResponse := httptest.NewRecorder()
	api.ServeHTTP(nextResponse, next)
	if nextResponse.Code != http.StatusUnauthorized || !strings.Contains(nextResponse.Body.String(), "invalid_bearer_token") {
		t.Fatalf("revoked token's next request returned %d: %s", nextResponse.Code, nextResponse.Body.String())
	}
}
