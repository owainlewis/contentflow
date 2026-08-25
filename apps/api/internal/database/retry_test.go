package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetrySerializableRetriesOnlyTransientPostgresFailures(t *testing.T) {
	attempts := 0
	result, err := RetrySerializable(context.Background(), func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", &pgconn.PgError{Code: "40001"}
		}
		return "ok", nil
	})
	if err != nil || result != "ok" || attempts != 3 {
		t.Fatalf("retry result = %q, %v after %d attempts", result, err, attempts)
	}

	permanent := errors.New("permanent")
	attempts = 0
	_, err = RetrySerializable(context.Background(), func() (string, error) {
		attempts++
		return "", permanent
	})
	if !errors.Is(err, permanent) || attempts != 1 {
		t.Fatalf("permanent failure was retried %d times: %v", attempts, err)
	}
}
