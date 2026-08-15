package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	contentItemsCollection = "content_items"
	sectionsCollection     = "sections"
	receiptsCollection     = "mutation_receipts"
)

type receiptDocument struct {
	WorkspaceID  string      `firestore:"workspace_id"`
	RequestHash  string      `firestore:"request_hash"`
	Operation    string      `firestore:"operation"`
	HTTPStatus   int         `firestore:"http_status"`
	OperationID  string      `firestore:"operation_id"`
	ItemIDs      []string    `firestore:"item_ids"`
	Revisions    []int64     `firestore:"revisions"`
	ResultExpiry []time.Time `firestore:"result_expires_at"`
	Status       string      `firestore:"status"`
	ErrorCode    string      `firestore:"error_code,omitempty"`
	ExpiresAt    time.Time   `firestore:"expires_at"`
}

type FirestoreStore struct{ client *firestore.Client }

func NewFirestoreStore(client *firestore.Client) *FirestoreStore {
	return &FirestoreStore{client: client}
}

func (s *FirestoreStore) Receipt(ctx context.Context, workspaceID, operationID, requestHash string, now time.Time) (MutationResult, bool, error) {
	snapshot, err := s.receiptRef(workspaceID, operationID).Get(ctx)
	if firestoreNotFound(err) {
		return MutationResult{}, false, nil
	}
	if err != nil {
		return MutationResult{}, false, unavailable(err)
	}
	var document receiptDocument
	if err := snapshot.DataTo(&document); err != nil {
		return MutationResult{}, false, unavailable(err)
	}
	if document.WorkspaceID != workspaceID || !document.ExpiresAt.After(now) {
		return MutationResult{}, false, nil
	}
	if document.RequestHash != requestHash {
		return MutationResult{}, true, operationConflict()
	}
	return document.result(), true, nil
}

func (s *FirestoreStore) Create(ctx context.Context, item Item, receipt Receipt) (MutationResult, error) {
	parent, err := toParentDocument(item)
	if err != nil {
		return MutationResult{}, unavailable(err)
	}
	var result MutationResult
	err = s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		if replay, found, err := s.transactionReceipt(transaction, receipt); found || err != nil {
			result = replay
			return err
		}
		itemRef := s.client.Collection(contentItemsCollection).Doc(item.ID)
		if _, err := transaction.Get(itemRef); err == nil {
			return unavailable(errIDCollision{})
		} else if !firestoreNotFound(err) {
			return err
		}
		if err := transaction.Create(itemRef, parent); err != nil {
			return err
		}
		if youtube, ok := item.Content.(YouTubeContent); ok {
			for _, section := range youtube.Sections {
				document := sectionDocument{Position: section.Position, Title: section.Title, Body: section.Body, WorkspaceID: item.WorkspaceID, ExpiresAt: item.ExpiresAt}
				if err := transaction.Create(itemRef.Collection(sectionsCollection).Doc(section.ID), document); err != nil {
					return err
				}
			}
		}
		if err := transaction.Set(s.receiptRef(receipt.WorkspaceID, receipt.OperationID), receiptToDocument(receipt)); err != nil {
			return err
		}
		result = receipt.MutationResult
		return nil
	})
	if err != nil {
		return MutationResult{}, translateFirestoreError(err)
	}
	return result, nil
}

