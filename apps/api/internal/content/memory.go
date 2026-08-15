package content

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.Mutex
	items    map[string]Item
	receipts map[string]Receipt
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]Item), receipts: make(map[string]Receipt)}
}

func memoryKey(workspaceID, id string) string { return workspaceID + "\x00" + id }

func (s *MemoryStore) Receipt(_ context.Context, workspaceID, operationID, requestHash string, now time.Time) (MutationResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, found := s.receipts[memoryKey(workspaceID, operationID)]
	if !found || !receipt.Expires.After(now) {
		return MutationResult{}, false, nil
	}
	if receipt.RequestHash != requestHash {
		return MutationResult{}, true, operationConflict()
	}
	return cloneResult(receipt.MutationResult), true, nil
}

func (s *MemoryStore) Create(_ context.Context, item Item, receipt Receipt) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, found, err := s.receiptLocked(receipt); found || err != nil {
		return result, err
	}
	key := memoryKey(item.WorkspaceID, item.ID)
	if _, found := s.items[key]; found {
		return MutationResult{}, unavailable(errIDCollision{})
	}
	s.items[key] = cloneItem(item)
	s.receipts[memoryKey(receipt.WorkspaceID, receipt.OperationID)] = cloneReceipt(receipt)
	return cloneResult(receipt.MutationResult), nil
}

func (s *MemoryStore) BatchCreate(_ context.Context, items []Item, receipt Receipt) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, found, err := s.receiptLocked(receipt); found || err != nil {
		return result, err
	}
	for _, item := range items {
		if _, found := s.items[memoryKey(item.WorkspaceID, item.ID)]; found {
			return MutationResult{}, unavailable(errIDCollision{})
		}
	}
	for _, item := range items {
		s.items[memoryKey(item.WorkspaceID, item.ID)] = cloneItem(item)
	}
	s.receipts[memoryKey(receipt.WorkspaceID, receipt.OperationID)] = cloneReceipt(receipt)
	return cloneResult(receipt.MutationResult), nil
}

func (s *MemoryStore) Replace(_ context.Context, item Item, revision int64, receipt Receipt) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, found, err := s.receiptLocked(receipt); found || err != nil {
		return result, err
	}
	key := memoryKey(item.WorkspaceID, item.ID)
	current, found := s.items[key]
	if !found || !current.ExpiresAt.After(item.UpdatedAt) {
		return MutationResult{}, notFound()
	}
	if current.Revision != revision {
		current = cloneItem(current)
		return MutationResult{}, conflict(current)
	}
	if current.Type != item.Type {
		return MutationResult{}, problem(400, "invalid_discriminator")
	}
	if err := validateSectionReplacement(current, item); err != nil {
		return MutationResult{}, err
	}
	s.items[key] = cloneItem(item)
	s.receipts[memoryKey(receipt.WorkspaceID, receipt.OperationID)] = cloneReceipt(receipt)
	return cloneResult(receipt.MutationResult), nil
}

func (s *MemoryStore) SetArchived(_ context.Context, workspaceID, id string, revision int64, archived bool, now time.Time, receipt Receipt) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, found, err := s.receiptLocked(receipt); found || err != nil {
		return result, err
	}
	key := memoryKey(workspaceID, id)
	current, found := s.items[key]
	if !found || !current.ExpiresAt.After(now) {
		return MutationResult{}, notFound()
	}
	if current.Revision != revision {
		current = cloneItem(current)
		return MutationResult{}, conflict(current)
	}
	current.Revision++
	current.UpdatedAt = now
	if archived {
		archivedAt := now
		current.ArchivedAt = &archivedAt
	} else {
		current.ArchivedAt = nil
	}
	s.items[key] = current
	s.receipts[memoryKey(receipt.WorkspaceID, receipt.OperationID)] = cloneReceipt(receipt)
	return cloneResult(receipt.MutationResult), nil
}

