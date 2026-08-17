package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashesAreSaltedAndVerifyConstantTime(t *testing.T) {
	first, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the same password produced the same hash, so it is not salted")
	}
	if !strings.HasPrefix(first, "$argon2id$") {
		t.Fatalf("hash is not argon2id: %s", first)
	}

	matched, err := VerifyPassword(first, "correct horse battery staple")
	if err != nil || !matched {
		t.Fatalf("the correct password did not verify: %v, %v", matched, err)
	}
	matched, err = VerifyPassword(first, "wrong password entirely")
	if err != nil || matched {
		t.Fatalf("a wrong password verified: %v, %v", matched, err)
	}
	if _, err := VerifyPassword("not-a-hash", "anything"); err == nil {
		t.Fatal("a malformed hash was accepted")
	}
}

func newPasswordService(t *testing.T) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	service, err := New(Config{
		PublicOrigin: "https://contentflow.example", OwnerIssuer: "issuer", OwnerSubject: "owner",
		WorkspaceID: "workspace", CredentialKey: make([]byte, 32),
	}, &fakeProvider{}, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func signIn(service *Service, email, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.HandlePasswordLogin(response, request)
	return response
}

func TestPasswordSignInIssuesASessionAndRejectsBadCredentials(t *testing.T) {
	service, store := newPasswordService(t)
	hash, err := HashPassword("a-sufficiently-long-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), User{
		ID: "user-1", WorkspaceID: "workspace", Email: "Owain@Example.com",
		PasswordHash: hash, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// Email matching ignores case.
	response := signIn(service, "owain@example.com", "a-sufficiently-long-password")
	if response.Code != http.StatusOK {
		t.Fatalf("valid credentials returned %d: %s", response.Code, response.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("no session cookie was set")
	}
	if !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("session cookie is not HttpOnly and Secure: %#v", sessionCookie)
	}
	if _, err := store.Session(context.Background(), sessionCookie.Value, time.Now()); err != nil {
		t.Fatalf("the issued session was not stored: %v", err)
	}

	if wrong := signIn(service, "owain@example.com", "not the password"); wrong.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password returned %d", wrong.Code)
	}
	// An unknown account and a wrong password must be indistinguishable.
	unknown := signIn(service, "nobody@example.com", "not the password")
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("an unknown account returned %d", unknown.Code)
	}
	wrong := signIn(service, "owain@example.com", "not the password")
	if unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown account and wrong password differ: %q vs %q", unknown.Body.String(), wrong.Body.String())
	}
}

func TestPasswordSignInRejectsEmptyCredentials(t *testing.T) {
	service, _ := newPasswordService(t)
	for _, test := range []struct{ email, password string }{
		{"", "a-sufficiently-long-password"},
		{"owain@example.com", ""},
	} {
		if response := signIn(service, test.email, test.password); response.Code != http.StatusUnauthorized {
			t.Fatalf("empty credentials returned %d", response.Code)
		}
	}
}
