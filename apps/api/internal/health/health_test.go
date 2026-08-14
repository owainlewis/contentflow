package health

import (
	"context"
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

func TestDependenciesReportMissingAssetDirectory(t *testing.T) {
	t.Parallel()

	checks := (Dependencies{AssetDirectory: t.TempDir() + "/missing"}).Check(context.Background())
	if checks["assets"] == nil {
		t.Fatal("expected missing asset directory to fail readiness")
	}
}
