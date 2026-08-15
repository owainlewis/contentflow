package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

type fakeProvider struct {
	identity  Identity
	verifier  string
	challenge string
}

func (p *fakeProvider) AuthorizationURL(state, challenge string) string {
	p.challenge = challenge
	return "https://accounts.example/authorize?state=" + url.QueryEscape(state) + "&code_challenge=" + url.QueryEscape(challenge) + "&code_challenge_method=S256"
}

func (p *fakeProvider) ExchangeIdentity(_ context.Context, _ string, verifier string) (Identity, error) {
	p.verifier = verifier
	return p.identity, nil
}

func newTestService(t *testing.T, identity Identity) (*Service, *MemoryStore, *fakeProvider) {
	t.Helper()
	provider := &fakeProvider{identity: identity}
	store := NewMemoryStore()
	service, err := New(Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "https://accounts.google.com",
		OwnerSubject: "owner-subject", WorkspaceID: "owner-workspace", CredentialKey: make([]byte, 32),
	}, provider, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, provider
}

func beginLogin(t *testing.T, service *Service) (*http.Cookie, string) {
	t.Helper()
	response := httptest.NewRecorder()
	service.HandleLogin(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
	if response.Code != http.StatusFound {
		t.Fatalf("login returned %d: %s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, candidate := range response.Result().Cookies() {
		if candidate.Name == loginCookieName {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("login attempt cookie missing")
	}
	return cookie, location.Query().Get("state")
}

func formPostCallback(cookie *http.Cookie, state, code string) *http.Request {
	form := url.Values{"code": {code}, "state": {state}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	return request
}

func TestLoginAttemptWritesAreRateLimitedBeforeCreation(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	now := time.Now()
	service.now = func() time.Time { return now }

	for requestNumber := 1; requestNumber <= loginAttemptRateLimit; requestNumber++ {
		response := httptest.NewRecorder()
		service.HandleLogin(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
		if response.Code != http.StatusFound {
			t.Fatalf("login request %d returned %d: %s", requestNumber, response.Code, response.Body.String())
		}
	}
	if got := len(store.attempts); got != loginAttemptRateLimit {
		t.Fatalf("stored %d login attempts, want %d", got, loginAttemptRateLimit)
	}

	response := httptest.NewRecorder()
	service.HandleLogin(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "login_rate_limit_exceeded") {
		t.Fatalf("limited login returned %d: %s", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("limited login set an OAuth attempt cookie")
	}
	if got := len(store.attempts); got != loginAttemptRateLimit {
		t.Fatalf("limited login stored an extra attempt: got %d", got)
	}
}

type failingAdmissionStore struct {
	Store
	err error
}

func (s failingAdmissionStore) AllowRequest(context.Context, string, time.Time, int, time.Duration) (bool, error) {
	return false, s.err
}

func TestLoginAdmissionDependencyFailureDoesNotCreateAttempt(t *testing.T) {
	memoryStore := NewMemoryStore()
	service, err := New(Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "https://accounts.google.com",
		OwnerSubject: "owner-subject", WorkspaceID: "owner-workspace", CredentialKey: make([]byte, 32),
	}, &fakeProvider{}, failingAdmissionStore{Store: memoryStore, err: errors.New("rate store unavailable")})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	service.HandleLogin(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "authentication_unavailable") {
		t.Fatalf("failed login admission returned %d: %s", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 0 || len(memoryStore.attempts) != 0 {
		t.Fatal("failed login admission created OAuth state")
	}
}

func TestOwnerSignInUsesPKCEAndSecureHostOnlySession(t *testing.T) {
	service, store, provider := newTestService(t, Identity{Issuer: "https://accounts.google.com", Subject: "owner-subject"})
	loginCookie, state := beginLogin(t, service)
	store.mu.RLock()
	storedAttempt := store.attempts[loginCookie.Value]
	store.mu.RUnlock()
	plainVerifier, err := service.open(storedAttempt.CodeVerifier)
	if err != nil {
		t.Fatal(err)
	}
	if storedAttempt.State == state || storedAttempt.CodeVerifier == plainVerifier {
		t.Fatal("raw OAuth state or PKCE verifier was retained in storage")
	}
	if !loginCookie.HttpOnly || !loginCookie.Secure || loginCookie.SameSite != http.SameSiteNoneMode || loginCookie.Domain != "" || loginCookie.Path != "/" {
		t.Fatalf("cross-site login cookie is not secure and host-only: %#v", loginCookie)
	}
	callback := formPostCallback(loginCookie, state, "authorization-code")
	response := httptest.NewRecorder()
	service.HandleCallback(response, callback)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "https://contentflow.example/" {
		t.Fatalf("callback returned %d, location %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	challenge := sha256.Sum256([]byte(provider.verifier))
	if got := base64.RawURLEncoding.EncodeToString(challenge[:]); got != provider.challenge {
		t.Fatalf("PKCE challenge %q does not match verifier", provider.challenge)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.Domain != "" || sessionCookie.Path != "/" {
		t.Fatalf("session cookie is not secure and host-only: %#v", sessionCookie)
	}
}

func TestCookieSecurityIsDerivedFromAuthenticatedOrigin(t *testing.T) {
	provider := &fakeProvider{}
	for _, test := range []struct {
		origin   string
		secure   bool
		sameSite http.SameSite
	}{
		{"https://staging.contentflow.example", true, http.SameSiteNoneMode},
		{"http://localhost:3000", false, http.SameSiteLaxMode},
		{"http://127.0.0.1:3000", false, http.SameSiteLaxMode},
	} {
		service, err := New(Config{
			PublicOrigin: test.origin, OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
			WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
		}, provider, NewMemoryStore())
		if err != nil {
			t.Fatalf("origin %s failed: %v", test.origin, err)
		}
		response := httptest.NewRecorder()
		service.HandleLogin(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
		cookies := response.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatalf("origin %s produced no login cookie", test.origin)
		}
		if cookies[0].Secure != test.secure || cookies[0].SameSite != test.sameSite {
			t.Fatalf("origin %s produced Secure=%t SameSite=%d, want Secure=%t SameSite=%d", test.origin, cookies[0].Secure, cookies[0].SameSite, test.secure, test.sameSite)
		}
	}

	if _, err := New(Config{
		PublicOrigin: "http://staging.contentflow.example", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
		WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
	}, provider, NewMemoryStore()); err == nil {
		t.Fatal("expected authenticated non-loopback HTTP origin to fail")
	}
}

func TestDifferentOwnerIssuerOrSubjectIsForbidden(t *testing.T) {
	for _, identity := range []Identity{
		{Issuer: "https://accounts.google.com", Subject: "someone-else"},
		{Issuer: "https://different-issuer.example", Subject: "owner-subject"},
	} {
		service, _, _ := newTestService(t, identity)
		cookie, state := beginLogin(t, service)
		request := formPostCallback(cookie, state, "code")
		response := httptest.NewRecorder()
		service.HandleCallback(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "owner_mismatch") {
			t.Fatalf("owner mismatch returned %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestHTTPSCallbackRequiresBoundedFormPost(t *testing.T) {
	for _, test := range []struct {
		name    string
		request func(*http.Cookie, string) *http.Request
	}{
		{
			name: "query response",
			request: func(cookie *http.Cookie, state string) *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=code&state="+url.QueryEscape(state), nil)
				request.AddCookie(cookie)
				return request
			},
		},
		{
			name: "oversized form",
			request: func(cookie *http.Cookie, state string) *http.Request {
				request := formPostCallback(cookie, state, strings.Repeat("a", 65<<10))
				return request
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := newTestService(t, Identity{Issuer: "https://accounts.google.com", Subject: "owner-subject"})
			cookie, state := beginLogin(t, service)
			response := httptest.NewRecorder()
			service.HandleCallback(response, test.request(cookie, state))
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "oauth_state_invalid") {
				t.Fatalf("invalid HTTPS callback returned %d: %s", response.Code, response.Body.String())
			}

			validResponse := httptest.NewRecorder()
			service.HandleCallback(validResponse, formPostCallback(cookie, state, "valid-code"))
			if validResponse.Code != http.StatusFound {
				t.Fatalf("invalid callback consumed the pending attempt: %d: %s", validResponse.Code, validResponse.Body.String())
			}
		})
	}
}

func TestLoopbackHTTPCallbackKeepsQueryModeForLocalDevelopment(t *testing.T) {
	provider := &fakeProvider{identity: Identity{Issuer: "https://accounts.google.com", Subject: "owner-subject"}}
	service, err := New(Config{
		PublicOrigin: "http://localhost:3000", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner-subject",
		WorkspaceID: "owner-workspace", CredentialKey: make([]byte, 32),
	}, provider, NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	cookie, state := beginLogin(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=code&state="+url.QueryEscape(state), nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	service.HandleCallback(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("loopback query callback returned %d: %s", response.Code, response.Body.String())
	}
}

func TestMismatchedOAuthStateDoesNotConsumePendingAttempt(t *testing.T) {
	store := NewMemoryStore()
	attempt := LoginAttempt{ID: "attempt", State: "correct-state", CodeVerifier: "verifier", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.SaveLoginAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeLoginAttempt(context.Background(), attempt.ID, "wrong-state", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched OAuth state returned %v", err)
	}
	if _, err := store.TakeLoginAttempt(context.Background(), attempt.ID, attempt.State, time.Now()); err != nil {
		t.Fatalf("mismatched OAuth state consumed the pending attempt: %v", err)
	}
}

func sessionRequest(t *testing.T, service *Service, store Store, method, target string, body io.Reader) *http.Request {
	t.Helper()
	session := Session{ID: "session-secret", WorkspaceID: "owner-workspace", CSRFToken: "csrf-secret", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, body)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	request.Header.Set("Origin", "https://contentflow.example")
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	return request
}

func protectedHandler(service *Service) http.Handler {
	return service.Authenticate(service.Authorize(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		principal, ok := PrincipalFromContext(request.Context())
		if !ok {
			http.Error(response, "missing principal", http.StatusInternalServerError)
			return
		}
		response.Header().Set("X-Workspace", principal.WorkspaceID)
		response.WriteHeader(http.StatusNoContent)
	})))
}

func TestBrowserMutationsRequireMatchingOriginAndSessionCSRF(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	tests := []struct {
		name   string
		origin string
		csrf   string
	}{
		{"missing origin", "", "csrf-secret"},
		{"wrong origin", "https://evil.example", "csrf-secret"},
		{"missing csrf", "https://contentflow.example", ""},
		{"wrong csrf", "https://contentflow.example", "wrong"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := sessionRequest(t, service, store, http.MethodPost, "/api/v1/content", nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-CSRF-Token", test.csrf)
			response := httptest.NewRecorder()
			protectedHandler(service).ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "csrf_check_failed") {
				t.Fatalf("returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBrowserOriginChecksUseCanonicalURLForm(t *testing.T) {
	provider := &fakeProvider{}
	store := NewMemoryStore()
	service, err := New(Config{
		PublicOrigin: "https://CONTENTFLOW.EXAMPLE:443/", OwnerIssuer: "https://accounts.google.com", OwnerSubject: "owner",
		WorkspaceID: "owner-workspace", CredentialKey: make([]byte, 32),
	}, provider, store)
	if err != nil {
		t.Fatal(err)
	}
	request := sessionRequest(t, service, store, http.MethodPost, "/api/v1/content", nil)
	request.Header.Set("Origin", "https://contentflow.example")
	response := httptest.NewRecorder()
	protectedHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("browser-canonical origin returned %d: %s", response.Code, response.Body.String())
	}
	if service.config.PublicOrigin != "https://contentflow.example" {
		t.Fatalf("configured origin was not canonicalized: %q", service.config.PublicOrigin)
	}
}

func createToken(t *testing.T, service *Service, store Store, scopes string) (string, string) {
	t.Helper()
	request := sessionRequest(t, service, store, http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"scopes":`+scopes+`}`))
	request = request.WithContext(context.WithValue(request.Context(), principalKey{}, Principal{WorkspaceID: "owner-workspace", Kind: "session", CSRFToken: "csrf-secret"}))
	response := httptest.NewRecorder()
	service.HandleCreateToken(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create token returned %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result.ID, result.Token
}

func TestTokensHaveRequiredEntropyAndStoreOnlyHashAndPrefix(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	id, raw := createToken(t, service, store, `["content:read","assets:write"]`)
	if _, err := ulid.ParseStrict(id); err != nil {
		t.Fatalf("token ID is not a ULID: %v", err)
	}
	encoded := strings.TrimPrefix(raw, "cf_")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("token has %d random bytes, decode error %v", len(decoded), err)
	}
	store.mu.RLock()
	stored := store.tokens[id]
	store.mu.RUnlock()
	if stored.Prefix != raw[:12] || stored.Hash != sha256.Sum256([]byte(raw)) {
		t.Fatal("stored token prefix or hash does not match")
	}
	storedJSON, _ := json.Marshal(stored)
	if strings.Contains(string(storedJSON), raw) {
		t.Fatal("raw API token was retained in storage")
	}
}

func TestTokenScopesAdministrationWorkspaceAndRevocation(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	id, raw := createToken(t, service, store, `["content:read"]`)
	handler := protectedHandler(service)

	read := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	read.Header.Set("Authorization", "Bearer "+raw)
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusNoContent || readResponse.Header().Get("X-Workspace") != "owner-workspace" {
		t.Fatalf("scoped read returned %d, workspace %q", readResponse.Code, readResponse.Header().Get("X-Workspace"))
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/content", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/tokens", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/tokens/"+id, nil),
	} {
		request.Header.Set("Authorization", "Bearer "+raw)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("bearer administration/write %s %s returned %d", request.Method, request.URL.Path, response.Code)
		}
	}

	if err := store.RevokeToken(context.Background(), "owner-workspace", id); err != nil {
		t.Fatal(err)
	}
	revoked := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	revoked.Header.Set("Authorization", "Bearer "+raw)
	revokedResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokedResponse, revoked)
	if revokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token returned %d", revokedResponse.Code)
	}
}

func TestEachTokenScopeIsEnforcedIndependently(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	tests := []struct {
		scope   Scope
		method  string
		path    string
		allowed bool
	}{
		{ScopeContentRead, http.MethodGet, "/api/v1/content", true},
		{ScopeContentRead, http.MethodPost, "/api/v1/content", false},
		{ScopeContentWrite, http.MethodPost, "/api/v1/content", true},
		{ScopeContentWrite, http.MethodGet, "/api/v1/content", false},
		{ScopeAssetsWrite, http.MethodPost, "/api/v1/assets/uploads", true},
		{ScopeAssetsWrite, http.MethodPost, "/api/v1/content", false},
	}
	for index, test := range tests {
		raw := "cf_scope_test_" + string(test.scope) + string(rune('a'+index))
		hash := sha256.Sum256([]byte(raw))
		if err := store.SaveToken(context.Background(), Token{ID: raw, WorkspaceID: "owner-workspace", Prefix: raw[:12], Hash: hash, Scopes: []Scope{test.scope}}); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer "+raw)
		response := httptest.NewRecorder()
		protectedHandler(service).ServeHTTP(response, request)
		if test.allowed && response.Code != http.StatusNoContent {
			t.Fatalf("scope %s unexpectedly blocked %s %s with %d", test.scope, test.method, test.path, response.Code)
		}
		if !test.allowed && response.Code != http.StatusForbidden {
			t.Fatalf("scope %s unexpectedly allowed %s %s with %d", test.scope, test.method, test.path, response.Code)
		}
	}
}

func TestPermanentContentDeletionRequiresOwnerSession(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	_, raw := createToken(t, service, store, `["content:write","assets:write"]`)
	handler := protectedHandler(service)

	contentDelete := httptest.NewRequest(http.MethodDelete, "/api/v1/content/01JCONTENT", nil)
	contentDelete.Header.Set("Authorization", "Bearer "+raw)
	tokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(tokenResponse, contentDelete)
	if tokenResponse.Code != http.StatusForbidden || !strings.Contains(tokenResponse.Body.String(), "owner_session_required") {
		t.Fatalf("bearer content deletion returned %d: %s", tokenResponse.Code, tokenResponse.Body.String())
	}

	assetDelete := httptest.NewRequest(http.MethodDelete, "/api/v1/content/01JCONTENT/assets/01JASSET", nil)
	assetDelete.Header.Set("Authorization", "Bearer "+raw)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetDelete)
	if assetResponse.Code != http.StatusNoContent {
		t.Fatalf("scoped bearer asset deletion returned %d: %s", assetResponse.Code, assetResponse.Body.String())
	}

	sessionDelete := sessionRequest(t, service, store, http.MethodDelete, "/api/v1/content/01JCONTENT", nil)
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionDelete)
	if sessionResponse.Code != http.StatusNoContent {
		t.Fatalf("owner session content deletion returned %d: %s", sessionResponse.Code, sessionResponse.Body.String())
	}
}

func TestBearerAuthenticationSchemeIsCaseInsensitive(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	_, raw := createToken(t, service, store, `["content:read"]`)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	request.Header.Set("Authorization", "bearer "+raw)
	response := httptest.NewRecorder()
	protectedHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("lowercase bearer scheme returned %d: %s", response.Code, response.Body.String())
	}
}

func TestPersistedCredentialsAreBoundToConfiguredWorkspace(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	handler := protectedHandler(service)

	foreignSession := Session{ID: "foreign-session", WorkspaceID: "another-workspace", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.SaveSession(context.Background(), foreignSession); err != nil {
		t.Fatal(err)
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	sessionRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: foreignSession.ID})
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusUnauthorized || !strings.Contains(sessionResponse.Body.String(), "authentication_required") {
		t.Fatalf("foreign-workspace session returned %d: %s", sessionResponse.Code, sessionResponse.Body.String())
	}

	raw := "cf_foreign_workspace_token"
	hash := sha256.Sum256([]byte(raw))
	if err := store.SaveToken(context.Background(), Token{
		ID: "foreign-token", WorkspaceID: "another-workspace", Prefix: raw[:12], Hash: hash, Scopes: []Scope{ScopeContentRead},
	}); err != nil {
		t.Fatal(err)
	}
	tokenRequest := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
	tokenRequest.Header.Set("Authorization", "Bearer "+raw)
	tokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusUnauthorized || !strings.Contains(tokenResponse.Body.String(), "invalid_bearer_token") {
		t.Fatalf("foreign-workspace token returned %d: %s", tokenResponse.Code, tokenResponse.Body.String())
	}
}

func TestAPIClientsAreLimitedTo120RequestsPerMinute(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	_, raw := createToken(t, service, store, `["content:read"]`)
	handler := protectedHandler(service)
	for requestNumber := 1; requestNumber <= 121; requestNumber++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/content", nil)
		request.Header.Set("Authorization", "Bearer "+raw)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if requestNumber == 121 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("request %d returned %d, want %d", requestNumber, response.Code, want)
		}
	}
}

func TestTokenScopesRejectUnknownAndDuplicateValues(t *testing.T) {
	service, store, _ := newTestService(t, Identity{})
	for _, scopes := range []string{`[]`, `["admin"]`, `["content:read","content:read"]`} {
		request := sessionRequest(t, service, store, http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"scopes":`+scopes+`}`))
		request = request.WithContext(context.WithValue(request.Context(), principalKey{}, Principal{WorkspaceID: "owner-workspace", Kind: "session"}))
		response := httptest.NewRecorder()
		service.HandleCreateToken(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("scopes %s returned %d", scopes, response.Code)
		}
	}
}
