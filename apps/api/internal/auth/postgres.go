package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/owainlewis/contentflow/apps/api/internal/database"
)

// PostgresStore holds sessions, OAuth attempts, API tokens, and rate limits.
// Session and attempt identifiers are stored as SHA-256 digests so a database
// leak does not hand over usable credentials.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func credentialKey(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *PostgresStore) SaveLoginAttempt(ctx context.Context, attempt LoginAttempt) error {
	_, err := s.pool.Exec(ctx,
		`insert into oauth_login_attempts (id, state, code_verifier, expires_at) values ($1, $2, $3, $4)`,
		credentialKey(attempt.ID), attempt.State, attempt.CodeVerifier, attempt.ExpiresAt)
	return err
}

func (s *PostgresStore) TakeLoginAttempt(ctx context.Context, id, state string, now time.Time) (LoginAttempt, error) {
	return database.RetrySerializable(ctx, func() (LoginAttempt, error) {
		transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return LoginAttempt{}, err
		}
		defer func() { _ = transaction.Rollback(ctx) }()

		key := credentialKey(id)
		row := transaction.QueryRow(ctx,
			`select state, code_verifier, expires_at from oauth_login_attempts where id = $1 for update`, key)
		var attempt LoginAttempt
		switch err := row.Scan(&attempt.State, &attempt.CodeVerifier, &attempt.ExpiresAt); {
		case errors.Is(err, pgx.ErrNoRows):
			return LoginAttempt{}, ErrNotFound
		case err != nil:
			return LoginAttempt{}, err
		}
		if !secureEqual(attempt.State, state) || !attempt.ExpiresAt.After(now) {
			return LoginAttempt{}, ErrNotFound
		}
		if _, err := transaction.Exec(ctx, `delete from oauth_login_attempts where id = $1`, key); err != nil {
			return LoginAttempt{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return LoginAttempt{}, err
		}
		attempt.ID = id
		return attempt, nil
	})
}

func (s *PostgresStore) SaveSession(ctx context.Context, session Session) error {
	_, err := s.pool.Exec(ctx,
		`insert into sessions (id, workspace_id, csrf_token, expires_at) values ($1, $2, $3, $4)`,
		credentialKey(session.ID), session.WorkspaceID, session.CSRFToken, session.ExpiresAt)
	return err
}

func (s *PostgresStore) Session(ctx context.Context, id string, now time.Time) (Session, error) {
	row := s.pool.QueryRow(ctx,
		`select workspace_id, csrf_token, expires_at from sessions where id = $1`, credentialKey(id))
	session := Session{ID: id}
	switch err := row.Scan(&session.WorkspaceID, &session.CSRFToken, &session.ExpiresAt); {
	case errors.Is(err, pgx.ErrNoRows):
		return Session{}, ErrNotFound
	case err != nil:
		return Session{}, err
	}
	if !session.ExpiresAt.After(now) {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (s *PostgresStore) SaveToken(ctx context.Context, token Token) error {
	scopes := make([]string, len(token.Scopes))
	for index, scope := range token.Scopes {
		scopes[index] = string(scope)
	}
	_, err := s.pool.Exec(ctx,
		`insert into api_tokens (id, workspace_id, prefix, hash, scopes, created_at) values ($1, $2, $3, $4, $5, $6)`,
		token.ID, token.WorkspaceID, token.Prefix, token.Hash[:], scopes, token.CreatedAt)
	return err
}

func (s *PostgresStore) TokenByHash(ctx context.Context, hash [sha256.Size]byte) (Token, error) {
	row := s.pool.QueryRow(ctx,
		`select id, workspace_id, prefix, hash, scopes, created_at from api_tokens where hash = $1`, hash[:])
	var token Token
	var storedHash []byte
	var scopes []string
	switch err := row.Scan(&token.ID, &token.WorkspaceID, &token.Prefix, &storedHash, &scopes, &token.CreatedAt); {
	case errors.Is(err, pgx.ErrNoRows):
		return Token{}, ErrNotFound
	case err != nil:
		return Token{}, err
	}
	if len(storedHash) != sha256.Size {
		return Token{}, fmt.Errorf("invalid stored token hash")
	}
	copy(token.Hash[:], storedHash)
	token.Scopes = make([]Scope, len(scopes))
	for index, scope := range scopes {
		token.Scopes[index] = Scope(scope)
	}
	return token, nil
}

func (s *PostgresStore) RevokeToken(ctx context.Context, workspaceID, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from api_tokens where id = $1 and workspace_id = $2`, id, workspaceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AllowRequests admits a request only when every bucket is under its limit. The
// buckets are read and written in one serializable transaction so concurrent
// requests cannot both slip past the same limit.
func (s *PostgresStore) AllowRequests(ctx context.Context, buckets []RateLimitBucket, now time.Time, window time.Duration) (bool, error) {
	return database.RetrySerializable(ctx, func() (bool, error) {
		transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return false, err
		}
		defer func() { _ = transaction.Rollback(ctx) }()

		type bucketState struct {
			id      string
			started time.Time
			count   int
		}
		states := make([]bucketState, len(buckets))
		for index, bucket := range buckets {
			state := bucketState{id: bucket.ID, started: now}
			row := transaction.QueryRow(ctx,
				`select window_started_at, count from api_token_rate_limits where id = $1 for update`, bucket.ID)
			switch err := row.Scan(&state.started, &state.count); {
			case errors.Is(err, pgx.ErrNoRows):
				state.started, state.count = now, 0
			case err != nil:
				return false, err
			}
			if now.Sub(state.started) >= window {
				state.started, state.count = now, 0
			}
			if state.count >= bucket.Limit {
				return false, nil
			}
			states[index] = state
		}

		for _, state := range states {
			if _, err := transaction.Exec(ctx,
				`insert into api_token_rate_limits (id, window_started_at, count, expires_at) values ($1, $2, $3, $4)
				 on conflict (id) do update set window_started_at = excluded.window_started_at,
				   count = excluded.count, expires_at = excluded.expires_at`,
				state.id, state.started, state.count+1, state.started.Add(2*window)); err != nil {
				return false, err
			}
		}
		if err := transaction.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (s *PostgresStore) AllowRequest(ctx context.Context, bucketID string, now time.Time, limit int, window time.Duration) (bool, error) {
	return s.AllowRequests(ctx, []RateLimitBucket{{ID: bucketID, Limit: limit}}, now, window)
}

func (s *PostgresStore) SaveUser(ctx context.Context, user User) error {
	_, err := s.pool.Exec(ctx,
		`insert into users (id, workspace_id, email, password_hash, created_at) values ($1, $2, $3, $4, $5)
		 on conflict (email) do update set password_hash = excluded.password_hash, workspace_id = excluded.workspace_id`,
		user.ID, user.WorkspaceID, NormalizeEmail(user.Email), user.PasswordHash, user.CreatedAt)
	return err
}

func (s *PostgresStore) UserByEmail(ctx context.Context, email string) (User, error) {
	row := s.pool.QueryRow(ctx,
		`select id, workspace_id, email, password_hash, created_at from users where email = $1`, NormalizeEmail(email))
	var user User
	switch err := row.Scan(&user.ID, &user.WorkspaceID, &user.Email, &user.PasswordHash, &user.CreatedAt); {
	case errors.Is(err, pgx.ErrNoRows):
		return User{}, ErrNotFound
	case err != nil:
		return User{}, err
	}
	return user, nil
}
