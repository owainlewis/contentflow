package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const serializableAttempts = 4

// RetrySerializable reruns a complete transaction after PostgreSQL reports a
// serialization failure or deadlock. Each attempt must begin a fresh
// transaction so PostgreSQL can take a new snapshot.
func RetrySerializable[T any](ctx context.Context, attempt func() (T, error)) (T, error) {
	var zero T
	for index := 0; index < serializableAttempts; index++ {
		result, err := attempt()
		if err == nil || !IsRetryableTransactionError(err) || index == serializableAttempts-1 {
			return result, err
		}
		delay := time.Duration(1<<index) * 5 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, nil
}

func IsRetryableTransactionError(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	return postgresError.Code == "40001" || postgresError.Code == "40P01"
}