func (s *FirestoreStore) Replace(ctx context.Context, item Item, revision int64, receipt Receipt) (MutationResult, error) {
	parent, err := toParentDocument(item)
	if err != nil {
		return MutationResult{}, unavailable(err)
	}
	var result MutationResult
	err = s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		if replay, found, err := s.transactionReceipt(transaction, receipt); found || err != nil {
			result = replay
			return err
		}
		itemRef := s.client.Collection(contentItemsCollection).Doc(item.ID)
		current, sections, err := s.transactionItem(transaction, itemRef, item.WorkspaceID, item.UpdatedAt)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return conflict(current)
		}
		if current.Type != item.Type {
			return problem(400, "invalid_discriminator")
		}
		if err := validateSectionReplacement(current, item); err != nil {
			return err
		}
		if err := transaction.Set(itemRef, parent); err != nil {
			return err
		}
		replacementSections := map[string]Section{}
		if youtube, ok := item.Content.(YouTubeContent); ok {
			for _, section := range youtube.Sections {
				replacementSections[section.ID] = section
				document := sectionDocument{Position: section.Position, Title: section.Title, Body: section.Body, WorkspaceID: item.WorkspaceID, ExpiresAt: item.ExpiresAt}
				if err := transaction.Set(itemRef.Collection(sectionsCollection).Doc(section.ID), document); err != nil {
					return err
				}
			}
		}
		for _, section := range sections {
			if _, survives := replacementSections[section.ID]; !survives {
				if err := transaction.Delete(itemRef.Collection(sectionsCollection).Doc(section.ID)); err != nil {
					return err
				}
			}
		}
		if err := transaction.Set(s.receiptRef(receipt.WorkspaceID, receipt.OperationID), receiptToDocument(receipt)); err != nil {
			return err
		}
		result = receipt.MutationResult
		return nil
	})
	if err != nil {
		return MutationResult{}, translateFirestoreError(err)
	}
	return result, nil
}

func (s *FirestoreStore) SetArchived(ctx context.Context, workspaceID, id string, revision int64, archived bool, now time.Time, receipt Receipt) (MutationResult, error) {
	var result MutationResult
	err := s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		if replay, found, err := s.transactionReceipt(transaction, receipt); found || err != nil {
			result = replay
			return err
		}
		itemRef := s.client.Collection(contentItemsCollection).Doc(id)
		current, _, err := s.transactionItem(transaction, itemRef, workspaceID, now)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return conflict(current)
		}
		updates := []firestore.Update{{Path: "revision", Value: revision + 1}, {Path: "updated_at", Value: now}}
		if archived {
			updates = append(updates, firestore.Update{Path: "archived_at", Value: now})
		} else {
			updates = append(updates, firestore.Update{Path: "archived_at", Value: firestore.Delete})
		}
		if err := transaction.Update(itemRef, updates); err != nil {
			return err
		}
		if err := transaction.Set(s.receiptRef(receipt.WorkspaceID, receipt.OperationID), receiptToDocument(receipt)); err != nil {
			return err
		}
		result = receipt.MutationResult
		return nil
	})
	if err != nil {
		return MutationResult{}, translateFirestoreError(err)
	}
	return result, nil
}

func (s *FirestoreStore) Delete(ctx context.Context, workspaceID, id string, revision int64, now time.Time, receipt Receipt) (MutationResult, error) {
	var result MutationResult
	err := s.client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		if replay, found, err := s.transactionReceipt(transaction, receipt); found || err != nil {
			result = replay
			return err
		}
		itemRef := s.client.Collection(contentItemsCollection).Doc(id)
		current, sections, err := s.transactionItem(transaction, itemRef, workspaceID, now)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return conflict(current)
		}
		for _, section := range sections {
			if err := transaction.Delete(itemRef.Collection(sectionsCollection).Doc(section.ID)); err != nil {
				return err
			}
		}
		if err := transaction.Delete(itemRef); err != nil {
			return err
		}
		if err := transaction.Set(s.receiptRef(receipt.WorkspaceID, receipt.OperationID), receiptToDocument(receipt)); err != nil {
			return err
		}
		result = receipt.MutationResult
		return nil
	})
	if err != nil {
		return MutationResult{}, translateFirestoreError(err)
	}
	return result, nil
}

