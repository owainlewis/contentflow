package health

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDependenciesReportAssetAndFirestoreState(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	checks := (Dependencies{
		AssetDirectory: t.TempDir(),
		FirestoreHost:  listener.Addr().String(),
		DialTimeout:    time.Second,
	}).Check(context.Background())

	for _, dependency := range []string{"assets", "firestore"} {
		if err := checks[dependency]; err != nil {
			t.Fatalf("expected %s to be ready: %v", dependency, err)
		}
	}
}

func TestDependenciesUseBoundedProductionFirestoreCheck(t *testing.T) {
	t.Parallel()
	want := errors.New("firestore unavailable")
	checks := (Dependencies{
		AssetDirectory: t.TempDir(),
		DialTimeout:    time.Second,
		FirestoreCheck: func(ctx context.Context) error {
			if _, hasDeadline := ctx.Deadline(); !hasDeadline {
				t.Error("production Firestore check has no deadline")
			}
			return want
		},
	}).Check(context.Background())
	if !errors.Is(checks["firestore"], want) {
		t.Fatalf("readiness did not report production Firestore failure: %v", checks["firestore"])
	}
}

func TestDependenciesReportMissingAssetDirectory(t *testing.T) {
	t.Parallel()

	checks := (Dependencies{AssetDirectory: t.TempDir() + "/missing"}).Check(context.Background())
	if checks["assets"] == nil {
		t.Fatal("expected missing asset directory to fail readiness")
	}
}
