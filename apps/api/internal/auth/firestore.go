package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	loginAttemptsCollection = "oauth_login_attempts"
	sessionsCollection      = "sessions"
	tokensCollection        = "api_tokens"
	rateLimitsCollection    = "api_token_rate_limits"
)

type FirestoreStore struct {
	client *firestore.Client
}

func NewFirestoreStore(client *firestore.Client) *FirestoreStore {
	return &FirestoreStore{client: client}
}

func (s *FirestoreStore) Check(ctx context.Context) error {
	_, err := s.client.Collection(sessionsCollection).Doc("_readiness").Get(ctx)
	if firestoreIsNotFound(err) {
		return nil
	}
	return err
}

type loginAttemptDocument struct {
	State        string    `firestore:"state"`
	CodeVerifier string    `firestore:"code_verifier"`
	ExpiresAt    time.Time `firestore:"expires_at"`
}

type sessionDocument struct {
	WorkspaceID string    `firestore:"workspace_id"`
	CSRFToken   string    `firestore:"csrf_token"`
	ExpiresAt   time.Time `firestore:"expires_at"`
}

type tokenDocument struct {
	ID          string    `firestore:"id"`
	WorkspaceID string    `firestore:"workspace_id"`
	Prefix      string    `firestore:"prefix"`
	Hash        []byte    `firestore:"token_hash"`
	Scopes      []string  `firestore:"scopes"`
	CreatedAt   time.Time `firestore:"created_at"`
}

type rateLimitDocument struct {
	WindowStartedAt time.Time `firestore:"window_started_at"`
	Count           int       `firestore:"count"`
	ExpiresAt       time.Time `firestore:"expires_at"`
}

func (s *FirestoreStore) SaveLoginAttempt(ctx context.Context, attempt LoginAttempt) error {
	_, err := s.client.Collection(loginAttemptsCollection).Doc(credentialDocumentID(attempt.ID)).Create(ctx, loginAttemptDocument{
		State: attempt.State, CodeVerifier: attempt.CodeVerifier, ExpiresAt: attempt.ExpiresAt,
	})
	return err
}

func (s *FirestoreStore) TakeLoginAttempt(ctx context.Context, id, state string, now time.Time) (LoginAttempt, error) {
	ref := s.client.Collection(loginAttemptsCollection).Doc(credentialDocumentID(id))
	var attempt LoginAttempt
	err := s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		snapshot, err := transaction.Get(ref)
		if err != nil {
			return err
		}
		var document loginAttemptDocument
		if err := snapshot.DataTo(&document); err != nil {
			return err
		}
		if err := transaction.Delete(ref); err != nil {
			return err
		}
		attempt = LoginAttempt{ID: id, State: document.State, CodeVerifier: document.CodeVerifier, ExpiresAt: document.ExpiresAt}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || firestoreIsNotFound(err) {
			return LoginAttempt{}, ErrNotFound
		}
		return LoginAttempt{}, err
	}
	if !secureEqual(attempt.State, state) || !attempt.ExpiresAt.After(now) {
		return LoginAttempt{}, ErrNotFound
	}
	return attempt, nil
}

func (s *FirestoreStore) SaveSession(ctx context.Context, session Session) error {
	_, err := s.client.Collection(sessionsCollection).Doc(credentialDocumentID(session.ID)).Create(ctx, sessionDocument{
		WorkspaceID: session.WorkspaceID, CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt,
	})
	return err
}

func (s *FirestoreStore) Session(ctx context.Context, id string, now time.Time) (Session, error) {
	snapshot, err := s.client.Collection(sessionsCollection).Doc(credentialDocumentID(id)).Get(ctx)
	if err != nil {
		if firestoreIsNotFound(err) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	var document sessionDocument
	if err := snapshot.DataTo(&document); err != nil {
		return Session{}, err
	}
	if !document.ExpiresAt.After(now) {
		return Session{}, ErrNotFound
	}
	return Session{ID: id, WorkspaceID: document.WorkspaceID, CSRFToken: document.CSRFToken, ExpiresAt: document.ExpiresAt}, nil
}

func (s *FirestoreStore) SaveToken(ctx context.Context, token Token) error {
	scopes := make([]string, len(token.Scopes))
	for index, scope := range token.Scopes {
		scopes[index] = string(scope)
	}
	_, err := s.client.Collection(tokensCollection).Doc(token.ID).Create(ctx, tokenDocument{
		ID: token.ID, WorkspaceID: token.WorkspaceID, Prefix: token.Prefix, Hash: token.Hash[:], Scopes: scopes, CreatedAt: token.CreatedAt,
	})
	return err
}

func (s *FirestoreStore) TokenByHash(ctx context.Context, hash [sha256.Size]byte) (Token, error) {
	documents := s.client.Collection(tokensCollection).Where("token_hash", "==", hash[:]).Limit(1).Documents(ctx)
	defer documents.Stop()
	snapshot, err := documents.Next()
	if err != nil {
		if err == iterator.Done {
			return Token{}, ErrNotFound
		}
		return Token{}, err
	}
	var document tokenDocument
	if err := snapshot.DataTo(&document); err != nil {
		return Token{}, err
	}
	if len(document.Hash) != sha256.Size {
		return Token{}, fmt.Errorf("invalid stored token hash")
	}
	var storedHash [sha256.Size]byte
	copy(storedHash[:], document.Hash)
	scopes := make([]Scope, len(document.Scopes))
	for index, scope := range document.Scopes {
		scopes[index] = Scope(scope)
	}
	return Token{ID: document.ID, WorkspaceID: document.WorkspaceID, Prefix: document.Prefix, Hash: storedHash, Scopes: scopes, CreatedAt: document.CreatedAt}, nil
}

func (s *FirestoreStore) RevokeToken(ctx context.Context, workspaceID, id string) error {
	ref := s.client.Collection(tokensCollection).Doc(id)
	err := s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		snapshot, err := transaction.Get(ref)
		if err != nil {
			return err
		}
		var document tokenDocument
		if err := snapshot.DataTo(&document); err != nil {
			return err
		}
		if document.WorkspaceID != workspaceID {
			return ErrNotFound
		}
		return transaction.Delete(ref)
	})
	if errors.Is(err, ErrNotFound) || firestoreIsNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (s *FirestoreStore) AllowTokenRequest(ctx context.Context, tokenID string, now time.Time, limit int, window time.Duration) (bool, error) {
	ref := s.client.Collection(rateLimitsCollection).Doc(tokenID)
	allowed := false
	err := s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		// RunTransaction may retry this callback after a commit conflict.
		allowed = false
		document := rateLimitDocument{WindowStartedAt: now, ExpiresAt: now.Add(2 * window)}
		snapshot, err := transaction.Get(ref)
		if err == nil {
			if err := snapshot.DataTo(&document); err != nil {
				return err
			}
		} else if !firestoreIsNotFound(err) {
			return err
		}
		if now.Sub(document.WindowStartedAt) >= window || now.Before(document.WindowStartedAt) {
			document = rateLimitDocument{WindowStartedAt: now, ExpiresAt: now.Add(2 * window)}
		}
		if document.Count >= limit {
			return nil
		}
		document.Count++
		document.ExpiresAt = document.WindowStartedAt.Add(2 * window)
		allowed = true
		return transaction.Set(ref, document)
	})
	return allowed, err
}

func credentialDocumentID(raw string) string {
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

func firestoreIsNotFound(err error) bool {
	return status.Code(err) == codes.NotFound
}
