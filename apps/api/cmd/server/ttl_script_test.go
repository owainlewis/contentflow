package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirestoreTTLScriptConfiguresEveryExpiringAuthCollection(t *testing.T) {
	temporaryDirectory := t.TempDir()
	logPath := filepath.Join(temporaryDirectory, "gcloud.log")
	fakeGcloud := filepath.Join(temporaryDirectory, "gcloud")
	if err := os.WriteFile(fakeGcloud, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$GCLOUD_TEST_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "..", "..", "scripts", "configure-firestore-ttl.sh")
	command := exec.Command("bash", script, "test-project", "test-database")
	command.Env = append(os.Environ(), "PATH="+temporaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"), "GCLOUD_TEST_LOG="+logPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("configure Firestore TTL: %v: %s", err, output)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(lines) != 3 {
		t.Fatalf("TTL script ran %d commands, want 3: %q", len(lines), logged)
	}
	for index, collection := range []string{"oauth_login_attempts", "sessions", "api_token_rate_limits"} {
		for _, argument := range []string{
			"firestore fields ttls update expires_at",
			"--collection-group=" + collection,
			"--database=test-database",
			"--project=test-project",
			"--enable-ttl",
		} {
			if !strings.Contains(lines[index], argument) {
				t.Fatalf("TTL command %q does not contain %q", lines[index], argument)
			}
		}
	}
}