func (s *MemoryStore) Delete(_ context.Context, workspaceID, id string, revision int64, now time.Time, receipt Receipt) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, found, err := s.receiptLocked(receipt); found || err != nil {
		return result, err
	}
	key := memoryKey(workspaceID, id)
	current, found := s.items[key]
	if !found || !current.ExpiresAt.After(now) {
		return MutationResult{}, notFound()
	}
	if current.Revision != revision {
		current = cloneItem(current)
		return MutationResult{}, conflict(current)
	}
	delete(s.items, key)
	s.receipts[memoryKey(receipt.WorkspaceID, receipt.OperationID)] = cloneReceipt(receipt)
	return cloneResult(receipt.MutationResult), nil
}

func (s *MemoryStore) Get(_ context.Context, workspaceID, id string, now time.Time) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, found := s.items[memoryKey(workspaceID, id)]
	if !found || !item.ExpiresAt.After(now) {
		return Item{}, notFound()
	}
	return cloneItem(item), nil
}

func (s *MemoryStore) List(_ context.Context, workspaceID string, query ListQuery, now time.Time) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Summary, 0)
	prefix := workspaceID + "\x00"
	for key, item := range s.items {
		if !strings.HasPrefix(key, prefix) || !item.ExpiresAt.After(now) {
			continue
		}
		if query.Type != "" && item.Type != query.Type {
			continue
		}
		if query.Status != "" && item.Status != query.Status {
			continue
		}
		if query.TitlePrefix != "" && !strings.HasPrefix(item.NormalizedWorkingTitle, query.TitlePrefix) {
			continue
		}
		items = append(items, item.Summary())
	}
	sort.SliceStable(items, func(left, right int) bool { return items[left].UpdatedAt.After(items[right].UpdatedAt) })
	return items, nil
}

func (s *MemoryStore) receiptLocked(receipt Receipt) (MutationResult, bool, error) {
	existing, found := s.receipts[memoryKey(receipt.WorkspaceID, receipt.OperationID)]
	if !found || !existing.Expires.After(receipt.Expires.Add(-ReceiptLifetime)) {
		return MutationResult{}, false, nil
	}
	if existing.RequestHash != receipt.RequestHash {
		return MutationResult{}, true, operationConflict()
	}
	return cloneResult(existing.MutationResult), true, nil
}

func validateSectionReplacement(current, replacement Item) error {
	before, beforeOK := current.Content.(YouTubeContent)
	after, afterOK := replacement.Content.(YouTubeContent)
	if !beforeOK && !afterOK {
		return nil
	}
	if !beforeOK || !afterOK {
		return problem(400, "invalid_discriminator")
	}
	existing := make(map[string]struct{}, len(before.Sections))
	for _, section := range before.Sections {
		existing[section.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(after.Sections))
	for index, section := range after.Sections {
		if section.Position != index {
			return problem(400, "invalid_section_order")
		}
		if _, duplicate := seen[section.ID]; duplicate {
			return problem(400, "duplicate_section_id")
		}
		seen[section.ID] = struct{}{}
		if _, found := existing[section.ID]; !found {
			// New section IDs were generated by the service and are allowed. A caller-
			// supplied unknown ID is rejected before the transaction by assignSectionIDs.
			continue
		}
	}
	return nil
}

func cloneItem(item Item) Item {
	copy := item
	if item.ArchivedAt != nil {
		archived := *item.ArchivedAt
		copy.ArchivedAt = &archived
	}
	if youtube, ok := item.Content.(YouTubeContent); ok {
		youtube.Sections = append([]Section(nil), youtube.Sections...)
		copy.Content = youtube
	}
	return copy
}

func cloneResult(result MutationResult) MutationResult {
	result.ItemIDs = append([]string(nil), result.ItemIDs...)
	result.Revisions = append([]int64(nil), result.Revisions...)
	result.ExpiresAt = append([]time.Time(nil), result.ExpiresAt...)
	return result
}

func cloneReceipt(receipt Receipt) Receipt {
	receipt.MutationResult = cloneResult(receipt.MutationResult)
	return receipt
}

type errIDCollision struct{}

func (errIDCollision) Error() string { return "generated content ID already exists" }
