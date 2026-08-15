package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

var ErrNotFound = errors.New("auth record not found")

type LoginAttempt struct {
	ID           string
	State        string
	CodeVerifier string
	ExpiresAt    time.Time
}

type Session struct {
	ID          string
	WorkspaceID string
	CSRFToken   string
	ExpiresAt   time.Time
}

type Token struct {
	ID          string
	WorkspaceID string
	Prefix      string
	Hash        [sha256.Size]byte
	Scopes      []Scope
	CreatedAt   time.Time
}

type Store interface {
	SaveLoginAttempt(context.Context, LoginAttempt) error
	TakeLoginAttempt(context.Context, string, string, time.Time) (LoginAttempt, error)
	SaveSession(context.Context, Session) error
	Session(context.Context, string, time.Time) (Session, error)
	SaveToken(context.Context, Token) error
	TokenByHash(context.Context, [sha256.Size]byte) (Token, error)
	RevokeToken(context.Context, string, string) error
	AllowRequest(context.Context, string, time.Time, int, time.Duration) (bool, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	attempts map[string]LoginAttempt
	sessions map[string]Session
	tokens   map[string]Token
	hashes   map[[sha256.Size]byte]string
	rates    map[string]rateLimit
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		attempts: make(map[string]LoginAttempt),
		sessions: make(map[string]Session),
		tokens:   make(map[string]Token),
		hashes:   make(map[[sha256.Size]byte]string),
		rates:    make(map[string]rateLimit),
	}
}

func (s *MemoryStore) AllowRequest(_ context.Context, bucketID string, now time.Time, limit int, window time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.rates[bucketID]
	if item.start.IsZero() || now.Sub(item.start) >= window {
		item = rateLimit{start: now}
	}
	if item.count >= limit {
		return false, nil
	}
	item.count++
	s.rates[bucketID] = item
	return true, nil
}

func (s *MemoryStore) SaveLoginAttempt(_ context.Context, attempt LoginAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[attempt.ID] = attempt
	return nil
}

func (s *MemoryStore) TakeLoginAttempt(_ context.Context, id, state string, now time.Time) (LoginAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[id]
	if !ok || attempt.State != state || !attempt.ExpiresAt.After(now) {
		return LoginAttempt{}, ErrNotFound
	}
	delete(s.attempts, id)
	return attempt, nil
}

func (s *MemoryStore) SaveSession(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	return nil
}

func (s *MemoryStore) Session(_ context.Context, id string, now time.Time) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	if !ok || !session.ExpiresAt.After(now) {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (s *MemoryStore) SaveToken(_ context.Context, token Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token.ID] = token
	s.hashes[token.Hash] = token.ID
	return nil
}

func (s *MemoryStore) TokenByHash(_ context.Context, hash [sha256.Size]byte) (Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.hashes[hash]
	if !ok {
		return Token{}, ErrNotFound
	}
	token, ok := s.tokens[id]
	if !ok {
		return Token{}, ErrNotFound
	}
	return token, nil
}

func (s *MemoryStore) RevokeToken(_ context.Context, workspaceID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[id]
	if !ok || token.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	delete(s.tokens, id)
	delete(s.hashes, token.Hash)
	return nil
}
