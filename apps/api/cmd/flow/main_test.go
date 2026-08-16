//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestBlockedPreRequestReadsAreInterruptedBySignals(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "flow")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build flow: %v: %s", err, output)
	}
	create := filepath.Join(t.TempDir(), "create.json")
	youtube := filepath.Join(t.TempDir(), "youtube.json")
	if err := os.WriteFile(create, []byte(`{"type":"x","working_title":"Blocked","status":"draft","content":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(youtube, []byte(`{"type":"youtube","working_title":"Blocked","status":"draft","content":{"transcript":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		arguments func(string) []string
		stdin     bool
		signal    os.Signal
	}{
		{"request FIFO", func(path string) []string { return []string{"content", "create", "--file", path, "--json"} }, false, syscall.SIGTERM},
		{"transcript FIFO", func(path string) []string {
			return []string{"content", "create", "--file", youtube, "--transcript-file", path, "--json"}
		}, false, syscall.SIGHUP},
		{"metadata FIFO", func(path string) []string {
			return []string{"content", "create", "--file", create, "--operation-id", "01J00000000000000000000085", "--replay-metadata", path, "--json"}
		}, false, syscall.SIGTERM},
		{"stdin", func(string) []string { return []string{"content", "create", "--file", "-", "--json"} }, true, syscall.SIGHUP},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blockedPath := filepath.Join(t.TempDir(), "blocked.fifo")
			if !test.stdin {
				if err := syscall.Mkfifo(blockedPath, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.CommandContext(t.Context(), binary, test.arguments(blockedPath)...)
			command.Env = append(os.Environ(), "CONTENTFLOW_API_URL="+server.URL, "CONTENTFLOW_API_TOKEN=blocked_read_token")
			var stdinReader, stdinWriter *os.File
			if test.stdin {
				var err error
				stdinReader, stdinWriter, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				command.Stdin = stdinReader
			}
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			if stdinReader != nil {
				_ = stdinReader.Close()
			}
			time.Sleep(500 * time.Millisecond)
			if err := command.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				var exitError *exec.ExitError
				handled := errors.As(err, &exitError) && exitError.ExitCode() == 2
				terminated := false
				if errors.As(err, &exitError) {
					if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
						sent, isSignal := test.signal.(syscall.Signal)
						terminated = isSignal && status.Signaled() && status.Signal() == sent
					}
				}
				if (!handled && !terminated) || stdout.Len() != 0 || strings.Contains(stderr.String(), "blocked_read_token") {
					t.Fatalf("blocked read did not exit safely: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
			case <-time.After(3 * time.Second):
				_ = command.Process.Kill()
				t.Fatal("blocked read ignored signal")
			}
			if stdinWriter != nil {
				_ = stdinWriter.Close()
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("blocked pre-request input reached API %d times", requests.Load())
	}
}

func TestMutationSignalReportsGeneratedRecoveryOperationID(t *testing.T) {
	requestStarted := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var mutation struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal(body, &mutation); err != nil {
			requestStarted <- ""
			return
		}
		requestStarted <- mutation.OperationID
		<-request.Context().Done()
	}))
	defer server.Close()

	binary := filepath.Join(t.TempDir(), "flow")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build flow: %v: %s", err, output)
	}
	input := filepath.Join(t.TempDir(), "create.json")
	if err := os.WriteFile(input, []byte(`{"type":"x","working_title":"Signal recovery","status":"draft","content":{"body":"body"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		signal os.Signal
	}{
		{name: "hangup", signal: syscall.SIGHUP},
		{name: "terminate", signal: syscall.SIGTERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), binary, "content", "create", "--file", input, "--json")
			command.Env = append(os.Environ(), "CONTENTFLOW_API_URL="+server.URL, "CONTENTFLOW_API_TOKEN=signal_test_token")
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			operationID := <-requestStarted
			if operationID == "" {
				t.Fatal("request did not contain a generated operation ID")
			}
			if err := command.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			err := command.Wait()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 9 || stdout.Len() != 0 {
				t.Fatalf("signal did not use the mutation recovery path: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			var problem struct {
				Error          string `json:"error"`
				OperationID    string `json:"operation_id"`
				ReplayMetadata string `json:"replay_metadata"`
				ReplayBefore   int64  `json:"replay_before"`
			}
			if decodeErr := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &problem); decodeErr != nil || problem.Error != "request_failed" || problem.OperationID != operationID || problem.ReplayMetadata == "" || problem.ReplayBefore <= 0 || strings.Contains(stderr.String(), "signal_test_token") {
				t.Fatalf("signal recovery output is unsafe: problem=%#v err=%v stderr=%q", problem, decodeErr, stderr.String())
			}
			t.Cleanup(func() { _ = os.Remove(problem.ReplayMetadata) })
		})
	}
}

func TestMutationBrokenStdoutReportsGeneratedRecoveryOperationID(t *testing.T) {
	requestOperationIDs := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var mutation struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal(body, &mutation); err != nil {
			requestOperationIDs <- ""
			return
		}
		requestOperationIDs <- mutation.OperationID
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"operation_id":"`+mutation.OperationID+`","status":"created","item_ids":["01J00000000000000000000081"],"revisions":[1],"expires_at":["2030-01-01T00:00:00Z"]}`)
	}))
	defer server.Close()

	binary := filepath.Join(t.TempDir(), "flow")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build flow: %v: %s", err, output)
	}
	input := filepath.Join(t.TempDir(), "create.json")
	if err := os.WriteFile(input, []byte(`{"type":"x","working_title":"Broken pipe recovery","status":"draft","content":{"body":"body"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			arguments := []string{"content", "create", "--file", input}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			command := exec.CommandContext(t.Context(), binary, arguments...)
			command.Env = append(os.Environ(), "CONTENTFLOW_API_URL="+server.URL, "CONTENTFLOW_API_TOKEN=pipe_test_token")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			command.Stdout = writer
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				_ = writer.Close()
				t.Fatal(err)
			}
			_ = writer.Close()
			operationID := <-requestOperationIDs
			waitErr := command.Wait()
			var exitError *exec.ExitError
			if operationID == "" || !errors.As(waitErr, &exitError) || exitError.ExitCode() != 9 || strings.Contains(stderr.String(), "pipe_test_token") {
				t.Fatalf("broken stdout did not use recovery path: operation_id=%q err=%v stderr=%q", operationID, waitErr, stderr.String())
			}
			var metadataPath string
			var replayBefore int64
			if jsonOutput {
				var problem struct {
					Error          string `json:"error"`
					OperationID    string `json:"operation_id"`
					ReplayMetadata string `json:"replay_metadata"`
					ReplayBefore   int64  `json:"replay_before"`
				}
				if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &problem); err != nil || problem.Error != "invalid_api_response" || problem.OperationID != operationID {
					t.Fatalf("unsafe JSON recovery output: problem=%#v err=%v stderr=%q", problem, err, stderr.String())
				}
				metadataPath = problem.ReplayMetadata
				replayBefore = problem.ReplayBefore
			} else {
				lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
				if len(lines) != 4 || lines[0] != "error: invalid_api_response" || lines[1] != "operation_id: "+operationID || !strings.HasPrefix(lines[2], "replay_metadata: ") || !strings.HasPrefix(lines[3], "replay_before: ") {
					t.Fatalf("unexpected human recovery output: %q", stderr.String())
				}
				metadataPath = strings.TrimPrefix(lines[2], "replay_metadata: ")
				if _, err := fmt.Sscanf(strings.TrimPrefix(lines[3], "replay_before: "), "%d", &replayBefore); err != nil {
					t.Fatalf("parse replay deadline: %v", err)
				}
			}
			t.Cleanup(func() { _ = os.Remove(metadataPath) })
			if info, err := os.Stat(metadataPath); err != nil || info.Mode().Perm() != 0o600 || replayBefore <= 0 {
				t.Fatalf("unsafe recovery metadata: info=%v deadline=%d err=%v", info, replayBefore, err)
			}
		})
	}
}
