package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFirestoreReadinessOnlyTreatsAnEmptyQueryAsHealthy(t *testing.T) {
	if err := firestoreReadinessError(iterator.Done); err != nil {
		t.Fatalf("empty Firestore query failed readiness: %v", err)
	}
	databaseMissing := status.Error(codes.NotFound, "database missing")
	if err := firestoreReadinessError(databaseMissing); !errors.Is(err, databaseMissing) {
		t.Fatalf("database-level NotFound was treated as healthy: %v", err)
	}
}

func TestFirestoreStorePersistsSessionsAndImmediateTokenRevocation(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	firstClient, err := firestore.NewClient(ctx, "contentflow-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.Close()
	secondClient, err := firestore.NewClient(ctx, "contentflow-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()

	first := NewFirestoreStore(firstClient)
	second := NewFirestoreStore(secondClient)
	if err := second.Check(ctx); err != nil {
		t.Fatalf("Firestore auth readiness check failed: %v", err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	session := Session{ID: "raw-session-" + suffix, WorkspaceID: "workspace", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour)}
	if err := first.SaveSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	persistedSession, err := second.Session(ctx, session.ID, time.Now())
	if err != nil || persistedSession.WorkspaceID != session.WorkspaceID {
		t.Fatalf("session did not persist across store instances: %#v, %v", persistedSession, err)
	}
	if _, err := firstClient.Collection(sessionsCollection).Doc(session.ID).Get(ctx); !firestoreIsNotFound(err) {
		t.Fatal("raw session credential was used as a Firestore document ID")
	}

	raw := "cf_0123456789012345678901234567890123456789012" + suffix
	hash := sha256.Sum256([]byte(raw))
	token := Token{ID: "01JTESTTOKEN" + suffix, WorkspaceID: "workspace", Prefix: raw[:12], Hash: hash, Scopes: []Scope{ScopeContentRead}, CreatedAt: time.Now()}
	if err := first.SaveToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	persistedToken, err := second.TokenByHash(ctx, hash)
	if err != nil || persistedToken.ID != token.ID {
		t.Fatalf("token hash did not persist across store instances: %#v, %v", persistedToken, err)
	}
	snapshot, err := firstClient.Collection(tokensCollection).Doc(token.ID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, rawStored := snapshot.Data()["token"]; rawStored {
		t.Fatal("Firestore token record contains a raw token field")
	}
	if err := second.RevokeToken(ctx, "workspace", token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.TokenByHash(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revocation was not visible on the next request: %v", err)
	}
}

func TestFirestoreLoginAttemptCanOnlyBeConsumedOnce(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "contentflow-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	store := NewFirestoreStore(client)
	attempt := LoginAttempt{ID: "attempt-" + time.Now().String(), State: "state", CodeVerifier: "verifier", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.SaveLoginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeLoginAttempt(ctx, attempt.ID, "wrong-state", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched OAuth state returned %v", err)
	}
	if _, err := store.TakeLoginAttempt(ctx, attempt.ID, attempt.State, time.Now()); err != nil {
		t.Fatalf("mismatched OAuth state consumed the pending attempt: %v", err)
	}
	if _, err := store.TakeLoginAttempt(ctx, attempt.ID, attempt.State, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("consumed OAuth attempt was reusable: %v", err)
	}
}

func TestFirestoreRateLimitIsSharedAcrossStoreInstances(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	firstClient, err := firestore.NewClient(ctx, "contentflow-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.Close()
	secondClient, err := firestore.NewClient(ctx, "contentflow-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	stores := []*FirestoreStore{NewFirestoreStore(firstClient), NewFirestoreStore(secondClient)}
	tokenID := "rate-" + time.Now().String()
	now := time.Now()
	for index := 0; index < 120; index++ {
		allowed, err := stores[index%len(stores)].AllowTokenRequest(ctx, tokenID, now, 120, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("request %d was not allowed: %v", index+1, err)
		}
	}
	allowed, err := stores[1].AllowTokenRequest(ctx, tokenID, now, 120, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("shared Firestore rate limit allowed request 121")
	}
}

func TestFirestoreRateLimitDoesNotOverAdmitDuringConcurrentRetries(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	clients := make([]*firestore.Client, 4)
	stores := make([]*FirestoreStore, len(clients))
	for index := range clients {
		client, err := firestore.NewClient(ctx, "contentflow-auth-test")
		if err != nil {
			t.Fatal(err)
		}
		clients[index] = client
		stores[index] = NewFirestoreStore(client)
		defer client.Close()
	}

	tokenID := "concurrent-rate-" + time.Now().String()
	now := time.Now()
	start := make(chan struct{})
	errorsChannel := make(chan error, 160)
	var allowed atomic.Int64
	var limited atomic.Int64
	var wait sync.WaitGroup
	for worker := range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for range 20 {
				accepted, err := stores[worker%len(stores)].AllowTokenRequest(ctx, tokenID, now, 120, time.Minute)
				if err != nil {
					errorsChannel <- err
					continue
				}
				if accepted {
					allowed.Add(1)
				} else {
					limited.Add(1)
				}
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	retryableFailures := 0
	for err := range errorsChannel {
		if status.Code(err) != codes.Aborted {
			t.Fatalf("concurrent rate-limit transaction returned non-retryable error: %v", err)
		}
		retryableFailures++
	}
	admitted := allowed.Load()
	if admitted > 120 {
		t.Fatalf("concurrent shared limit admitted %d requests, want at most 120", admitted)
	}
	if got := admitted + limited.Load() + int64(retryableFailures); got != 160 {
		t.Fatalf("concurrent outcomes accounted for %d requests, want 160", got)
	}

	var document rateLimitDocument
	snapshot, err := clients[0].Collection(rateLimitsCollection).Doc(tokenID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.DataTo(&document); err != nil {
		t.Fatal(err)
	}
	if int64(document.Count) != admitted {
		t.Fatalf("persisted rate count is %d after %d admitted requests", document.Count, admitted)
	}

	for admitted < 120 {
		accepted, err := stores[0].AllowTokenRequest(ctx, tokenID, now, 120, time.Minute)
		if err != nil || !accepted {
			t.Fatalf("sequential retry %d was not admitted: accepted=%t error=%v", admitted+1, accepted, err)
		}
		admitted++
	}
	accepted, err := stores[0].AllowTokenRequest(ctx, tokenID, now, 120, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("concurrent rate state allowed request 121 after retryable failures were replayed")
	}
}

func TestFirestoreRateLimitDoesNotResetForOutOfOrderRequestTime(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "contentflow-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	store := NewFirestoreStore(client)
	tokenID := "out-of-order-rate-" + time.Now().String()
	newer := time.Now()
	for request := 1; request <= 120; request++ {
		allowed, err := store.AllowTokenRequest(ctx, tokenID, newer, 120, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("newer request %d was not allowed: %v", request, err)
		}
	}
	allowed, err := store.AllowTokenRequest(ctx, tokenID, newer.Add(-time.Second), 120, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("older request time reset the active rate-limit window")
	}
}
