package content

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) GetThumbnail(ctx context.Context, workspaceID, id string) (Thumbnail, error) {
	var thumbnail Thumbnail
	var kind Type
	var contentType *string
	err := s.pool.QueryRow(ctx, `select c.type,t.content_type,t.data from content_items c left join content_thumbnails t on t.workspace_id=c.workspace_id and t.content_id=c.id where c.workspace_id=$1 and c.id=$2`, workspaceID, id).Scan(&kind, &contentType, &thumbnail.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return Thumbnail{}, notFound()
	}
	if err != nil {
		return Thumbnail{}, unavailable(err)
	}
	if err := thumbnailType(Item{Type: kind}); err != nil {
		return Thumbnail{}, err
	}
	if contentType == nil {
		return Thumbnail{}, problem(404, "thumbnail_not_found")
	}
	thumbnail.ContentType = *contentType
	return thumbnail, nil
}

// Lock the parent for the mutation so uploads/removals cannot race deletion.
func (s *PostgresStore) mutateThumbnail(ctx context.Context, workspaceID, id string, thumbnail *Thumbnail) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return unavailable(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var kind Type
	err = tx.QueryRow(ctx, `select type from content_items where workspace_id=$1 and id=$2 for update`, workspaceID, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return unavailable(err)
	}
	if err := thumbnailType(Item{Type: kind}); err != nil {
		return err
	}
	if thumbnail == nil {
		_, err = tx.Exec(ctx, `delete from content_thumbnails where workspace_id=$1 and content_id=$2`, workspaceID, id)
	} else {
		_, err = tx.Exec(ctx, `insert into content_thumbnails(workspace_id,content_id,content_type,data) values($1,$2,$3,$4) on conflict(workspace_id,content_id) do update set content_type=excluded.content_type,data=excluded.data`, workspaceID, id, thumbnail.ContentType, thumbnail.Data)
	}
	if err != nil {
		return unavailable(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return unavailable(err)
	}
	return nil
}
func (s *PostgresStore) PutThumbnail(ctx context.Context, workspaceID, id string, thumbnail Thumbnail) error {
	return s.mutateThumbnail(ctx, workspaceID, id, &thumbnail)
}
func (s *PostgresStore) DeleteThumbnail(ctx context.Context, workspaceID, id string) error {
	return s.mutateThumbnail(ctx, workspaceID, id, nil)
}
