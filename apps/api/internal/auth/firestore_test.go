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
)

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
	if _, err := store.TakeLoginAttempt(ctx, attempt.ID, attempt.State, time.Now()); err != nil {
		t.Fatal(err)
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
				}
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent rate-limit transaction failed: %v", err)
	}
	if got := allowed.Load(); got != 120 {
		t.Fatalf("concurrent shared limit admitted %d requests, want exactly 120", got)
	}
}
