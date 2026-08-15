package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

type Store interface {
	Receipt(context.Context, string, string, string, time.Time) (MutationResult, bool, error)
	Create(context.Context, Item, Receipt) (MutationResult, error)
	Replace(context.Context, Item, int64, Receipt) (MutationResult, error)
	SetArchived(context.Context, string, string, int64, bool, time.Time, Receipt) (MutationResult, error)
	Delete(context.Context, string, string, int64, time.Time, Receipt) (MutationResult, error)
	Get(context.Context, string, string, time.Time) (Item, error)
	List(context.Context, string, ListQuery, time.Time) ([]Summary, error)
}

type Service struct {
	store Store
	now   func() time.Time
	ids   *idGenerator
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, ids: newIDGenerator()}
}

func RequestHash(method, path string, body []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(method))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(path))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Service) Create(ctx context.Context, workspaceID string, request CreateRequest, requestHash string) (MutationResult, error) {
	if err := validateRequest(request); err != nil {
		return MutationResult{}, err
	}
	now := s.canonicalNow()
	if replay, found, err := s.store.Receipt(ctx, workspaceID, request.OperationID, requestHash, now); found || err != nil {
		return replay, err
	}
	id, err := s.ids.New(now)
	if err != nil {
		return MutationResult{}, &Error{Status: 500, Code: "id_generation_failed", Cause: err}
	}
	contentValue, err := s.assignSectionIDs(request.Content, nil, now)
	if err != nil {
		return MutationResult{}, err
	}
	item := Item{
		ID: id, WorkspaceID: workspaceID, Type: request.Type, Status: request.Status,
		WorkingTitle: request.WorkingTitle, NormalizedWorkingTitle: NormalizeTitle(request.WorkingTitle),
		Revision: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(ContentLifetime), Content: contentValue,
	}
	item.SearchableWorkingTitle = SearchableTitle(item.NormalizedWorkingTitle)
	if err := validateEncodedSizes(item); err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{OperationID: request.OperationID, ItemIDs: []string{id}, Revisions: []int64{1}, ExpiresAt: []time.Time{item.ExpiresAt}, Status: "created"}
	return s.store.Create(ctx, item, newReceipt(workspaceID, requestHash, "create", http.StatusCreated, result, now))
}

func (s *Service) Replace(ctx context.Context, workspaceID, id string, request ReplaceRequest, requestHash string) (MutationResult, error) {
	if request.Revision < 1 {
		return MutationResult{}, problem(400, "invalid_revision")
	}
	if err := validateRequest(request.CreateRequest); err != nil {
		return MutationResult{}, err
	}
	now := s.canonicalNow()
	if replay, found, err := s.store.Receipt(ctx, workspaceID, request.OperationID, requestHash, now); found || err != nil {
		return replay, err
	}
	current, err := s.store.Get(ctx, workspaceID, id, now)
	if err != nil {
		return MutationResult{}, err
	}
	if current.Type != request.Type {
		return MutationResult{}, problem(400, "invalid_discriminator")
	}
	if current.Revision != request.Revision {
		return MutationResult{}, conflict(current)
	}
	contentValue, err := s.assignSectionIDs(request.Content, &current, now)
	if err != nil {
		return MutationResult{}, err
	}
	replacement := Item{
		ID: current.ID, WorkspaceID: current.WorkspaceID, Type: current.Type, Status: request.Status,
		WorkingTitle: request.WorkingTitle, NormalizedWorkingTitle: NormalizeTitle(request.WorkingTitle),
		Revision: request.Revision + 1, CreatedAt: current.CreatedAt, UpdatedAt: now,
		ExpiresAt: current.ExpiresAt, ArchivedAt: current.ArchivedAt, Content: contentValue,
	}
	replacement.SearchableWorkingTitle = SearchableTitle(replacement.NormalizedWorkingTitle)
	if err := validateEncodedSizes(replacement); err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{OperationID: request.OperationID, ItemIDs: []string{id}, Revisions: []int64{replacement.Revision}, ExpiresAt: []time.Time{replacement.ExpiresAt}, Status: "updated"}
	return s.store.Replace(ctx, replacement, request.Revision, newReceipt(workspaceID, requestHash, "replace", http.StatusOK, result, now))
}

func (s *Service) SetArchived(ctx context.Context, workspaceID, id string, request RevisionRequest, archived bool, requestHash string) (MutationResult, error) {
	if err := validateRevisionRequest(request); err != nil {
		return MutationResult{}, err
	}
	now := s.canonicalNow()
	if replay, found, err := s.store.Receipt(ctx, workspaceID, request.OperationID, requestHash, now); found || err != nil {
		return replay, err
	}
	current, err := s.store.Get(ctx, workspaceID, id, now)
	if err != nil {
		return MutationResult{}, err
	}
	status := "restored"
	operation := "restore"
	if archived {
		status, operation = "archived", "archive"
	}
	result := MutationResult{OperationID: request.OperationID, ItemIDs: []string{id}, Revisions: []int64{request.Revision + 1}, ExpiresAt: []time.Time{current.ExpiresAt}, Status: status}
	return s.store.SetArchived(ctx, workspaceID, id, request.Revision, archived, now, newReceipt(workspaceID, requestHash, operation, http.StatusOK, result, now))
}

