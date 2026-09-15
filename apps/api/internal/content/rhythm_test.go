package content

import (
	"context"
	"errors"
	"testing"
)

func checkRhythmPersistence(t *testing.T, store rhythmStore) {
	t.Helper()
	ctx := context.Background()
	initial, err := store.GetRhythm(ctx, "rhythm-a")
	if err != nil || initial.Revision != 0 || initial.Targets[TypeYouTube] != 1 || initial.Targets[TypeInstagram] != 7 {
		t.Fatalf("starter rhythm: %#v, %v", initial, err)
	}
	initial.Targets[TypeYouTube] = 2
	saved, err := store.PutRhythm(ctx, "rhythm-a", initial)
	if err != nil || saved.Revision != 1 {
		t.Fatalf("save: %#v, %v", saved, err)
	}
	// Retry the same value with the original revision after a lost response.
	replayed, err := store.PutRhythm(ctx, "rhythm-a", initial)
	if err != nil || replayed.Revision != 1 {
		t.Fatalf("retry: %#v, %v", replayed, err)
	}
	initial.Targets[TypeYouTube] = 3
	_, err = store.PutRhythm(ctx, "rhythm-a", initial)
	var conflict *Error
	if !errors.As(err, &conflict) || conflict.Status != 409 {
		t.Fatalf("stale write: %v", err)
	}
	reloaded, err := store.GetRhythm(ctx, "rhythm-a")
	if err != nil || reloaded.Targets[TypeYouTube] != 2 || reloaded.Revision != 1 {
		t.Fatalf("reload: %#v, %v", reloaded, err)
	}
	other, err := store.GetRhythm(ctx, "rhythm-b")
	if err != nil || other.Targets[TypeYouTube] != 1 || other.Revision != 0 {
		t.Fatalf("workspace isolation: %#v, %v", other, err)
	}
}

func TestMemoryWeeklyRhythm(t *testing.T) { checkRhythmPersistence(t, NewMemoryStore()) }

func TestPostgresWeeklyRhythm(t *testing.T) {
	store, pool := newPostgresStore(t)
	if _, err := pool.Exec(t.Context(), "truncate weekly_rhythms"); err != nil {
		t.Fatal(err)
	}
	checkRhythmPersistence(t, store)
	// A new store must observe the same saved workspace preference.
	loaded, err := NewPostgresStore(pool).GetRhythm(t.Context(), "rhythm-a")
	if err != nil || loaded.Targets[TypeYouTube] != 2 {
		t.Fatalf("new store: %#v, %v", loaded, err)
	}
}
