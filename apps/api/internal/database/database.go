// Package database owns the PostgreSQL connection pool, the migrations, and the
// expiry cleanup shared by the content and auth stores.
package database

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open connects, verifies the connection, and applies any pending migrations.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// A single small instance backs this, so keep the pool modest.
	config.MaxConns = 8
	config.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

type migration struct {
	version int
	name    string
	body    string
}

// Migrate applies every migration the database has not seen, each in its own
// transaction, in version order. Applying twice is a no-op.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx,
		`create table if not exists schema_migrations (
			version    integer     primary key,
			name       text        not null,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := pool.Query(ctx, `select version from schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}

	pending, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, step := range pending {
		if applied[step.version] {
			continue
		}
		if err := applyMigration(ctx, pool, step); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, step migration) error {
	transaction, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", step.version, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(ctx, step.body); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", step.version, step.name, err)
	}
	if _, err := transaction.Exec(ctx,
		`insert into schema_migrations (version, name) values ($1, $2)`, step.version, step.name); err != nil {
		return fmt.Errorf("record migration %d: %w", step.version, err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %d: %w", step.version, err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	steps := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(name, "_")
		if !found {
			return nil, fmt.Errorf("migration %q is not named <version>_<description>.sql", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		steps = append(steps, migration{version: version, name: name, body: string(body)})
	}
	sort.Slice(steps, func(left, right int) bool { return steps[left].version < steps[right].version })
	for index := 1; index < len(steps); index++ {
		if steps[index].version == steps[index-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", steps[index].version)
		}
	}
	return steps, nil
}

// Cleanup removes rows past their expiry. Reads already filter on expires_at, so
// this reclaims space rather than enforcing correctness.
func Cleanup(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int64, error) {
	tables := []string{"content_items", "mutation_receipts", "oauth_login_attempts", "sessions", "api_token_rate_limits"}
	var removed int64
	for _, table := range tables {
		tag, err := pool.Exec(ctx, "delete from "+table+" where expires_at <= $1", now)
		if err != nil {
			return removed, fmt.Errorf("cleanup %s: %w", table, err)
		}
		removed += tag.RowsAffected()
	}
	return removed, nil
}
