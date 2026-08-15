package health

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"
)

type Checker interface {
	Check(context.Context) map[string]error
}

type Dependencies struct {
	AssetDirectory string
	FirestoreHost  string
	FirestoreCheck func(context.Context) error
	DialTimeout    time.Duration
}

func (d Dependencies) Check(ctx context.Context) map[string]error {
	checks := map[string]error{
		"assets": checkAssetDirectory(d.AssetDirectory),
	}
	timeout := d.DialTimeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	if d.FirestoreCheck != nil {
		checkContext, cancel := context.WithTimeout(ctx, timeout)
		checks["firestore"] = d.FirestoreCheck(checkContext)
		cancel()
	} else if d.FirestoreHost != "" {
		dialer := net.Dialer{Timeout: timeout}
		connection, err := dialer.DialContext(ctx, "tcp", d.FirestoreHost)
		if err == nil {
			err = connection.Close()
		}
		checks["firestore"] = err
	}
	return checks
}

func checkAssetDirectory(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("asset path is not a directory")
	}
	probe, err := os.CreateTemp(directory, ".readiness-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		return closeErr
	}
	return os.Remove(name)
}
