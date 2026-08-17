package content

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore persists content in PostgreSQL. Sections live inside the content
// JSONB rather than in a side table: they were only ever split out to dodge the
// Firestore document size limit.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const itemColumns = `id, workspace_id, type, status, working_title, normalized_working_title,
	revision, created_at, updated_at, expires_at, scheduled_at, content`

func (s *PostgresStore) Receipt(ctx context.Context, workspaceID, operationID, requestHash string, now time.Time) (MutationResult, bool, error) {
	row := s.pool.QueryRow(ctx,
		`select request_hash, result, expires_at from mutation_receipts where workspace_id = $1 and operation_id = $2`,
		workspaceID, operationID)
	var storedHash string
	var encoded []byte
	var expires time.Time
	switch err := row.Scan(&storedHash, &encoded, &expires); {
	case errors.Is(err, pgx.ErrNoRows):
		return MutationResult{}, false, nil
	case err != nil:
		return MutationResult{}, false, unavailable(err)
	}
	if !expires.After(now) {
		return MutationResult{}, false, nil
	}
	if storedHash != requestHash {
		return MutationResult{}, true, operationConflict()
	}
	var result MutationResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return MutationResult{}, false, unavailable(err)
	}
	return result, true, nil
}

// inTransaction runs body in a serializable transaction, replaying a matching
// receipt before any work so a retried request never mutates twice.
func (s *PostgresStore) inTransaction(ctx context.Context, receipt Receipt, body func(pgx.Tx) error) (MutationResult, error) {
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return MutationResult{}, unavailable(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	replay, found, err := transactionReceipt(ctx, transaction, receipt)
	if err != nil {
		return MutationResult{}, err
	}
	if found {
		return replay, nil
	}
	if err := body(transaction); err != nil {
		return MutationResult{}, err
	}
	if err := writeReceipt(ctx, transaction, receipt); err != nil {
		return MutationResult{}, unavailable(err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return MutationResult{}, unavailable(err)
	}
	return receipt.MutationResult, nil
}

func transactionReceipt(ctx context.Context, transaction pgx.Tx, receipt Receipt) (MutationResult, bool, error) {
	row := transaction.QueryRow(ctx,
		`select request_hash, result, expires_at from mutation_receipts where workspace_id = $1 and operation_id = $2`,
		receipt.WorkspaceID, receipt.OperationID)
	var storedHash string
	var encoded []byte
	var expires time.Time
	switch err := row.Scan(&storedHash, &encoded, &expires); {
	case errors.Is(err, pgx.ErrNoRows):
		return MutationResult{}, false, nil
	case err != nil:
		return MutationResult{}, false, unavailable(err)
	}
	if !expires.After(receipt.Expires.Add(-ReceiptLifetime)) {
		return MutationResult{}, false, nil
	}
	if storedHash != receipt.RequestHash {
		return MutationResult{}, true, operationConflict()
	}
	var result MutationResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return MutationResult{}, false, unavailable(err)
	}
	return result, true, nil
}

func writeReceipt(ctx context.Context, transaction pgx.Tx, receipt Receipt) error {
	encoded, err := json.Marshal(receipt.MutationResult)
	if err != nil {
		return err
	}
	_, err = transaction.Exec(ctx,
		`insert into mutation_receipts (workspace_id, operation_id, request_hash, operation, http_status, result, error_code, expires_at)
		 values ($1, $2, $3, $4, $5, $6, $7, $8)
		 on conflict (workspace_id, operation_id) do update set
		   request_hash = excluded.request_hash, operation = excluded.operation,
		   http_status = excluded.http_status, result = excluded.result,
		   error_code = excluded.error_code, expires_at = excluded.expires_at`,
		receipt.WorkspaceID, receipt.OperationID, receipt.RequestHash, receipt.Operation,
		receipt.HTTPStatus, encoded, receipt.ErrorCode, receipt.Expires)
	return err
}

func insertItem(ctx context.Context, transaction pgx.Tx, item Item) error {
	encoded, err := json.Marshal(item.Content)
	if err != nil {
		return unavailable(err)
	}
	tag, err := transaction.Exec(ctx,
		`insert into content_items (`+itemColumns+`)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 on conflict (id) do nothing`,
		item.ID, item.WorkspaceID, item.Type, item.Status, item.WorkingTitle, item.SearchableWorkingTitle,
		item.Revision, item.CreatedAt, item.UpdatedAt, item.ExpiresAt, item.ScheduledAt, encoded)
	if err != nil {
		return unavailable(err)
	}
	if tag.RowsAffected() == 0 {
		return unavailable(errIDCollision{})
	}
	return nil
}

func (s *PostgresStore) Create(ctx context.Context, item Item, receipt Receipt) (MutationResult, error) {
	return s.inTransaction(ctx, receipt, func(transaction pgx.Tx) error {
		return insertItem(ctx, transaction, item)
	})
}

func (s *PostgresStore) BatchCreate(ctx context.Context, items []Item, receipt Receipt) (MutationResult, error) {
	return s.inTransaction(ctx, receipt, func(transaction pgx.Tx) error {
		for _, item := range items {
			if err := insertItem(ctx, transaction, item); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *PostgresStore) Replace(ctx context.Context, item Item, revision int64, receipt Receipt) (MutationResult, error) {
	return s.inTransaction(ctx, receipt, func(transaction pgx.Tx) error {
		current, err := lockItem(ctx, transaction, item.WorkspaceID, item.ID, item.UpdatedAt)
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
		encoded, err := json.Marshal(item.Content)
		if err != nil {
			return unavailable(err)
		}
		_, err = transaction.Exec(ctx,
			`update content_items set type = $3, status = $4, working_title = $5, normalized_working_title = $6,
			   revision = $7, updated_at = $8, expires_at = $9, scheduled_at = $10, content = $11
			 where id = $1 and workspace_id = $2`,
			item.ID, item.WorkspaceID, item.Type, item.Status, item.WorkingTitle, item.SearchableWorkingTitle,
			item.Revision, item.UpdatedAt, item.ExpiresAt, item.ScheduledAt, encoded)
		if err != nil {
			return unavailable(err)
		}
		return nil
	})
}

func (s *PostgresStore) Delete(ctx context.Context, workspaceID, id string, revision int64, now time.Time, receipt Receipt) (MutationResult, error) {
	return s.inTransaction(ctx, receipt, func(transaction pgx.Tx) error {
		current, err := lockItem(ctx, transaction, workspaceID, id, now)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return conflict(current)
		}
		if _, err := transaction.Exec(ctx, `delete from content_items where id = $1 and workspace_id = $2`, id, workspaceID); err != nil {
			return unavailable(err)
		}
		return nil
	})
}

// lockItem reads the row for update so a concurrent mutation on the same item
// serialises behind this transaction rather than racing it.
func lockItem(ctx context.Context, transaction pgx.Tx, workspaceID, id string, now time.Time) (Item, error) {
	row := transaction.QueryRow(ctx,
		`select `+itemColumns+` from content_items where id = $1 and workspace_id = $2 for update`,
		id, workspaceID)
	item, err := scanItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, notFound()
	}
	if err != nil {
		return Item{}, unavailable(err)
	}
	if !item.ExpiresAt.After(now) {
		return Item{}, notFound()
	}
	return item, nil
}

func (s *PostgresStore) Get(ctx context.Context, workspaceID, id string, now time.Time) (Item, error) {
	row := s.pool.QueryRow(ctx, `select `+itemColumns+` from content_items where id = $1 and workspace_id = $2`, id, workspaceID)
	item, err := scanItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, notFound()
	}
	if err != nil {
		return Item{}, unavailable(err)
	}
	if !item.ExpiresAt.After(now) {
		return Item{}, notFound()
	}
	return item, nil
}

func (s *PostgresStore) List(ctx context.Context, workspaceID string, filter ListQuery, now time.Time) ([]Summary, error) {
	query := strings.Builder{}
	query.WriteString(`select ` + itemColumns + ` from content_items where workspace_id = $1 and expires_at > $2`)
	arguments := []any{workspaceID, now}
	if filter.Type != "" {
		arguments = append(arguments, filter.Type)
		query.WriteString(` and type = $` + itoa(len(arguments)))
	}
	if filter.Status != "" {
		arguments = append(arguments, filter.Status)
		query.WriteString(` and status = $` + itoa(len(arguments)))
	}
	if filter.TitlePrefix != "" {
		arguments = append(arguments, SearchableTitle(filter.TitlePrefix)+"%")
		query.WriteString(` and normalized_working_title like $` + itoa(len(arguments)))
	}
	query.WriteString(` order by updated_at desc`)

	rows, err := s.pool.Query(ctx, query.String(), arguments...)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()

	summaries := make([]Summary, 0)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, unavailable(err)
		}
		summaries = append(summaries, item.Summary())
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return summaries, nil
}

type scanner interface {
	Scan(destination ...any) error
}

func scanItem(source scanner) (Item, error) {
	var item Item
	var encoded []byte
	if err := source.Scan(&item.ID, &item.WorkspaceID, &item.Type, &item.Status, &item.WorkingTitle,
		&item.NormalizedWorkingTitle, &item.Revision, &item.CreatedAt, &item.UpdatedAt,
		&item.ExpiresAt, &item.ScheduledAt, &encoded); err != nil {
		return Item{}, err
	}
	contentValue, err := decodeTypedContent(item.Type, encoded)
	if err != nil {
		return Item{}, err
	}
	item.Content = contentValue
	item.SearchableWorkingTitle = item.NormalizedWorkingTitle
	return item, nil
}

func itoa(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