func (s *FirestoreStore) Get(ctx context.Context, workspaceID, id string, now time.Time) (Item, error) {
	snapshot, err := s.client.Collection(contentItemsCollection).Doc(id).Get(ctx)
	if firestoreNotFound(err) {
		return Item{}, notFound()
	}
	if err != nil {
		return Item{}, unavailable(err)
	}
	item, err := decodeParent(snapshot)
	if err != nil {
		return Item{}, unavailable(err)
	}
	if item.WorkspaceID != workspaceID || !item.ExpiresAt.After(now) {
		return Item{}, notFound()
	}
	if item.Type == TypeYouTube {
		sections, err := readSections(snapshot.Ref.Collection(sectionsCollection).Documents(ctx), workspaceID, item.ExpiresAt)
		if err != nil {
			return Item{}, unavailable(err)
		}
		youtube := item.Content.(YouTubeContent)
		youtube.Sections = sections
		item.Content = youtube
	}
	return item, nil
}

func (s *FirestoreStore) List(ctx context.Context, workspaceID string, filter ListQuery, now time.Time) ([]Summary, error) {
	query := s.client.Collection(contentItemsCollection).Where("workspace_id", "==", workspaceID)
	if filter.TitlePrefix != "" {
		searchPrefix := SearchableTitle(filter.TitlePrefix)
		if len(searchPrefix) < len(filter.TitlePrefix) {
			query = query.Where("normalized_working_title", "==", searchPrefix)
		} else {
			query = query.Where("normalized_working_title", ">=", searchPrefix)
			if successor, exists := titlePrefixSuccessor(searchPrefix); exists {
				query = query.Where("normalized_working_title", "<", successor)
			}
		}
	} else if filter.Type != "" && filter.Status == "" {
		query = query.Where("type", "==", filter.Type)
	} else if filter.Status != "" && filter.Type == "" {
		query = query.Where("status", "==", filter.Status)
	}
	query = query.Where("expires_at", ">", now)
	iterator := query.Documents(ctx)
	defer iterator.Stop()
	items := make([]Summary, 0)
	for {
		snapshot, err := iterator.Next()
		if err == iteratorDone {
			break
		}
		if err != nil {
			return nil, unavailable(err)
		}
		item, err := decodeParent(snapshot)
		if err != nil {
			return nil, unavailable(err)
		}
		if item.WorkspaceID != workspaceID || !item.ExpiresAt.After(now) {
			continue
		}
		if filter.Type != "" && item.Type != filter.Type {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.TitlePrefix != "" && !strings.HasPrefix(item.NormalizedWorkingTitle, filter.TitlePrefix) {
			continue
		}
		items = append(items, item.Summary())
	}
	return items, nil
}

var iteratorDone = iterator.Done

func (s *FirestoreStore) transactionReceipt(transaction *firestore.Transaction, receipt Receipt) (MutationResult, bool, error) {
	snapshot, err := transaction.Get(s.receiptRef(receipt.WorkspaceID, receipt.OperationID))
	if firestoreNotFound(err) {
		return MutationResult{}, false, nil
	}
	if err != nil {
		return MutationResult{}, false, err
	}
	var document receiptDocument
	if err := snapshot.DataTo(&document); err != nil {
		return MutationResult{}, false, err
	}
	now := receipt.Expires.Add(-ReceiptLifetime)
	if document.WorkspaceID != receipt.WorkspaceID || !document.ExpiresAt.After(now) {
		return MutationResult{}, false, nil
	}
	if document.RequestHash != receipt.RequestHash {
		return MutationResult{}, true, operationConflict()
	}
	return document.result(), true, nil
}

func (s *FirestoreStore) transactionItem(transaction *firestore.Transaction, reference *firestore.DocumentRef, workspaceID string, now time.Time) (Item, []Section, error) {
	snapshot, err := transaction.Get(reference)
	if firestoreNotFound(err) {
		return Item{}, nil, notFound()
	}
	if err != nil {
		return Item{}, nil, err
	}
	item, err := decodeParent(snapshot)
	if err != nil {
		return Item{}, nil, err
	}
	if item.WorkspaceID != workspaceID || !item.ExpiresAt.After(now) {
		return Item{}, nil, notFound()
	}
	sections := []Section{}
	if item.Type == TypeYouTube {
		sections, err = readSections(transaction.Documents(reference.Collection(sectionsCollection)), workspaceID, item.ExpiresAt)
		if err != nil {
			return Item{}, nil, err
		}
		youtube := item.Content.(YouTubeContent)
		youtube.Sections = sections
		item.Content = youtube
	}
	return item, sections, nil
}

type snapshotIterator interface {
	GetAll() ([]*firestore.DocumentSnapshot, error)
}

func readSections(iterator snapshotIterator, workspaceID string, expiresAt time.Time) ([]Section, error) {
	snapshots, err := iterator.GetAll()
	if err != nil {
		return nil, err
	}
	sections := make([]Section, len(snapshots))
	for index, snapshot := range snapshots {
		var document sectionDocument
		if err := snapshot.DataTo(&document); err != nil {
			return nil, err
		}
		if document.WorkspaceID != workspaceID || !document.ExpiresAt.Equal(expiresAt) {
			return nil, fmt.Errorf("section scope or expiry mismatch")
		}
		sections[index] = Section{ID: snapshot.Ref.ID, Position: document.Position, Title: document.Title, Body: document.Body}
	}
	sort.Slice(sections, func(left, right int) bool { return sections[left].Position < sections[right].Position })
	seen := make(map[string]struct{}, len(sections))
	for position, section := range sections {
		if section.Position != position {
			return nil, fmt.Errorf("non-contiguous section positions")
		}
		if _, duplicate := seen[section.ID]; duplicate {
			return nil, fmt.Errorf("duplicate section ID")
		}
		seen[section.ID] = struct{}{}
	}
	return sections, nil
}

func decodeParent(snapshot *firestore.DocumentSnapshot) (Item, error) {
	var document parentDocument
	if err := snapshot.DataTo(&document); err != nil {
		return Item{}, err
	}
	if !validType(document.Type) || !validStatus(document.Status) || document.Revision < 1 {
		return Item{}, fmt.Errorf("invalid stored content metadata")
	}
	contentValue, err := contentFromMap(document.Type, document.Content)
	if err != nil {
		return Item{}, err
	}
	return Item{
		ID: snapshot.Ref.ID, WorkspaceID: document.WorkspaceID, Type: document.Type, Status: document.Status,
		WorkingTitle: document.WorkingTitle, NormalizedWorkingTitle: NormalizeTitle(document.WorkingTitle),
		SearchableWorkingTitle: document.NormalizedWorkingTitle,
		Revision:               document.Revision, CreatedAt: document.CreatedAt, UpdatedAt: document.UpdatedAt,
		ExpiresAt: document.ExpiresAt, ArchivedAt: document.ArchivedAt, Content: contentValue,
	}, nil
}

func receiptToDocument(receipt Receipt) receiptDocument {
	return receiptDocument{
		WorkspaceID: receipt.WorkspaceID, RequestHash: receipt.RequestHash, Operation: receipt.Operation,
		HTTPStatus: receipt.HTTPStatus, OperationID: receipt.OperationID, ItemIDs: append([]string(nil), receipt.ItemIDs...),
		Revisions: append([]int64(nil), receipt.Revisions...), ResultExpiry: append([]time.Time(nil), receipt.ExpiresAt...),
		Status: receipt.Status, ErrorCode: receipt.ErrorCode, ExpiresAt: receipt.Expires,
	}
}

func (d receiptDocument) result() MutationResult {
	return MutationResult{OperationID: d.OperationID, ItemIDs: append([]string(nil), d.ItemIDs...), Revisions: append([]int64(nil), d.Revisions...), ExpiresAt: append([]time.Time(nil), d.ResultExpiry...), Status: d.Status}
}

func (s *FirestoreStore) receiptRef(workspaceID, operationID string) *firestore.DocumentRef {
	hash := sha256.Sum256([]byte(workspaceID + "\x00" + operationID))
	return s.client.Collection(receiptsCollection).Doc(hex.EncodeToString(hash[:]))
}

func firestoreNotFound(err error) bool { return status.Code(err) == codes.NotFound }

func translateFirestoreError(err error) error {
	var contentError *Error
	if errors.As(err, &contentError) {
		return err
	}
	return unavailable(err)
}
