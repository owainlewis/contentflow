package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/database"
)

// createUser adds or updates a password account. The password is read from the
// environment rather than an argument so it never lands in shell history, a
// process listing, or a Cloud Run job's stored configuration.
func createUser(arguments []string) error {
	if len(arguments) != 1 {
		return errors.New("usage: contentflow createuser <email> (password read from CONTENTFLOW_NEW_USER_PASSWORD)")
	}
	email := auth.NormalizeEmail(arguments[0])
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("a valid email address is required")
	}
	password := os.Getenv("CONTENTFLOW_NEW_USER_PASSWORD")
	if len(password) < 12 {
		return errors.New("CONTENTFLOW_NEW_USER_PASSWORD must be at least 12 characters")
	}
	databaseURL := os.Getenv("CONTENTFLOW_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("CONTENTFLOW_DATABASE_URL is required")
	}
	workspaceID := os.Getenv("CONTENTFLOW_WORKSPACE_ID")
	if workspaceID == "" {
		return errors.New("CONTENTFLOW_WORKSPACE_ID is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user := auth.User{
		ID: ulid.Make().String(), WorkspaceID: workspaceID,
		Email: email, PasswordHash: hash, CreatedAt: time.Now().UTC(),
	}
	if err := auth.NewPostgresStore(pool).SaveUser(ctx, user); err != nil {
		return err
	}
	fmt.Printf("account ready for %s in workspace %s\n", email, workspaceID)
	return nil
}
