package content

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/database"
)

// WeeklyRhythm belongs to the workspace and does not expire with content.
type WeeklyRhythm struct {
	Revision int64        `json:"revision"`
	Targets  map[Type]int `json:"targets"`
}

func DefaultWeeklyRhythm() WeeklyRhythm {
	return WeeklyRhythm{Targets: map[Type]int{
		TypeYouTube: 1, TypeInstagram: 7, TypeLinkedIn: 7, TypeEmail: 1,
		TypeLinkedInNewsletter: 1, TypeCarousel: 1, TypeTikTok: 0, TypeSubstack: 0, TypeX: 0,
	}}
}

type rhythmStore interface {
	GetRhythm(context.Context, string) (WeeklyRhythm, error)
	PutRhythm(context.Context, string, WeeklyRhythm) (WeeklyRhythm, error)
}

func (h *HTTPHandler) rhythm(response http.ResponseWriter, request *http.Request) {
	store, ok := h.service.store.(rhythmStore)
	if !ok {
		writeContentError(response, problem(503, "rhythm_unavailable"))
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	var result WeeklyRhythm
	var err error
	if request.Method == http.MethodGet {
		result, err = store.GetRhythm(request.Context(), principal.WorkspaceID)
	} else {
		body, readErr := readMutationBody(response, request)
		if readErr != nil {
			writeContentError(response, readErr)
			return
		}
		var input struct {
			Revision *int64        `json:"revision"`
			Targets  map[Type]*int `json:"targets"`
		}
		if decodeExact(body, &input) != nil || input.Revision == nil || *input.Revision < 0 || len(input.Targets) != len(DefaultWeeklyRhythm().Targets) {
			writeContentError(response, problem(400, "invalid_rhythm"))
			return
		}
		targets := make(map[Type]int, len(input.Targets))
		for kind, target := range input.Targets {
			if !validType(kind) || target == nil || *target < 0 || *target > 35 {
				writeContentError(response, problem(400, "invalid_rhythm"))
				return
			}
			targets[kind] = *target
		}
		result, err = store.PutRhythm(request.Context(), principal.WorkspaceID, WeeklyRhythm{Revision: *input.Revision, Targets: targets})
	}
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, result)
}

func (s *MemoryStore) GetRhythm(_ context.Context, workspace string) (WeeklyRhythm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.rhythms[workspace]
	if !ok {
		value = DefaultWeeklyRhythm()
	}
	value.Targets = maps.Clone(value.Targets)
	return value, nil
}

func (s *MemoryStore) PutRhythm(_ context.Context, workspace string, input WeeklyRhythm) (WeeklyRhythm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.rhythms[workspace]
	if !ok {
		current = DefaultWeeklyRhythm()
	}
	if maps.Equal(current.Targets, input.Targets) {
		current.Targets = maps.Clone(current.Targets)
		return current, nil
	}
	if input.Revision != current.Revision {
		return WeeklyRhythm{}, problem(409, "rhythm_revision_conflict")
	}
	input.Revision++
	input.Targets = maps.Clone(input.Targets)
	s.rhythms[workspace] = input
	input.Targets = maps.Clone(input.Targets)
	return input, nil
}

func (s *PostgresStore) GetRhythm(ctx context.Context, workspace string) (WeeklyRhythm, error) {
	var result WeeklyRhythm
	var raw []byte
	err := s.pool.QueryRow(ctx, "select revision, targets from weekly_rhythms where workspace_id = $1", workspace).Scan(&result.Revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultWeeklyRhythm(), nil
	}
	if err != nil {
		return WeeklyRhythm{}, unavailable(err)
	}
	if err := json.Unmarshal(raw, &result.Targets); err != nil {
		return WeeklyRhythm{}, unavailable(err)
	}
	return result, nil
}

func (s *PostgresStore) PutRhythm(ctx context.Context, workspace string, input WeeklyRhythm) (WeeklyRhythm, error) {
	result, err := database.RetrySerializable(ctx, func() (WeeklyRhythm, error) {
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return WeeklyRhythm{}, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		defaults, _ := json.Marshal(DefaultWeeklyRhythm().Targets)
		if _, err := tx.Exec(ctx, "insert into weekly_rhythms (workspace_id, revision, targets) values ($1, 0, $2) on conflict do nothing", workspace, defaults); err != nil {
			return WeeklyRhythm{}, err
		}
		var current WeeklyRhythm
		var raw []byte
		if err := tx.QueryRow(ctx, "select revision, targets from weekly_rhythms where workspace_id = $1 for update", workspace).Scan(&current.Revision, &raw); err != nil {
			return WeeklyRhythm{}, err
		}
		if err := json.Unmarshal(raw, &current.Targets); err != nil {
			return WeeklyRhythm{}, err
		}
		if !maps.Equal(current.Targets, input.Targets) {
			if input.Revision != current.Revision {
				return WeeklyRhythm{}, problem(409, "rhythm_revision_conflict")
			}
			current = WeeklyRhythm{Revision: current.Revision + 1, Targets: input.Targets}
			raw, _ = json.Marshal(current.Targets)
			if _, err := tx.Exec(ctx, "update weekly_rhythms set revision = $2, targets = $3 where workspace_id = $1", workspace, current.Revision, raw); err != nil {
				return WeeklyRhythm{}, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return WeeklyRhythm{}, err
		}
		return current, nil
	})
	if err != nil {
		return WeeklyRhythm{}, unavailable(err)
	}
	return result, nil
}