func (s *Service) Delete(ctx context.Context, workspaceID, id string, request RevisionRequest, requestHash string) (MutationResult, error) {
	if err := validateRevisionRequest(request); err != nil {
		return MutationResult{}, err
	}
	now := s.canonicalNow()
	if replay, found, err := s.store.Receipt(ctx, workspaceID, request.OperationID, requestHash, now); found || err != nil {
		return replay, err
	}
	current, err := s.store.Get(ctx, workspaceID, id, now)
	if err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{OperationID: request.OperationID, ItemIDs: []string{id}, Revisions: []int64{request.Revision + 1}, ExpiresAt: []time.Time{current.ExpiresAt}, Status: "deleted"}
	return s.store.Delete(ctx, workspaceID, id, request.Revision, now, newReceipt(workspaceID, requestHash, "delete", http.StatusOK, result, now))
}

func (s *Service) Get(ctx context.Context, workspaceID, id string) (Item, error) {
	return s.store.Get(ctx, workspaceID, id, s.canonicalNow())
}

func (s *Service) Transcript(ctx context.Context, workspaceID, id string) (Transcript, error) {
	item, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return Transcript{}, err
	}
	youtube, ok := item.Content.(YouTubeContent)
	if !ok {
		return Transcript{}, problem(400, "transcript_not_supported")
	}
	if transcriptMissing(youtube.Transcript) {
		return Transcript{}, problem(409, "transcript_missing")
	}
	return Transcript{ID: item.ID, Revision: item.Revision, Transcript: youtube.Transcript}, nil
}

func (s *Service) List(ctx context.Context, workspaceID string, query ListQuery) ([]Summary, error) {
	if query.Type != "" && !validType(query.Type) {
		return nil, problem(400, "invalid_type_filter")
	}
	if query.Status != "" && !validStatus(query.Status) {
		return nil, problem(400, "invalid_status_filter")
	}
	if len([]byte(query.TitlePrefix)) > MaxTextBytes {
		return nil, problem(400, "invalid_title_prefix")
	}
	query.TitlePrefix = NormalizeTitle(query.TitlePrefix)
	items, err := s.store.List(ctx, workspaceID, query, s.canonicalNow())
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].UpdatedAt.Equal(items[right].UpdatedAt) {
			return items[left].ID > items[right].ID
		}
		return items[left].UpdatedAt.After(items[right].UpdatedAt)
	})
	return items, nil
}

func (s *Service) assignSectionIDs(value any, current *Item, now time.Time) (any, error) {
	youtube, ok := value.(YouTubeContent)
	if !ok {
		return value, nil
	}
	if youtube.Sections == nil {
		youtube.Sections = []Section{}
	}
	existing := map[string]struct{}{}
	if current != nil {
		currentYouTube, valid := current.Content.(YouTubeContent)
		if !valid {
			return nil, problem(400, "invalid_discriminator")
		}
		for _, section := range currentYouTube.Sections {
			existing[section.ID] = struct{}{}
		}
	}
	for index := range youtube.Sections {
		section := &youtube.Sections[index]
		if section.ID == "" {
			id, err := s.ids.New(now)
			if err != nil {
				return nil, &Error{Status: 500, Code: "id_generation_failed", Cause: err}
			}
			section.ID = id
			continue
		}
		if current == nil {
			return nil, problem(400, "section_id_not_allowed")
		}
		if _, found := existing[section.ID]; !found {
			return nil, problem(400, "unknown_section_id")
		}
	}
	return youtube, nil
}

func newReceipt(workspaceID, requestHash, operation string, status int, result MutationResult, now time.Time) Receipt {
	return Receipt{WorkspaceID: workspaceID, RequestHash: requestHash, Operation: operation, HTTPStatus: status, MutationResult: result, Expires: now.Add(ReceiptLifetime)}
}

func (s *Service) canonicalNow() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

func unavailable(err error) error {
	var contentError *Error
	if errors.As(err, &contentError) {
		return err
	}
	return &Error{Status: 503, Code: "content_unavailable", Cause: err}
}

func conflict(current Item) error {
	return &Error{Status: 409, Code: "revision_conflict", Current: &current}
}

func operationConflict() error { return problem(409, "operation_id_conflict") }

func notFound() error { return problem(404, "content_not_found") }

func ensureWorkspace(workspaceID string) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace ID is required")
	}
	return nil
}
