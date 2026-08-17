package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/owainlewis/contentflow/apps/api/internal/database"
)

func newPostgresStore(t *testing.T) *PostgresStore {
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
	if _, err := pool.Exec(context.Background(),
		"truncate oauth_login_attempts, sessions, api_tokens, api_token_rate_limits"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewPostgresStore(pool)
}

func TestPostgresLoginAttemptsAreSingleUseAndStateChecked(t *testing.T) {
	store := newPostgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	attempt := LoginAttempt{ID: "attempt-id", State: "state-value", CodeVerifier: "verifier", ExpiresAt: now.Add(10 * time.Minute)}
	if err := store.SaveLoginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}

	if _, err := store.TakeLoginAttempt(ctx, "attempt-id", "wrong-state", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a mismatched state was accepted: %v", err)
	}
	taken, err := store.TakeLoginAttempt(ctx, "attempt-id", "state-value", now)
	if err != nil || taken.CodeVerifier != "verifier" {
		t.Fatalf("take failed: %#v, %v", taken, err)
	}
	if _, err := store.TakeLoginAttempt(ctx, "attempt-id", "state-value", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attempt was reusable: %v", err)
	}
}

func TestPostgresSessionsExpireAndStoreOnlyDigests(t *testing.T) {
	store := newPostgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	session := Session{ID: "raw-session-id", WorkspaceID: "workspace", CSRFToken: "csrf", ExpiresAt: now.Add(time.Hour)}
	if err := store.SaveSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Session(ctx, "raw-session-id", now)
	if err != nil || loaded.WorkspaceID != "workspace" || loaded.CSRFToken != "csrf" {
		t.Fatalf("session did not round trip: %#v, %v", loaded, err)
	}
	if _, err := store.Session(ctx, "raw-session-id", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session stayed valid: %v", err)
	}

	var stored string
	if err := store.pool.QueryRow(ctx, "select id from sessions limit 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "raw-session-id" {
		t.Fatal("the raw session id was stored instead of a digest")
	}
}

func TestPostgresTokensRoundTripAndRevokeWithinWorkspace(t *testing.T) {
	store := newPostgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("secret-token"))
	token := Token{ID: "token-1", WorkspaceID: "workspace", Prefix: "cf_abc", Hash: hash,
		Scopes: []Scope{ScopeContentRead, ScopeContentWrite}, CreatedAt: now}
	if err := store.SaveToken(ctx, token); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.TokenByHash(ctx, hash)
	if err != nil || loaded.ID != "token-1" || len(loaded.Scopes) != 2 || loaded.Scopes[1] != ScopeContentWrite {
		t.Fatalf("token did not round trip: %#v, %v", loaded, err)
	}
	if err := store.RevokeToken(ctx, "other-workspace", "token-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a token was revoked across workspaces: %v", err)
	}
	if err := store.RevokeToken(ctx, "workspace", "token-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TokenByHash(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token still resolved: %v", err)
	}
}

func TestPostgresRateLimitsAdmitUntilTheLimitAndResetPerWindow(t *testing.T) {
	store := newPostgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	window := time.Minute
	buckets := []RateLimitBucket{{ID: "client", Limit: 2}, {ID: "workspace", Limit: 3}}

	for attempt := 1; attempt <= 2; attempt++ {
		allowed, err := store.AllowRequests(ctx, buckets, now, window)
		if err != nil || !allowed {
			t.Fatalf("request %d was refused: %v, %v", attempt, allowed, err)
		}
	}
	allowed, err := store.AllowRequests(ctx, buckets, now, window)
	if err != nil || allowed {
		t.Fatalf("the tightest bucket did not stop the third request: %v, %v", allowed, err)
	}

	// A refused request must not consume budget from the wider bucket.
	allowed, err = store.AllowRequests(ctx, buckets, now.Add(window), window)
	if err != nil || !allowed {
		t.Fatalf("a new window did not reset the buckets: %v, %v", allowed, err)
	}
}
