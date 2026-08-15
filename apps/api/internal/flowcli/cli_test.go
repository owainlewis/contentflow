package flowcli

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/content"
	"github.com/owainlewis/contentflow/apps/api/internal/server"
)

const (
	testWorkspace = "cli-workspace"
	readToken     = "cf_read_token_not_a_real_secret"
	writeToken    = "cf_write_token_not_a_real_secret"
	fullToken     = "cf_full_token_not_a_real_secret"
)

type fakeOAuth struct{}

func (fakeOAuth) AuthorizationURL(string, string) string { return "https://accounts.example/login" }
func (fakeOAuth) ExchangeIdentity(context.Context, string, string) (auth.Identity, error) {
	return auth.Identity{}, errors.New("unused")
}

type staticChecker struct{}

func (staticChecker) Check(context.Context) map[string]error { return map[string]error{"test": nil} }

type apiFixture struct {
	server       *httptest.Server
	handler      http.Handler
	contentStore *content.MemoryStore
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	authStore := auth.NewMemoryStore()
	authentication, err := auth.New(auth.Config{
		PublicOrigin: "http://127.0.0.1", OwnerIssuer: "https://accounts.example", OwnerSubject: "owner",
		WorkspaceID: testWorkspace, CredentialKey: make([]byte, 32),
	}, fakeOAuth{}, authStore)
	if err != nil {
		t.Fatal(err)
	}
	for index, token := range []struct {
		raw    string
		scopes []auth.Scope
	}{
		{readToken, []auth.Scope{auth.ScopeContentRead}},
		{writeToken, []auth.Scope{auth.ScopeContentWrite}},
		{fullToken, []auth.Scope{auth.ScopeContentRead, auth.ScopeContentWrite}},
	} {
		hash := sha256.Sum256([]byte(token.raw))
		if err := authStore.SaveToken(context.Background(), auth.Token{
			ID: fmt.Sprintf("token-%d", index), WorkspaceID: testWorkspace, Prefix: token.raw[:12], Hash: hash, Scopes: token.scopes, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	contentStore := content.NewMemoryStore()
	handler := server.NewAPIWithContent(staticChecker{}, authentication, content.NewHTTPHandler(content.NewService(contentStore)))
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	return &apiFixture{server: testServer, handler: handler, contentStore: contentStore}
}

type invocation struct {
	exitCode int
	stdout   string
	stderr   string
}

var testOperationCounter atomic.Uint64

func invoke(t *testing.T, apiURL, token string, httpClient *http.Client, stdin string, arguments ...string) invocation {
	t.Helper()
	var stdout, stderr bytes.Buffer
	result := Run(context.Background(), arguments, Options{
		APIURL: apiURL, Token: token, HTTPClient: httpClient, Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
		NewOperationID: func() (string, error) {
			return fmt.Sprintf("01J%023d", testOperationCounter.Add(1)), nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	return invocation{exitCode: result, stdout: stdout.String(), stderr: stderr.String()}
}

func assertFileMutationRecovery(t *testing.T, raw string, jsonOutput bool, code, operationID string) (string, replayMetadata) {
	t.Helper()
	var metadataPath string
	var replayBefore int64
	if jsonOutput {
		var problem struct {
			Error          string `json:"error"`
			OperationID    string `json:"operation_id"`
			ReplayFile     string `json:"replay_file"`
			ReplayMetadata string `json:"replay_metadata"`
			ReplayBefore   int64  `json:"replay_before"`
			RequestFile    string `json:"request_file"`
		}
		if err := decodeStrictJSON([]byte(raw), &problem); err != nil || problem.Error != code || problem.OperationID != operationID || problem.ReplayFile != "" || problem.RequestFile != "" {
			t.Fatalf("unexpected file-backed JSON recovery: raw=%q problem=%#v err=%v", raw, problem, err)
		}
		metadataPath = problem.ReplayMetadata
		replayBefore = problem.ReplayBefore
	} else {
		lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
		if len(lines) != 4 || lines[0] != "error: "+code || lines[1] != "operation_id: "+operationID || !strings.HasPrefix(lines[2], "replay_metadata: ") || !strings.HasPrefix(lines[3], "replay_before: ") {
			t.Fatalf("unexpected file-backed human recovery: %q", raw)
		}
		metadataPath = strings.TrimPrefix(lines[2], "replay_metadata: ")
		var err error
		replayBefore, err = strconv.ParseInt(strings.TrimPrefix(lines[3], "replay_before: "), 10, 64)
		if err != nil {
			t.Fatalf("parse replay deadline: %v", err)
		}
	}
	t.Cleanup(func() { _ = os.Remove(metadataPath) })
	contents, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read file-backed recovery metadata: %v", err)
	}
	info, err := os.Stat(metadataPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe file-backed recovery metadata: info=%v err=%v", info, err)
	}
	var metadata replayMetadata
	if err := decodeStrictJSON(contents, &metadata); err != nil || metadata.OperationID != operationID || metadata.Origin != "https://contentflow.example" || metadata.ReplayBefore != replayBefore || metadata.TemporaryRequest == nil || *metadata.TemporaryRequest {
		t.Fatalf("invalid file-backed recovery metadata: %#v err=%v", metadata, err)
	}
	if replayBefore < time.Now().Add(22*time.Hour).Unix() || replayBefore > time.Now().Add(23*time.Hour+time.Minute).Unix() {
		t.Fatalf("unexpected file-backed replay deadline: %d", replayBefore)
	}
	return metadataPath, metadata
}

func writeTestFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseMutation(t *testing.T, raw string) mutationResponse {
	t.Helper()
	var result mutationResponse
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode mutation %q: %v", raw, err)
	}
	return result
}

func TestCLICommandsAgainstRealAPIInHumanAndJSONModes(t *testing.T) {
	fixture := newAPIFixture(t)
	client := fixture.server.Client()
	transcriptPath := writeTestFile(t, "transcript.txt", "recorded\ntranscript")
	youtubeCreate := writeTestFile(t, "youtube-create.json", `{
  "type":"youtube","working_title":"Agent Source","status":"draft",
  "content":{"topic":"CLI","icp":"agents","angle":"safe","cta":"read","publishing_title":"Source","description":"private description","transcript":"replaced","sections":[{"position":0,"title":"Intro","body":"script only"}]}
}`)

	created := invoke(t, fixture.server.URL, fullToken, client, "", "content", "create", "--file", youtubeCreate, "--transcript-file", transcriptPath, "--json")
	if created.exitCode != ExitSuccess || created.stderr != "" {
		t.Fatalf("create JSON failed: %#v", created)
	}
	createdMutation := parseMutation(t, created.stdout)
	if len(createdMutation.ItemIDs) != 1 {
		t.Fatalf("unexpected create result: %s", created.stdout)
	}
	id := createdMutation.ItemIDs[0]

	createdHuman := invoke(t, fixture.server.URL, fullToken, client, "", "content", "create", "--file", writeTestFile(t, "x-create.json", `{"type":"x","working_title":"Second Draft","status":"draft","content":{"body":"standalone"}}`))
	if createdHuman.exitCode != ExitSuccess || !strings.HasPrefix(createdHuman.stdout, "Status: created\n") {
		t.Fatalf("create human failed: %#v", createdHuman)
	}

	for _, jsonMode := range []bool{false, true} {
		arguments := []string{"content", "show", id}
		if jsonMode {
			arguments = append(arguments, "--json")
		}
		result := invoke(t, fixture.server.URL, fullToken, client, "", arguments...)
		if result.exitCode != ExitSuccess || !strings.Contains(result.stdout, id) {
			t.Fatalf("show json=%v failed: %#v", jsonMode, result)
		}
	}

	humanTranscript := invoke(t, fixture.server.URL, fullToken, client, "", "content", "transcript", id)
	if humanTranscript.exitCode != ExitSuccess || humanTranscript.stdout != "recorded\ntranscript" {
		t.Fatalf("human transcript changed: %#v", humanTranscript)
	}
	jsonTranscript := invoke(t, fixture.server.URL, fullToken, client, "", "content", "transcript", id, "--json")
	if jsonTranscript.exitCode != ExitSuccess || strings.Contains(jsonTranscript.stdout, "sections") || strings.Contains(jsonTranscript.stdout, "description") {
		t.Fatalf("JSON transcript was not isolated: %#v", jsonTranscript)
	}

	for _, jsonMode := range []bool{false, true} {
		arguments := []string{"content", "list", "--search", "agent", "--type", "youtube", "--status", "draft"}
		if jsonMode {
			arguments = append(arguments, "--json")
		}
		result := invoke(t, fixture.server.URL, fullToken, client, "", arguments...)
		if result.exitCode != ExitSuccess || !strings.Contains(result.stdout, "Agent Source") {
			t.Fatalf("list json=%v failed: %#v", jsonMode, result)
		}
		if strings.Contains(result.stdout, "recorded") || strings.Contains(result.stdout, "script only") || strings.Contains(result.stdout, "private description") {
			t.Fatalf("list leaked non-summary fields: %s", result.stdout)
		}
	}

	show := invoke(t, fixture.server.URL, fullToken, client, "", "content", "show", id, "--json")
	var current itemResponse
	if err := json.Unmarshal([]byte(show.stdout), &current); err != nil {
		t.Fatal(err)
	}
	var currentContent map[string]any
	if err := json.Unmarshal(current.Content, &currentContent); err != nil {
		t.Fatal(err)
	}
	currentContent["description"] = "updated"
	updateInput, _ := json.Marshal(map[string]any{
		"type": current.Type, "working_title": current.WorkingTitle, "status": current.Status, "revision": current.Revision, "content": currentContent,
	})
	updatePath := writeTestFile(t, "update.json", string(updateInput))
	updated := invoke(t, fixture.server.URL, fullToken, client, "stdin transcript", "content", "update", id, "--file", updatePath, "--transcript-file", "-", "--json")
	if updated.exitCode != ExitSuccess {
		t.Fatalf("stdin update failed: %#v", updated)
	}
	updatedMutation := parseMutation(t, updated.stdout)
	updatedShow := invoke(t, fixture.server.URL, fullToken, client, "", "content", "show", id, "--json")
	if !strings.Contains(updatedShow.stdout, `"transcript":"stdin transcript"`) || !strings.Contains(updatedShow.stdout, `"body":"script only"`) {
		t.Fatalf("transcript update changed sections or failed round trip: %s", updatedShow.stdout)
	}

	var next itemResponse
	if err := json.Unmarshal([]byte(updatedShow.stdout), &next); err != nil {
		t.Fatal(err)
	}
	var nextContent map[string]any
	_ = json.Unmarshal(next.Content, &nextContent)
	clearInput, _ := json.Marshal(map[string]any{"type": next.Type, "working_title": next.WorkingTitle, "status": next.Status, "revision": updatedMutation.Revisions[0], "content": nextContent})
	cleared := invoke(t, fixture.server.URL, fullToken, client, "", "content", "update", id, "--file", writeTestFile(t, "clear.json", string(clearInput)), "--clear-transcript")
	if cleared.exitCode != ExitSuccess || !strings.HasPrefix(cleared.stdout, "Status: updated\n") {
		t.Fatalf("clear transcript failed: %#v", cleared)
	}
	missingHuman := invoke(t, fixture.server.URL, fullToken, client, "", "content", "transcript", id)
	if missingHuman.exitCode != ExitConflict || missingHuman.stderr != "error: transcript_missing\n" {
		t.Fatalf("missing transcript human mapping changed: %#v", missingHuman)
	}
	missingJSON := invoke(t, fixture.server.URL, fullToken, client, "", "content", "transcript", id, "--json")
	if missingJSON.exitCode != ExitConflict || missingJSON.stderr != "{\"error\":\"transcript_missing\"}\n" {
		t.Fatalf("missing transcript JSON mapping changed: %#v", missingJSON)
	}

	clearedShow := invoke(t, fixture.server.URL, fullToken, client, "", "content", "show", id, "--json")
	var clearedItem itemResponse
	_ = json.Unmarshal([]byte(clearedShow.stdout), &clearedItem)

	archiveHuman := invoke(t, fixture.server.URL, fullToken, client, "", "content", "archive", id, "--revision", fmt.Sprint(clearedItem.Revision))
	if archiveHuman.exitCode != ExitSuccess || !strings.HasPrefix(archiveHuman.stdout, "Status: archived\n") {
		t.Fatalf("archive human failed: %#v", archiveHuman)
	}
	archiveJSON := invoke(t, fixture.server.URL, fullToken, client, "", "content", "archive", id, "--revision", fmt.Sprint(clearedItem.Revision+1), "--json")
	if archiveJSON.exitCode != ExitSuccess || parseMutation(t, archiveJSON.stdout).Status != "archived" {
		t.Fatalf("archive JSON failed: %#v", archiveJSON)
	}
	restoreHuman := invoke(t, fixture.server.URL, fullToken, client, "", "content", "restore", id, "--revision", fmt.Sprint(clearedItem.Revision+2))
	if restoreHuman.exitCode != ExitSuccess || !strings.HasPrefix(restoreHuman.stdout, "Status: restored\n") {
		t.Fatalf("restore human failed: %#v", restoreHuman)
	}
	restoreJSON := invoke(t, fixture.server.URL, fullToken, client, "", "content", "restore", id, "--revision", fmt.Sprint(clearedItem.Revision+3), "--json")
	if restoreJSON.exitCode != ExitSuccess || parseMutation(t, restoreJSON.stdout).Status != "restored" {
		t.Fatalf("restore JSON failed: %#v", restoreJSON)
	}

	batchFile := writeTestFile(t, "batch.json", `{"items":[{"type":"x","working_title":"Draft one","status":"draft","content":{"body":"one"}},{"type":"linkedin","working_title":"Draft two","status":"draft","content":{"body":"two"}}]}`)
	batchHuman := invoke(t, fixture.server.URL, fullToken, client, "", "content", "batch-create", "--file", batchFile)
	if batchHuman.exitCode != ExitSuccess || !strings.HasPrefix(batchHuman.stdout, "Status: created\n") {
		t.Fatalf("batch human failed: %#v", batchHuman)
	}
	batchJSON := invoke(t, fixture.server.URL, fullToken, client, "", "content", "batch-create", "--file", batchFile, "--operation-id", "01J00000000000000000000088", "--json")
	if batchJSON.exitCode != ExitSuccess || len(parseMutation(t, batchJSON.stdout).ItemIDs) != 2 {
		t.Fatalf("batch JSON failed: %#v", batchJSON)
	}
}

func TestListJSONMatchesRealAPISummarySearchAndFilters(t *testing.T) {
	fixture := newAPIFixture(t)
	client := fixture.server.Client()
	for _, title := range []string{"Parity Alpha", "Parity Beta"} {
		path := writeTestFile(t, title+".json", fmt.Sprintf(`{"type":"x","working_title":%q,"status":"ready","content":{"body":"must stay private"}}`, title))
		if result := invoke(t, fixture.server.URL, fullToken, client, "", "content", "create", "--file", path, "--json"); result.exitCode != ExitSuccess {
			t.Fatalf("seed failed: %#v", result)
		}
	}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, fixture.server.URL+"/api/v1/content?q=parity&type=x&status=ready", nil)
	request.Header.Set("Authorization", "Bearer "+fullToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	cli := invoke(t, fixture.server.URL, fullToken, client, "", "content", "list", "--search", "parity", "--type", "x", "--status", "ready", "--json")
	if cli.exitCode != ExitSuccess {
		t.Fatalf("CLI list failed: %#v", cli)
	}
	var directValue, cliValue map[string]any
	_ = json.Unmarshal(direct, &directValue)
	_ = json.Unmarshal([]byte(cli.stdout), &cliValue)
	if !reflect.DeepEqual(directValue, cliValue) {
		t.Fatalf("CLI/API parity mismatch\nAPI: %s\nCLI: %s", direct, cli.stdout)
	}
	for _, item := range cliValue["items"].([]any) {
		if _, leaked := item.(map[string]any)["content"]; leaked {
			t.Fatalf("summary leaked content: %v", item)
		}
	}
}

func TestListAcceptsValidMultiMegabyteResponse(t *testing.T) {
	title := strings.Repeat("a", 500<<10)
	items := make([]map[string]any, 33)
	for index := range items {
		items[index] = map[string]any{
			"id": fmt.Sprintf("01J%023d", index+1), "type": "x", "status": "draft", "working_title": title,
			"revision": 1, "created_at": "2026-08-15T09:00:00Z", "updated_at": "2026-08-15T09:00:00Z",
			"expires_at": "2026-10-10T09:00:00Z", "asset_counts": map[string]int{},
		}
	}
	body, _ := json.Marshal(map[string]any{"items": items})
	if len(body) <= maxResponseBytes {
		t.Fatalf("test response is only %d bytes", len(body))
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(body)
	}))
	defer server.Close()

	for _, arguments := range [][]string{{"content", "list"}, {"content", "list", "--json"}} {
		result := invoke(t, server.URL, fullToken, server.Client(), "", arguments...)
		if result.exitCode != ExitSuccess || len(result.stdout) <= maxResponseBytes {
			t.Fatalf("valid large list failed for %v: exit=%d bytes=%d stderr=%q", arguments, result.exitCode, len(result.stdout), result.stderr)
		}
	}
}

func TestStreamingListRejectsLateInvalidItemWithoutPartialOutput(t *testing.T) {
	valid := `{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Valid","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T09:00:00Z","expires_at":"2026-10-10T09:00:00Z","asset_counts":{}}`
	body := `{"items":[` + valid + `,{"id":"not-a-ulid"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, body)
	}))
	defer server.Close()

	for _, arguments := range [][]string{{"content", "list"}, {"content", "list", "--json"}} {
		result := invoke(t, server.URL, fullToken, server.Client(), "", arguments...)
		if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") {
			t.Fatalf("late invalid item leaked output for %v: %#v", arguments, result)
		}
	}
}

func TestStreamingListResponseLimitRejectsWithoutPartialOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"items":[]}`)
		chunk := bytes.Repeat([]byte{' '}, 32<<10)
		for written := 0; written < maxListResponseBytes; written += len(chunk) {
			_, _ = response.Write(chunk)
		}
	}))
	defer server.Close()

	for _, arguments := range [][]string{{"content", "list"}, {"content", "list", "--json"}} {
		result := invoke(t, server.URL, fullToken, server.Client(), "", arguments...)
		if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") {
			t.Fatalf("oversized list leaked output for %v: %#v", arguments, result)
		}
	}
}

func TestListRequiresHTTP200AndAcceptsContractAssetKeys(t *testing.T) {
	valid := `{"items":[{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Assets","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T09:00:00Z","expires_at":"2026-10-10T09:00:00Z","asset_counts":{"audio":2,"future_kind":1}}]}`
	for _, jsonOutput := range []bool{false, true} {
		arguments := []string{"content", "list"}
		if jsonOutput {
			arguments = append(arguments, "--json")
		}
		t.Run(fmt.Sprintf("valid keys json=%v", jsonOutput), func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(valid))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
			if result.exitCode != ExitSuccess || result.stderr != "" || result.stdout == "" || (jsonOutput && !strings.Contains(result.stdout, `"future_kind":1`)) {
				t.Fatalf("contract-valid asset keys were rejected: %#v", result)
			}
		})
		t.Run(fmt.Sprintf("partial status json=%v", jsonOutput), func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusPartialContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(valid))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") {
				t.Fatalf("partial list status was accepted: %#v", result)
			}
		})
	}
}

func TestScopedBearerTokensAgainstRealAPI(t *testing.T) {
	fixture := newAPIFixture(t)
	client := fixture.server.Client()
	input := writeTestFile(t, "create.json", `{"type":"x","working_title":"Scoped","status":"draft","content":{"body":"body"}}`)
	tests := []struct {
		name      string
		token     string
		arguments []string
		wantExit  int
		wantError string
	}{
		{"read allows list", readToken, []string{"content", "list"}, ExitSuccess, ""},
		{"read denies create", readToken, []string{"content", "create", "--file", input}, ExitForbidden, "insufficient_scope"},
		{"write denies list", writeToken, []string{"content", "list"}, ExitForbidden, "insufficient_scope"},
		{"write allows create", writeToken, []string{"content", "create", "--file", input}, ExitSuccess, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := invoke(t, fixture.server.URL, test.token, client, "", test.arguments...)
			if result.exitCode != test.wantExit || !strings.Contains(result.stderr, test.wantError) {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

func TestAPIErrorExitCodesAndSecretRedaction(t *testing.T) {
	tests := []struct {
		status int
		exit   int
		code   string
	}{
		{http.StatusBadRequest, ExitInvalid, "invalid_request"}, {http.StatusUnauthorized, ExitAuth, "invalid_bearer_token"}, {http.StatusForbidden, ExitForbidden, "insufficient_scope"},
		{http.StatusNotFound, ExitNotFound, "content_not_found"}, {http.StatusConflict, ExitConflict, "revision_conflict"}, {http.StatusRequestEntityTooLarge, ExitInvalid, "request_too_large"},
		{http.StatusTooManyRequests, ExitRateLimited, "rate_limit_exceeded"}, {http.StatusServiceUnavailable, ExitUnavailable, "content_unavailable"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if strings.Contains(request.URL.String(), fullToken) {
					t.Error("token entered request URL")
				}
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(test.status)
				_, _ = fmt.Fprintf(response, `{"error":%q,"detail":%q}`, test.code, fullToken)
			}))
			defer server.Close()
			result := invoke(t, server.URL, fullToken, server.Client(), "", "content", "list", "--json")
			if result.exitCode != test.exit || result.stderr != fmt.Sprintf("{\"error\":%q}\n", test.code) || strings.Contains(result.stderr+result.stdout, fullToken) {
				t.Fatalf("error mapping or redaction failed: %#v", result)
			}
		})
	}
	t.Run("transport error", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, timeoutError{}
		})}
		result := invoke(t, "https://contentflow.example", fullToken, client, "", "content", "list", "--json")
		if result.exitCode != ExitUnavailable || result.stderr != "{\"error\":\"request_failed\"}\n" || strings.Contains(result.stderr+result.stdout, fullToken) {
			t.Fatalf("transport error leaked a secret: %#v", result)
		}
	})
}

func TestBearerTokenNeverFollowsRedirect(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+fullToken {
			t.Error("source request omitted bearer token")
		}
		http.Redirect(response, request, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	result := invoke(t, source.URL, fullToken, source.Client(), "", "content", "list", "--json")
	if result.exitCode != ExitUnavailable || targetCalls.Load() != 0 || strings.Contains(result.stderr+result.stdout, fullToken) {
		t.Fatalf("redirect leaked bearer credential: result=%#v targetCalls=%d", result, targetCalls.Load())
	}
}

func TestPlaintextAPIRequiresLiteralLoopbackIP(t *testing.T) {
	for _, apiURL := range []string{"http://localhost:3000", "http://contentflow.example"} {
		t.Run("reject "+apiURL, func(t *testing.T) {
			if _, err := newClient(apiURL, fullToken, nil, nil); err == nil {
				t.Fatalf("accepted unsafe plaintext URL %s", apiURL)
			}
		})
	}
	for _, apiURL := range []string{"http://127.0.0.1:3000", "http://127.1.2.3:3000", "http://[::1]:3000"} {
		t.Run("allow "+apiURL, func(t *testing.T) {
			if _, err := newClient(apiURL, fullToken, nil, nil); err != nil {
				t.Fatalf("rejected literal loopback URL %s: %v", apiURL, err)
			}
		})
	}
}

func TestAPIOriginRejectsEmptyQueryWithoutSendingRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	result := invoke(t, server.URL+"?", fullToken, server.Client(), "", "content", "list", "--json")
	assertLocalJSONError(t, result, "configuration_error")
	if calls.Load() != 0 {
		t.Fatalf("invalid API origin sent %d requests", calls.Load())
	}
}

func TestJSONCoversConfigurationAndLocalInputErrors(t *testing.T) {
	t.Run("configuration", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exitCode := Run(context.Background(), []string{"content", "list", "--json"}, Options{
			Token: fullToken, Stdout: &stdout, Stderr: &stderr,
		})
		assertLocalJSONError(t, invocation{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}, "configuration_error")
	})

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("local input error reached the API")
	}))
	defer server.Close()
	malformed := writeTestFile(t, "malformed.json", `{`)
	youtube := writeTestFile(t, "youtube.json", `{"type":"youtube","content":{"transcript":"existing"}}`)
	tests := []struct {
		name      string
		arguments []string
	}{
		{"invalid flag", []string{"content", "list", "--unknown", "--json"}},
		{"missing file", []string{"content", "create", "--file", filepath.Join(t.TempDir(), "missing.json"), "--json"}},
		{"malformed JSON", []string{"content", "create", "--file", malformed, "--json"}},
		{"conflicting transcript flags", []string{"content", "create", "--file", youtube, "--transcript-file", malformed, "--clear-transcript", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := invoke(t, server.URL, fullToken, server.Client(), "", test.arguments...)
			assertLocalJSONError(t, result, "usage_error")
		})
	}
}

func TestJSONReadSuccessValidatesEndpointSchema(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
	}{
		{"list", []string{"content", "list", "--json"}},
		{"show", []string{"content", "show", "01J00000000000000000000001", "--json"}},
		{"transcript", []string{"content", "transcript", "01J00000000000000000000001", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || result.stderr != "{\"error\":\"invalid_api_response\"}\n" {
				t.Fatalf("invalid %s response was accepted: %#v", test.name, result)
			}
		})
	}
}

func TestAPIResponsesRejectInvalidUTF8InAllCommandPaths(t *testing.T) {
	invalid := func(prefix, suffix string) []byte {
		return append(append([]byte(prefix), 0xff), []byte(suffix)...)
	}
	validSummaryPrefix := `{"items":[{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"`
	tests := []struct {
		name      string
		body      []byte
		arguments []string
	}{
		{"list human", invalid(validSummaryPrefix, `","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","asset_counts":{}}]}`), []string{"content", "list"}},
		{"show JSON", invalid(`{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"`, `","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{}}`), []string{"content", "show", "01J00000000000000000000001", "--json"}},
		{"transcript human", invalid(`{"id":"01J00000000000000000000001","revision":1,"transcript":"`, `"}`), []string{"content", "transcript", "01J00000000000000000000001"}},
		{"show duplicate field JSON", []byte(`{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":null,"working_title":"hidden","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{}}`), []string{"content", "show", "01J00000000000000000000001", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(test.body))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") {
				t.Fatalf("invalid UTF-8 response was accepted: %#v", result)
			}
		})
	}

	input := writeTestFile(t, "invalid-utf8-mutation.json", `{"type":"x","working_title":"Mutation","status":"draft","content":{"body":"body"}}`)
	mutationBody := invalid(`{"operation_id":"01J00000000000000000000082","item_ids":["01J00000000000000000000083"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"`, `"}`)
	mutation := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(mutationBody))}, nil
	})}, "", "content", "create", "--file", input, "--operation-id", "01J00000000000000000000082", "--json")
	if mutation.exitCode != ExitUnavailable || mutation.stdout != "" {
		t.Fatalf("invalid UTF-8 mutation response was accepted: %#v", mutation)
	}
	assertFileMutationRecovery(t, mutation.stderr, true, "invalid_api_response", "01J00000000000000000000082")
}

func TestCanonicalUppercaseULIDsAreRequiredAtEveryBoundary(t *testing.T) {
	lowerOperation := strings.ToLower("01J00000000000000000000092")
	lowerContent := strings.ToLower("01J00000000000000000000093")
	input := writeTestFile(t, "lowercase-operation.json", `{"type":"x","working_title":"Canonical IDs","status":"draft","content":{"body":"body"}}`)
	for _, test := range []struct {
		name      string
		arguments []string
	}{
		{"operation human", []string{"content", "create", "--file", input, "--operation-id", lowerOperation}},
		{"content JSON", []string{"content", "show", lowerContent, "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, timeoutError{}
			})}, "", test.arguments...)
			if result.exitCode != ExitUsage || result.stdout != "" || requests.Load() != 0 || strings.Contains(result.stderr, fullToken) {
				t.Fatalf("lowercase input ID reached API: result=%#v requests=%d", result, requests.Load())
			}
		})
	}

	uppercaseContent := "01J00000000000000000000093"
	uppercaseOperation := "01J00000000000000000000092"
	validBase := `"type":"x","status":"draft","working_title":"Canonical","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z"`
	responses := []struct {
		name      string
		status    int
		body      string
		arguments []string
	}{
		{"list content human", http.StatusOK, `{"items":[{"id":"` + lowerContent + `",` + validBase + `,"asset_counts":{}}]}`, []string{"content", "list"}},
		{"show section JSON", http.StatusOK, `{"id":"` + uppercaseContent + `",` + strings.Replace(validBase, `"type":"x"`, `"type":"youtube"`, 1) + `,"content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"` + lowerContent + `","position":0,"title":"A","body":""}]}}`, []string{"content", "show", uppercaseContent, "--json"}},
		{"transcript content human", http.StatusOK, `{"id":"` + lowerContent + `","revision":1,"transcript":"spoken"}`, []string{"content", "transcript", uppercaseContent}},
		{"mutation operation JSON", http.StatusCreated, `{"operation_id":"` + lowerOperation + `","item_ids":["` + uppercaseContent + `"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, []string{"content", "create", "--file", input, "--operation-id", uppercaseOperation, "--json"}},
		{"mutation item human", http.StatusCreated, `{"operation_id":"` + uppercaseOperation + `","item_ids":["` + lowerContent + `"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, []string{"content", "create", "--file", input, "--operation-id", uppercaseOperation}},
	}
	for _, test := range responses {
		t.Run(test.name, func(t *testing.T) {
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") || strings.Contains(result.stderr, fullToken) {
				t.Fatalf("lowercase response ID was accepted: %#v", result)
			}
			if strings.Contains(test.name, "mutation") {
				assertFileMutationRecovery(t, result.stderr, strings.Contains(test.name, "JSON"), "invalid_api_response", uppercaseOperation)
			}
		})
	}
}

func TestMutationInputsRejectInvalidUTF8WithoutHTTPRequest(t *testing.T) {
	invalidFile := func(name, prefix, suffix string) string {
		path := filepath.Join(t.TempDir(), name)
		body := append(append([]byte(prefix), 0xff), []byte(suffix)...)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	create := invalidFile("create.json", `{"type":"x","working_title":"`, `","status":"draft","content":{}}`)
	batch := invalidFile("batch.json", `{"items":[{"type":"x","working_title":"`, `","status":"draft","content":{}}]}`)
	duplicate := writeTestFile(t, "duplicate.json", `{"type":"x","working_title":null,"working_title":"hidden","status":"draft","content":{}}`)
	transcript := invalidFile("transcript.txt", "spoken ", "")
	youtube := writeTestFile(t, "utf8-youtube.json", `{"type":"youtube","working_title":"Video","status":"draft","content":{"transcript":""}}`)
	tests := [][]string{
		{"content", "create", "--file", create, "--json"},
		{"content", "batch-create", "--file", batch, "--json"},
		{"content", "create", "--file", duplicate, "--json"},
		{"content", "create", "--file", youtube, "--transcript-file", transcript, "--json"},
	}
	for _, arguments := range tests {
		var requests atomic.Int64
		result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, timeoutError{}
		})}, "", arguments...)
		if result.exitCode != ExitUsage || result.stdout != "" || requests.Load() != 0 {
			t.Fatalf("invalid UTF-8 input reached API for %v: %#v requests=%d", arguments, result, requests.Load())
		}
	}
	stdinResult := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid UTF-8 stdin reached API")
		return nil, timeoutError{}
	})}, string([]byte{'x', 0xff}), "content", "create", "--file", youtube, "--transcript-file", "-", "--json")
	if stdinResult.exitCode != ExitUsage || stdinResult.stdout != "" {
		t.Fatalf("invalid UTF-8 stdin was accepted: %#v", stdinResult)
	}
}

func TestCancelledOutputWriteReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := (cancellableWriter{ctx: ctx, destination: writer}).Write(bytes.Repeat([]byte("x"), 64*1024))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked output returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked output ignored cancellation")
	}
}

func TestCancelledFIFOOpenDoesNotLeakGoroutine(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "cancel-open.fifo")
	if err := makeTestFIFO(fifo, 0o600); err != nil {
		t.Skipf("named pipes are unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := openFileWithContext(ctx, fifo)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled FIFO open returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO open ignored cancellation")
	}
	deadline := time.Now().Add(time.Second)
	for {
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
		if !strings.Contains(stacks.String(), "openFileWithContext.func1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled FIFO open leaked its worker goroutine")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTranscriptResponseRequiresCompleteCanonicalText(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		jsonOutput bool
	}{
		{"missing transcript human", `{"id":"01J00000000000000000000001","revision":1}`, false},
		{"blank transcript JSON", `{"id":"01J00000000000000000000001","revision":1,"transcript":"  "}`, true},
		{"invalid ID human", `{"id":"not-an-id","revision":1,"transcript":"canonical"}`, false},
		{"zero revision JSON", `{"id":"01J00000000000000000000001","revision":0,"transcript":"canonical"}`, true},
		{"mismatched ID human", `{"id":"01J00000000000000000000002","revision":1,"transcript":"canonical"}`, false},
		{"mismatched ID JSON", `{"id":"01J00000000000000000000002","revision":1,"transcript":"canonical"}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			arguments := []string{"content", "transcript", "01J00000000000000000000001"}
			wantStderr := "error: invalid_api_response\n"
			if test.jsonOutput {
				arguments = append(arguments, "--json")
				wantStderr = "{\"error\":\"invalid_api_response\"}\n"
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || result.stderr != wantStderr {
				t.Fatalf("invalid transcript response was accepted: %#v", result)
			}
		})
	}
}

func TestTranscriptResponseEnforcesTextLimitInHumanAndJSONModes(t *testing.T) {
	for _, size := range []int{500 << 10, (500 << 10) + 1} {
		transcript := strings.Repeat("x", size)
		body := fmt.Sprintf(`{"id":"01J00000000000000000000001","revision":1,"transcript":%q}`, transcript)
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("bytes=%d/json=%v", size, jsonOutput), func(t *testing.T) {
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				arguments := []string{"content", "transcript", "01J00000000000000000000001"}
				if jsonOutput {
					arguments = append(arguments, "--json")
				}
				result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
				if size == 500<<10 {
					if result.exitCode != ExitSuccess || result.stderr != "" || result.stdout == "" {
						t.Fatalf("maximum transcript was rejected: %#v", result)
					}
					return
				}
				if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") {
					t.Fatalf("oversized transcript was accepted: %#v", result)
				}
			})
		}
	}
}

func TestShowAndTranscriptRequireHTTP200(t *testing.T) {
	item := `{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Item","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{}}`
	transcript := `{"id":"01J00000000000000000000001","revision":1,"transcript":"partial"}`
	for _, test := range []struct {
		name      string
		body      string
		arguments []string
	}{
		{"show", item, []string{"content", "show", "01J00000000000000000000001"}},
		{"transcript", transcript, []string{"content", "transcript", "01J00000000000000000000001"}},
	} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", test.name, jsonOutput), func(t *testing.T) {
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusPartialContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
				})
				arguments := slices.Clone(test.arguments)
				if jsonOutput {
					arguments = append(arguments, "--json")
				}
				result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
				if result.exitCode != ExitUnavailable || result.stdout != "" || !strings.Contains(result.stderr, "invalid_api_response") {
					t.Fatalf("partial %s response was accepted: %#v", test.name, result)
				}
			})
		}
	}
}

func TestResponseSchemasRejectUnexpectedPrivateFields(t *testing.T) {
	validSummary := `"id":"01J00000000000000000000001","type":"youtube","status":"draft","working_title":"Summary","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","asset_counts":{}`
	readTests := []struct {
		name      string
		body      string
		arguments []string
	}{
		{"list transcript JSON", `{"items":[{` + validSummary + `,"transcript":"PRIVATE"}]}`, []string{"content", "list", "--json"}},
		{"list sections human", `{"items":[{` + validSummary + `,"sections":[{"body":"PRIVATE"}]}]}`, []string{"content", "list"}},
		{"list content JSON", `{"items":[{` + validSummary + `,"content":{"body":"PRIVATE"}}]}`, []string{"content", "list", "--json"}},
		{"list duplicate IDs human", `{"items":[{` + validSummary + `},{` + validSummary + `}]}`, []string{"content", "list"}},
		{"list duplicate IDs JSON", `{"items":[{` + validSummary + `},{` + validSummary + `}]}`, []string{"content", "list", "--json"}},
		{"list null asset count human", `{"items":[{` + strings.Replace(validSummary, `"asset_counts":{}`, `"asset_counts":{"images":null}`, 1) + `}]}`, []string{"content", "list"}},
		{"list null asset count JSON", `{"items":[{` + strings.Replace(validSummary, `"asset_counts":{}`, `"asset_counts":{"images":null}`, 1) + `}]}`, []string{"content", "list", "--json"}},
		{"list null archived timestamp human", `{"items":[{` + validSummary + `,"archived_at":null}]}`, []string{"content", "list"}},
		{"list null archived timestamp JSON", `{"items":[{` + validSummary + `,"archived_at":null}]}`, []string{"content", "list", "--json"}},
		{"show null archived timestamp human", `{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Item","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","archived_at":null,"content":{"body":"body"}}`, []string{"content", "show", "01J00000000000000000000001"}},
		{"show null archived timestamp JSON", `{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Item","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","archived_at":null,"content":{"body":"body"}}`, []string{"content", "show", "01J00000000000000000000001", "--json"}},
		{"show oversized title human", fmt.Sprintf(`{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":%q,"revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"body":"body"}}`, strings.Repeat("x", (500<<10)+1)), []string{"content", "show", "01J00000000000000000000001"}},
		{"show oversized title JSON", fmt.Sprintf(`{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":%q,"revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"body":"body"}}`, strings.Repeat("x", (500<<10)+1)), []string{"content", "show", "01J00000000000000000000001", "--json"}},
		{"transcript sections JSON", `{"id":"01J00000000000000000000001","revision":1,"transcript":"canonical","sections":[{"body":"PRIVATE"}]}`, []string{"content", "transcript", "01J00000000000000000000001", "--json"}},
		{"list missing fields JSON", `{"items":[{"id":"01J00000000000000000000001"}]}`, []string{"content", "list", "--json"}},
	}
	for _, test := range readTests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || strings.Contains(result.stderr+result.stdout, "PRIVATE") {
				t.Fatalf("private or malformed response was accepted: %#v", result)
			}
		})
	}

	t.Run("mutation body JSON", func(t *testing.T) {
		input := writeTestFile(t, "private-mutation.json", `{"type":"x","working_title":"Private response","status":"draft","content":{"body":"request"}}`)
		body := `{"operation_id":"01J00000000000000000000045","item_ids":["01J00000000000000000000046"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created","content":{"transcript":"PRIVATE"}}`
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "create", "--file", input, "--operation-id", "01J00000000000000000000045", "--json")
		if result.exitCode != ExitUnavailable || result.stdout != "" || strings.Contains(result.stderr+result.stdout, "PRIVATE") {
			t.Fatalf("private mutation response was accepted: %#v", result)
		}
		assertFileMutationRecovery(t, result.stderr, true, "invalid_api_response", "01J00000000000000000000045")
	})
}

func TestShowResponseContentMatchesDiscriminatorSchema(t *testing.T) {
	base := `"id":"01J00000000000000000000001","status":"draft","working_title":"Typed","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z"`
	oversized := strings.Repeat("x", (500<<10)+1)
	tests := []struct {
		contentType string
		content     string
	}{
		{"youtube", `{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"Intro","body":"Body"}]}`},
		{"linkedin", `{"body":"body"}`},
		{"x", `{"body":"body"}`},
		{"instagram", `{"script":"script"}`},
		{"tiktok", `{"script":"script"}`},
		{"email", `{"subject":"subject","body":"body"}`},
		{"substack", `{"headline":"headline","subheadline":"subheadline","body":"body"}`},
	}
	for _, test := range tests {
		t.Run(test.contentType, func(t *testing.T) {
			body := []byte(`{` + base + `,"type":"` + test.contentType + `","content":` + test.content + `}`)
			if _, err := decodeItemResponse(body); err != nil {
				t.Fatalf("valid %s content rejected: %v", test.contentType, err)
			}
		})
	}
	for _, valid := range []string{
		`{` + base + `,"type":"youtube","content":{"transcript":""}}`,
		`{` + base + `,"type":"x","content":{}}`,
		`{` + base + `,"type":"email","content":{"subject":"optional"}}`,
	} {
		if _, err := decodeItemResponse([]byte(valid)); err != nil {
			t.Fatalf("optional typed fields were required: %v body=%s", err, valid)
		}
	}
	for _, invalid := range []string{
		`{` + base + `,"type":"x","content":{"script":"wrong discriminator"}}`,
		`{` + base + `,"type":"x","content":{"body":"body","transcript":"PRIVATE"}}`,
		`{` + base + `,"type":"youtube","content":{"topic":null,"icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[]}}`,
		`{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":null}}`,
		`{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":null,"body":""}]}}`,
		`{` + base + `,"type":"linkedin","content":{"body":null}}`,
		`{` + base + `,"type":"x","content":{"body":null}}`,
		`{` + base + `,"type":"instagram","content":{"script":null}}`,
		`{` + base + `,"type":"tiktok","content":{"script":null}}`,
		`{` + base + `,"type":"email","content":{"subject":null,"body":""}}`,
		`{` + base + `,"type":"substack","content":{"headline":null,"subheadline":"","body":""}}`,
		`{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"bad","position":0,"title":"","body":""}]}}`,
		`{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"","body":""},{"id":"01J00000000000000000000003","position":0,"title":"","body":""}]}}`,
		`{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"","body":""},{"id":"01J00000000000000000000003","position":2,"title":"","body":""}]}}`,
		`{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"","body":""},{"id":"01J00000000000000000000002","position":1,"title":"","body":""}]}}`,
		fmt.Sprintf(`{%s,"type":"youtube","content":{"topic":%q,"icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[]}}`, base, oversized),
		fmt.Sprintf(`{%s,"type":"linkedin","content":{"body":%q}}`, base, oversized),
		fmt.Sprintf(`{%s,"type":"x","content":{"body":%q}}`, base, oversized),
		fmt.Sprintf(`{%s,"type":"instagram","content":{"script":%q}}`, base, oversized),
		fmt.Sprintf(`{%s,"type":"tiktok","content":{"script":%q}}`, base, oversized),
		fmt.Sprintf(`{%s,"type":"email","content":{"subject":%q,"body":""}}`, base, oversized),
		fmt.Sprintf(`{%s,"type":"substack","content":{"headline":%q,"subheadline":"","body":""}}`, base, oversized),
	} {
		if _, err := decodeItemResponse([]byte(invalid)); err == nil {
			t.Fatalf("mismatched content was accepted: %s", invalid)
		}
	}
}

func TestShowRejectsNullAndUnorderedTypedContentInHumanAndJSONModes(t *testing.T) {
	base := `"id":"01J00000000000000000000001","status":"draft","working_title":"Typed","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z"`
	sections := make([]map[string]any, 201)
	for index := range sections {
		sections[index] = map[string]any{"id": fmt.Sprintf("01J%023d", index+2), "position": index, "title": "", "body": ""}
	}
	tooManySections, _ := json.Marshal(sections)
	tests := []struct {
		name    string
		body    string
		useJSON bool
	}{
		{"null X body human", `{` + base + `,"type":"x","content":{"body":null}}`, false},
		{"null email subject JSON", `{` + base + `,"type":"email","content":{"subject":null,"body":"body"}}`, true},
		{"duplicate YouTube positions human", `{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"A","body":""},{"id":"01J00000000000000000000003","position":0,"title":"B","body":""}]}}`, false},
		{"duplicate YouTube IDs JSON", `{` + base + `,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"A","body":""},{"id":"01J00000000000000000000002","position":1,"title":"B","body":""}]}}`, true},
		{"oversized X body human", fmt.Sprintf(`{%s,"type":"x","content":{"body":%q}}`, base, strings.Repeat("x", (500<<10)+1)), false},
		{"oversized YouTube section body JSON", fmt.Sprintf(`{%s,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"","body":%q}]}}`, base, strings.Repeat("x", (500<<10)+1)), true},
		{"too many YouTube sections human", fmt.Sprintf(`{%s,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":%s}}`, base, tooManySections), false},
		{"too many YouTube sections JSON", fmt.Sprintf(`{%s,"type":"youtube","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":%s}}`, base, tooManySections), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			arguments := []string{"content", "show", "01J00000000000000000000001"}
			want := "error: invalid_api_response\n"
			if test.useJSON {
				arguments = append(arguments, "--json")
				want = "{\"error\":\"invalid_api_response\"}\n"
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || result.stderr != want {
				t.Fatalf("invalid typed content was accepted: %#v", result)
			}
		})
	}
}

func TestShowAcceptsOmittedOptionalTypedFieldsInHumanAndJSONModes(t *testing.T) {
	base := `"id":"01J00000000000000000000001","status":"draft","working_title":"Optional","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z"`
	for _, test := range []struct {
		name       string
		body       string
		jsonOutput bool
	}{
		{"minimal YouTube human", `{` + base + `,"type":"youtube","content":{"transcript":""}}`, false},
		{"empty X JSON", `{` + base + `,"type":"x","content":{}}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			arguments := []string{"content", "show", "01J00000000000000000000001"}
			if test.jsonOutput {
				arguments = append(arguments, "--json")
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
			if result.exitCode != ExitSuccess || result.stdout == "" || result.stderr != "" {
				t.Fatalf("optional fields were required: %#v", result)
			}
		})
	}
}

func TestShowResponseIDMustMatchRequestedContentInHumanAndJSONModes(t *testing.T) {
	body := `{"id":"01J00000000000000000000002","type":"x","status":"draft","working_title":"Wrong item","revision":1,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"body":"wrong"}}`
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			arguments := []string{"content", "show", "01J00000000000000000000001"}
			want := "error: invalid_api_response\n"
			if jsonOutput {
				arguments = append(arguments, "--json")
				want = "{\"error\":\"invalid_api_response\"}\n"
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || result.stderr != want {
				t.Fatalf("mismatched show response was accepted: %#v", result)
			}
		})
	}
}

func TestConflictCurrentIsCanonicalizedOrOmitted(t *testing.T) {
	input := writeTestFile(t, "conflict.json", `{"type":"x","working_title":"Conflict","status":"draft","revision":1,"content":{"body":"body"}}`)
	validCurrent := `{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Current","revision":2,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"body":"current"}}`
	invalidYouTubeCurrent := `{"id":"01J00000000000000000000001","type":"youtube","status":"draft","working_title":"Current","revision":2,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"topic":"","icp":"","angle":"","cta":"","publishing_title":"","description":"","transcript":"","sections":[{"id":"01J00000000000000000000002","position":0,"title":"A","body":""},{"id":"01J00000000000000000000002","position":1,"title":"B","body":""}]}}`
	tests := []struct {
		name        string
		current     string
		wantCurrent bool
	}{
		{"valid", validCurrent, true},
		{"missing", "", false},
		{"null", "null", false},
		{"private top-level field", strings.TrimSuffix(validCurrent, "}") + `,"transcript":"PRIVATE"}`, false},
		{"mismatched content", strings.Replace(validCurrent, `"content":{"body":"current"}`, `"content":{"script":"PRIVATE"}`, 1), false},
		{"unordered current", invalidYouTubeCurrent, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"error":"revision_conflict","detail":"PRIVATE DETAIL"`
				if test.current != "" {
					body += `,"current":` + test.current
				}
				body += `}`
				return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "update", "01J00000000000000000000001", "--file", input, "--operation-id", "01J00000000000000000000074", "--json")
			if result.stdout != "" || strings.Contains(result.stderr, "PRIVATE") {
				t.Fatalf("unsafe conflict output: %#v", result)
			}
			if !test.wantCurrent {
				if result.exitCode != ExitUnavailable {
					t.Fatalf("invalid conflict current was accepted: %#v", result)
				}
				assertFileMutationRecovery(t, result.stderr, true, "invalid_api_response", "01J00000000000000000000074")
				return
			}
			var problem struct {
				Error   string          `json:"error"`
				Current json.RawMessage `json:"current"`
			}
			if err := json.Unmarshal([]byte(result.stderr), &problem); result.exitCode != ExitConflict || err != nil || problem.Error != "revision_conflict" || len(problem.Current) == 0 {
				t.Fatalf("unexpected canonical conflict: %q, %v", result.stderr, err)
			}
		})
	}
}

func TestConflictCurrentMustMatchMutationTarget(t *testing.T) {
	current := `{"id":"01J00000000000000000000002","type":"x","status":"draft","working_title":"Wrong current","revision":2,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"body":"wrong"}}`
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"error":"revision_conflict","current":` + current + `}`
		return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	update := writeTestFile(t, "target-conflict.json", `{"type":"x","working_title":"Target","status":"draft","revision":1,"content":{"body":"target"}}`)
	tests := []struct {
		name      string
		arguments []string
		want      string
	}{
		{"update JSON", []string{"content", "update", "01J00000000000000000000001", "--file", update, "--operation-id", "01J00000000000000000000003", "--json"}, ""},
		{"archive human", []string{"content", "archive", "01J00000000000000000000001", "--revision", "1", "--operation-id", "01J00000000000000000000003"}, "error: invalid_api_response\noperation_id: 01J00000000000000000000003\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || strings.Contains(result.stderr, "Wrong current") {
				t.Fatalf("unrelated conflict current was emitted: %#v", result)
			}
			if test.want == "" {
				assertFileMutationRecovery(t, result.stderr, true, "invalid_api_response", "01J00000000000000000000003")
			} else if result.stderr != test.want {
				t.Fatalf("unexpected archive recovery output: %#v", result)
			}
		})
	}
}

func TestCurrentIsEmittedOnlyForTargetedRevisionConflict(t *testing.T) {
	current := `{"id":"01J00000000000000000000001","type":"x","status":"draft","working_title":"Current","revision":2,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","content":{"body":"current"}}`
	input := writeTestFile(t, "current-contract.json", `{"type":"x","working_title":"Target","status":"draft","revision":1,"content":{"body":"target"}}`)
	tests := []struct {
		name      string
		status    int
		code      string
		arguments []string
		exit      int
	}{
		{"create revision conflict", http.StatusConflict, "revision_conflict", []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000003", "--json"}, ExitConflict},
		{"update operation conflict", http.StatusConflict, "operation_id_conflict", []string{"content", "update", "01J00000000000000000000001", "--file", input, "--operation-id", "01J00000000000000000000003", "--json"}, ExitConflict},
		{"update forbidden", http.StatusForbidden, "insufficient_scope", []string{"content", "update", "01J00000000000000000000001", "--file", input, "--operation-id", "01J00000000000000000000003", "--json"}, ExitForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := fmt.Sprintf(`{"error":%q,"current":%s}`, test.code, current)
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != test.exit || result.stdout != "" || strings.Contains(result.stderr, "current") || strings.Contains(result.stderr, "working_title") {
				t.Fatalf("out-of-contract current was emitted: %#v", result)
			}
		})
	}
}

func TestSubcommandHelpDoesNotRequireConfiguration(t *testing.T) {
	for _, arguments := range [][]string{
		{"content", "help"},
		{"content", "--help"},
		{"content", "-h"},
		{"content", "create", "--help"},
		{"content", "transcript", "-h"},
	} {
		var stdout, stderr bytes.Buffer
		exitCode := Run(context.Background(), arguments, Options{Stdout: &stdout, Stderr: &stderr})
		if exitCode != ExitSuccess || stderr.String() != "" || !strings.Contains(stdout.String(), "flow content create") {
			t.Fatalf("help required configuration for %v: exit=%d stdout=%q stderr=%q", arguments, exitCode, stdout.String(), stderr.String())
		}
	}
}

func assertLocalJSONError(t *testing.T, result invocation, wantCode string) {
	t.Helper()
	if result.exitCode != ExitUsage || result.stdout != "" || !json.Valid([]byte(result.stderr)) {
		t.Fatalf("local error was not JSON: %#v", result)
	}
	var problem struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(result.stderr), &problem); err != nil || problem.Error != wantCode || problem.Detail == "" {
		t.Fatalf("unexpected local error: %#v, %v", problem, err)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout containing " + fullToken }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type timeoutResponseBody struct{ closed bool }

func (*timeoutResponseBody) Read([]byte) (int, error) { return 0, timeoutError{} }
func (body *timeoutResponseBody) Close() error {
	body.closed = true
	return nil
}

type closeErrorBody struct{ io.Reader }

func (closeErrorBody) Close() error { return errors.New("close failed") }

func TestCompleteResponseBodyIsKeptWhenCloseReportsAnError(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       closeErrorBody{Reader: strings.NewReader(`{"items":[]}`)},
		}, nil
	})
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "list", "--json")
	if result.exitCode != ExitSuccess || result.stdout != "{\"items\":[]}\n" || result.stderr != "" {
		t.Fatalf("complete response was discarded after close error: %#v", result)
	}
}

func TestMutationTimeoutRetryReusesFrozenRequestBytesAndOperationID(t *testing.T) {
	input := writeTestFile(t, "batch.json", `{"items":[{"type":"x","working_title":"Retry","status":"draft","content":{"body":"body"}}]}`)
	var bodies [][]byte
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			return nil, timeoutError{}
		}
		return &http.Response{
			StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"operation_id":"01J00000000000000000000077","item_ids":["01J00000000000000000000001"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`)),
		}, nil
	})
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "batch-create", "--file", input, "--operation-id", "01J00000000000000000000077", "--json")
	if result.exitCode != ExitSuccess || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry was not byte-identical: result=%#v bodies=%q", result, bodies)
	}
	for _, body := range bodies {
		if bytes.Count(body, []byte("01J00000000000000000000077")) != 1 {
			t.Fatalf("operation ID was not frozen in %s", body)
		}
	}
}

func TestMutationResponseBodyTimeoutRetriesFrozenBytes(t *testing.T) {
	input := writeTestFile(t, "create.json", `{"type":"x","working_title":"Body timeout","status":"draft","content":{"body":"body"}}`)
	timedOutBody := &timeoutResponseBody{}
	var bodies [][]byte
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: timedOutBody}, nil
		}
		return &http.Response{
			StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"operation_id":"01J00000000000000000000055","item_ids":["01J00000000000000000000001"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`)),
		}, nil
	})
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "create", "--file", input, "--operation-id", "01J00000000000000000000055", "--json")
	if result.exitCode != ExitSuccess || !timedOutBody.closed || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("response-body timeout retry was unsafe: result=%#v closed=%v bodies=%q", result, timedOutBody.closed, bodies)
	}
}

func TestResponseBodyLimitAppliesAfterDecompressionAndPreservesMutationRecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(response)
		_, _ = compressed.Write(bytes.Repeat([]byte{'x'}, maxResponseBytes+1))
		_ = compressed.Close()
	}))
	defer server.Close()
	read := invoke(t, server.URL, fullToken, server.Client(), "", "content", "show", "01J00000000000000000000001", "--json")
	if read.exitCode != ExitUnavailable || read.stdout != "" || read.stderr != `{"error":"request_failed"}`+"\n" {
		t.Fatalf("oversized decompressed response was accepted: %#v", read)
	}

	input := writeTestFile(t, "oversized-response.json", `{"type":"x","working_title":"Bound response","status":"draft","content":{"body":"body"}}`)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{'x'}, maxResponseBytes+1))),
		}, nil
	})
	mutation := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "create", "--file", input, "--operation-id", "01J00000000000000000000075", "--json")
	if mutation.exitCode != ExitUnavailable || mutation.stdout != "" || strings.Contains(mutation.stderr, fullToken) {
		t.Fatalf("oversized mutation response lost recovery data: %#v", mutation)
	}
	assertFileMutationRecovery(t, mutation.stderr, true, "request_failed", "01J00000000000000000000075")
}

func TestStdinMutationFailureRetainsExactFrozenReplay(t *testing.T) {
	tests := []struct {
		name      string
		stdin     string
		arguments []string
		json      bool
	}{
		{
			name:      "request JSON human",
			stdin:     `{"items":[{"type":"x","working_title":"Piped batch","status":"draft","content":{"body":"body"}}]}`,
			arguments: []string{"content", "batch-create", "--file", "-", "--operation-id", "01J00000000000000000000071"},
		},
		{
			name:      "transcript JSON",
			stdin:     "exact piped transcript",
			arguments: []string{"content", "create", "--file", writeTestFile(t, "piped-transcript.json", `{"type":"youtube","working_title":"Piped transcript","status":"draft","content":{}}`), "--transcript-file", "-", "--operation-id", "01J00000000000000000000072", "--json"},
			json:      true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var bodies [][]byte
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				bodies = append(bodies, slices.Clone(body))
				return nil, timeoutError{}
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, test.stdin, test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || len(bodies) != mutationAttempts || strings.Contains(result.stderr, fullToken) {
				t.Fatalf("stdin failure was not recoverable: result=%#v attempts=%d", result, len(bodies))
			}
			var replayPath, metadataPath string
			var replayBefore int64
			if test.json {
				var problem struct {
					Error          string `json:"error"`
					OperationID    string `json:"operation_id"`
					ReplayFile     string `json:"replay_file"`
					ReplayMetadata string `json:"replay_metadata"`
					ReplayBefore   int64  `json:"replay_before"`
				}
				if err := json.Unmarshal([]byte(result.stderr), &problem); err != nil || problem.Error != "request_failed" || problem.OperationID != "01J00000000000000000000072" {
					t.Fatalf("unexpected JSON recovery output: %q, %v", result.stderr, err)
				}
				replayPath = problem.ReplayFile
				metadataPath = problem.ReplayMetadata
				replayBefore = problem.ReplayBefore
			} else {
				lines := strings.Split(strings.TrimSuffix(result.stderr, "\n"), "\n")
				if len(lines) != 5 || lines[0] != "error: request_failed" || lines[1] != "operation_id: 01J00000000000000000000071" || !strings.HasPrefix(lines[2], "replay_file: ") || !strings.HasPrefix(lines[3], "replay_metadata: ") || !strings.HasPrefix(lines[4], "replay_before: ") {
					t.Fatalf("unexpected human recovery output: %q", result.stderr)
				}
				replayPath = strings.TrimPrefix(lines[2], "replay_file: ")
				metadataPath = strings.TrimPrefix(lines[3], "replay_metadata: ")
				var err error
				replayBefore, err = strconv.ParseInt(strings.TrimPrefix(lines[4], "replay_before: "), 10, 64)
				if err != nil {
					t.Fatalf("parse replay deadline: %v", err)
				}
			}
			t.Cleanup(func() {
				_ = os.Remove(replayPath)
				_ = os.Remove(metadataPath)
			})
			if replayBefore < time.Now().Add(22*time.Hour).Unix() || replayBefore > time.Now().Add(23*time.Hour+time.Minute).Unix() {
				t.Fatalf("unexpected replay deadline: %d", replayBefore)
			}
			replay, err := os.ReadFile(replayPath)
			if err != nil {
				t.Fatalf("read replay: %v", err)
			}
			info, err := os.Stat(replayPath)
			if err != nil {
				t.Fatalf("stat replay: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("replay permissions = %v", info.Mode().Perm())
			}
			metadataBytes, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatalf("read replay metadata: %v", err)
			}
			metadataInfo, err := os.Stat(metadataPath)
			if err != nil || metadataInfo.Mode().Perm() != 0o600 {
				t.Fatalf("replay metadata permissions: info=%v err=%v", metadataInfo, err)
			}
			var metadata replayMetadata
			if err := decodeStrictJSON(metadataBytes, &metadata); err != nil {
				t.Fatalf("decode replay metadata: %v", err)
			}
			digest := sha256.Sum256(replay)
			if metadata.OperationID == "" || metadata.Origin != "https://contentflow.example" || metadata.RequestSHA256 != fmt.Sprintf("%x", digest[:]) || metadata.ReplayBefore != replayBefore || metadata.TemporaryRequest == nil || !*metadata.TemporaryRequest {
				t.Fatalf("invalid replay metadata: %#v", metadata)
			}
			for _, body := range bodies {
				if !bytes.Equal(body, replay) {
					t.Fatalf("replay differs from frozen attempt: replay=%q body=%q", replay, body)
				}
			}
			if !bytes.Contains(replay, []byte(test.stdin)) && test.json {
				t.Fatalf("replay lost exact transcript: %q", replay)
			}
			var replayArguments []string
			if strings.Contains(test.arguments[1], "batch") {
				replayArguments = []string{"content", "batch-create", "--file", replayPath, "--operation-id", metadata.OperationID, "--replay-metadata", metadataPath}
			} else {
				replayArguments = []string{"content", "create", "--file", replayPath, "--operation-id", metadata.OperationID, "--replay-metadata", metadataPath}
			}
			if test.json {
				replayArguments = append(replayArguments, "--json")
			}
			var replayBodies [][]byte
			validReplay := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				replayBodies = append(replayBodies, body)
				return nil, timeoutError{}
			})}, "", replayArguments...)
			if validReplay.exitCode != ExitUnavailable || len(replayBodies) != mutationAttempts || !strings.Contains(validReplay.stderr, metadataPath) {
				t.Fatalf("valid replay was not preserved: %#v attempts=%d", validReplay, len(replayBodies))
			}
			for _, replayBody := range replayBodies {
				if !bytes.Equal(replayBody, replay) {
					t.Fatalf("replayed request changed: got=%q want=%q", replayBody, replay)
				}
			}
			var wrongOriginRequests atomic.Int64
			wrongOrigin := invoke(t, "https://other-contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				wrongOriginRequests.Add(1)
				return nil, timeoutError{}
			})}, "", replayArguments...)
			if wrongOrigin.exitCode != ExitUsage || wrongOrigin.stdout != "" || wrongOriginRequests.Load() != 0 || strings.Contains(wrongOrigin.stderr, fullToken) || !strings.Contains(wrongOrigin.stderr, "replay metadata does not match request") {
				t.Fatalf("cross-origin replay was not rejected locally: %#v requests=%d", wrongOrigin, wrongOriginRequests.Load())
			}

			metadata.ReplayBefore = 1
			encodedMetadata, err := marshalJSON(metadata)
			if err != nil {
				t.Fatalf("write expired replay metadata: %v", err)
			}
			if err := os.WriteFile(metadataPath, encodedMetadata, 0o600); err != nil {
				t.Fatalf("write expired replay metadata: %v", err)
			}
			future := time.Now().Add(48 * time.Hour)
			if err := os.Chtimes(replayPath, future, future); err != nil {
				t.Fatalf("touch replay file: %v", err)
			}
			var replayRequests atomic.Int64
			expired := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				replayRequests.Add(1)
				return nil, timeoutError{}
			})}, "", replayArguments...)
			if expired.exitCode != ExitUsage || expired.stdout != "" || replayRequests.Load() != 0 || strings.Contains(expired.stderr, fullToken) || !strings.Contains(expired.stderr, "replay deadline has passed") {
				t.Fatalf("expired replay was not rejected locally: %#v requests=%d", expired, replayRequests.Load())
			}
		})
	}
}

func TestNonRegularRequestFailureRetainsExactFrozenReplay(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			operationID := "01J00000000000000000000086"
			fifo := filepath.Join(t.TempDir(), "request.fifo")
			if err := makeTestFIFO(fifo, 0o600); err != nil {
				t.Skipf("named pipes are unavailable: %v", err)
			}
			payload := []byte(`{"type":"x","working_title":"FIFO recovery","status":"draft","content":{"body":"exact one-shot input"}}`)
			writerDone := make(chan error, 1)
			go func() {
				writer, err := os.OpenFile(fifo, os.O_WRONLY, 0)
				if err == nil {
					_, err = writer.Write(payload)
					_ = writer.Close()
				}
				writerDone <- err
			}()
			arguments := []string{"content", "create", "--file", fifo, "--operation-id", operationID}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			var attempts [][]byte
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				attempts = append(attempts, slices.Clone(body))
				return nil, timeoutError{}
			})}, "", arguments...)
			if err := <-writerDone; err != nil {
				t.Fatalf("write FIFO: %v", err)
			}
			if result.exitCode != ExitUnavailable || result.stdout != "" || len(attempts) != mutationAttempts || strings.Contains(result.stderr, fullToken) {
				t.Fatalf("one-shot request was not recoverable: result=%#v attempts=%d", result, len(attempts))
			}
			var replayPath, metadataPath string
			if jsonOutput {
				var problem struct {
					Error          string `json:"error"`
					OperationID    string `json:"operation_id"`
					ReplayFile     string `json:"replay_file"`
					ReplayMetadata string `json:"replay_metadata"`
					ReplayBefore   int64  `json:"replay_before"`
				}
				if err := decodeStrictJSON([]byte(result.stderr), &problem); err != nil || problem.Error != "request_failed" || problem.OperationID != operationID {
					t.Fatalf("unexpected JSON recovery: result=%#v problem=%#v err=%v", result, problem, err)
				}
				replayPath, metadataPath = problem.ReplayFile, problem.ReplayMetadata
			} else {
				lines := strings.Split(strings.TrimSuffix(result.stderr, "\n"), "\n")
				if len(lines) != 5 || lines[0] != "error: request_failed" || lines[1] != "operation_id: "+operationID || !strings.HasPrefix(lines[2], "replay_file: ") || !strings.HasPrefix(lines[3], "replay_metadata: ") {
					t.Fatalf("unexpected human recovery: %q", result.stderr)
				}
				replayPath = strings.TrimPrefix(lines[2], "replay_file: ")
				metadataPath = strings.TrimPrefix(lines[3], "replay_metadata: ")
			}
			t.Cleanup(func() {
				_ = os.Remove(replayPath)
				_ = os.Remove(metadataPath)
				_ = os.Remove(filepath.Dir(replayPath))
			})
			frozen, err := os.ReadFile(replayPath)
			if err != nil {
				t.Fatalf("read FIFO replay: %v", err)
			}
			for _, attempt := range attempts {
				if !bytes.Equal(attempt, frozen) {
					t.Fatalf("FIFO retry bytes changed: attempt=%q frozen=%q", attempt, frozen)
				}
			}
			if bytes.Equal(frozen, payload) || !bytes.Contains(frozen, []byte(operationID)) {
				t.Fatalf("replay did not contain the exact finalized request: %q", frozen)
			}
			itemID := "01J00000000000000000000087"
			replayed := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				if !bytes.Equal(body, frozen) {
					t.Fatalf("replayed FIFO bytes changed: got=%q want=%q", body, frozen)
				}
				response := fmt.Sprintf(`{"operation_id":%q,"item_ids":[%q],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, operationID, itemID)
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})}, "", "content", "create", "--file", replayPath, "--operation-id", operationID, "--replay-metadata", metadataPath, "--json")
			if replayed.exitCode != ExitSuccess || replayed.stderr != "" || !strings.Contains(replayed.stdout, operationID) {
				t.Fatalf("FIFO replay failed: %#v", replayed)
			}
			for _, path := range []string{replayPath, metadataPath, filepath.Dir(replayPath)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("successful FIFO replay retained %s: %v", path, err)
				}
			}
		})
	}
}

func TestSuccessfulReplayPreservesOperatorOwnedRequestCopy(t *testing.T) {
	operationID := "01J00000000000000000000076"
	initial := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})}, `{"type":"x","working_title":"Copied replay","status":"draft","content":{"body":"body"}}`, "content", "create", "--file", "-", "--operation-id", operationID, "--json")
	if initial.exitCode != ExitUnavailable || initial.stdout != "" {
		t.Fatalf("stdin replay precondition failed: %#v", initial)
	}
	var problem struct {
		ReplayFile     string `json:"replay_file"`
		ReplayMetadata string `json:"replay_metadata"`
	}
	if err := json.Unmarshal([]byte(initial.stderr), &problem); err != nil || problem.ReplayFile == "" || problem.ReplayMetadata == "" {
		t.Fatalf("stdin replay recovery was missing: %#v err=%v", initial, err)
	}
	t.Cleanup(func() {
		_ = os.Remove(problem.ReplayFile)
		_ = os.Remove(problem.ReplayMetadata)
	})
	frozen, err := os.ReadFile(problem.ReplayFile)
	if err != nil {
		t.Fatal(err)
	}
	operatorCopy := writeTestFile(t, "operator-replay-copy.json", string(frozen))
	replayed := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"operation_id":"` + operationID + `","item_ids":["01J00000000000000000000077"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, "", "content", "create", "--file", operatorCopy, "--operation-id", operationID, "--replay-metadata", problem.ReplayMetadata, "--json")
	if replayed.exitCode != ExitSuccess || replayed.stderr != "" {
		t.Fatalf("copied replay failed: %#v", replayed)
	}
	preserved, err := os.ReadFile(operatorCopy)
	if err != nil || !bytes.Equal(preserved, frozen) {
		t.Fatalf("operator-owned replay copy was changed or removed: err=%v", err)
	}
	for _, generated := range []string{problem.ReplayFile, problem.ReplayMetadata, filepath.Dir(problem.ReplayFile)} {
		if _, err := os.Stat(generated); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generated recovery file was retained after success: %s err=%v", generated, err)
		}
	}
}

func TestRelocatedRecoveryBundleReplaysAndRemainsOperatorOwned(t *testing.T) {
	operationID := "01J00000000000000000000090"
	initial := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})}, `{"type":"x","working_title":"Durable recovery","status":"draft","content":{"body":"body"}}`, "content", "create", "--file", "-", "--operation-id", operationID, "--json")
	var recovery struct {
		ReplayFile     string `json:"replay_file"`
		ReplayMetadata string `json:"replay_metadata"`
	}
	if initial.exitCode != ExitUnavailable || json.Unmarshal([]byte(initial.stderr), &recovery) != nil || recovery.ReplayFile == "" || recovery.ReplayMetadata == "" {
		t.Fatalf("relocated recovery precondition failed: %#v", initial)
	}
	durableDirectory := filepath.Join(t.TempDir(), "durable-recovery")
	if err := os.Mkdir(durableDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	durableRequest := filepath.Join(durableDirectory, "request.json")
	durableMetadata := filepath.Join(durableDirectory, "metadata.json")
	for source, destination := range map[string]string{recovery.ReplayFile: durableRequest, recovery.ReplayMetadata: durableMetadata} {
		contents, err := os.ReadFile(source)
		if err != nil || os.WriteFile(destination, contents, 0o600) != nil {
			t.Fatalf("copy recovery bundle %s: %v", source, err)
		}
	}
	removeRecoveryFiles(recovery.ReplayFile, recovery.ReplayMetadata)
	removeRecoveryBundleDirectory(recovery.ReplayFile, recovery.ReplayMetadata)
	requestBefore, _ := os.ReadFile(durableRequest)
	metadataBefore, _ := os.ReadFile(durableMetadata)
	itemID := "01J00000000000000000000091"
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if !bytes.Equal(body, requestBefore) {
			t.Fatalf("relocated replay bytes changed: got=%q want=%q", body, requestBefore)
		}
		response := fmt.Sprintf(`{"operation_id":%q,"item_ids":[%q],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, operationID, itemID)
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})}, "", "content", "create", "--file", durableRequest, "--operation-id", operationID, "--replay-metadata", durableMetadata, "--json")
	if result.exitCode != ExitSuccess || result.stderr != "" {
		t.Fatalf("relocated recovery bundle failed: %#v", result)
	}
	requestAfter, requestErr := os.ReadFile(durableRequest)
	metadataAfter, metadataErr := os.ReadFile(durableMetadata)
	info, directoryErr := os.Stat(durableDirectory)
	if requestErr != nil || metadataErr != nil || directoryErr != nil || info.Mode().Perm() != 0o700 || !bytes.Equal(requestAfter, requestBefore) || !bytes.Equal(metadataAfter, metadataBefore) {
		t.Fatalf("operator recovery bundle changed: requestErr=%v metadataErr=%v directoryErr=%v", requestErr, metadataErr, directoryErr)
	}
}

func TestReservedTempPrefixRecoveryCopiesRemainOperatorOwned(t *testing.T) {
	t.Run("complete bundle", func(t *testing.T) {
		operationID := "01J00000000000000000000092"
		initial := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, timeoutError{}
		})}, `{"type":"x","working_title":"Reserved bundle","status":"draft","content":{"body":"body"}}`, "content", "create", "--file", "-", "--operation-id", operationID, "--json")
		var recovery struct {
			ReplayFile     string `json:"replay_file"`
			ReplayMetadata string `json:"replay_metadata"`
		}
		if initial.exitCode != ExitUnavailable || json.Unmarshal([]byte(initial.stderr), &recovery) != nil || recovery.ReplayFile == "" || recovery.ReplayMetadata == "" {
			t.Fatalf("reserved bundle precondition failed: %#v", initial)
		}
		operatorDirectory, err := os.MkdirTemp("", "contentflow-recovery-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(operatorDirectory) })
		operatorRequest := filepath.Join(operatorDirectory, "request.json")
		operatorMetadata := filepath.Join(operatorDirectory, "metadata.json")
		for source, destination := range map[string]string{recovery.ReplayFile: operatorRequest, recovery.ReplayMetadata: operatorMetadata} {
			contents, readErr := os.ReadFile(source)
			if readErr != nil || os.WriteFile(destination, contents, 0o600) != nil {
				t.Fatalf("copy recovery artifact %s: %v", source, readErr)
			}
		}
		removeRecoveryFiles(recovery.ReplayFile, recovery.ReplayMetadata)
		removeRecoveryBundleDirectory(recovery.ReplayFile, recovery.ReplayMetadata)

		result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"operation_id":%q,"item_ids":["01J00000000000000000000093"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, operationID)
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}, "", "content", "create", "--file", operatorRequest, "--operation-id", operationID, "--replay-metadata", operatorMetadata, "--json")
		if result.exitCode != ExitSuccess || result.stderr != "" {
			t.Fatalf("reserved-prefix bundle replay failed: %#v", result)
		}
		for _, path := range []string{operatorRequest, operatorMetadata, operatorDirectory} {
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("operator bundle artifact was removed: %s err=%v", path, statErr)
			}
		}
	})

	t.Run("standalone metadata", func(t *testing.T) {
		operationID := "01J00000000000000000000094"
		requestPath := writeTestFile(t, "reserved-standalone-request.json", `{"type":"x","working_title":"Reserved metadata","status":"draft","content":{"body":"body"}}`)
		initial := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, timeoutError{}
		})}, "", "content", "create", "--file", requestPath, "--operation-id", operationID, "--json")
		metadataPath, _ := assertFileMutationRecovery(t, initial.stderr, true, "request_failed", operationID)
		metadataBytes, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		operatorFile, err := os.CreateTemp("", "contentflow-replay-metadata-*.json")
		if err != nil {
			t.Fatal(err)
		}
		operatorMetadata := operatorFile.Name()
		t.Cleanup(func() { _ = os.Remove(operatorMetadata) })
		if _, err = operatorFile.Write(metadataBytes); err != nil {
			t.Fatal(err)
		}
		if err = operatorFile.Close(); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(metadataPath)

		result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"operation_id":%q,"item_ids":["01J00000000000000000000095"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, operationID)
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}, "", "content", "create", "--file", requestPath, "--operation-id", operationID, "--replay-metadata", operatorMetadata, "--json")
		if result.exitCode != ExitSuccess || result.stderr != "" {
			t.Fatalf("reserved-prefix metadata replay failed: %#v", result)
		}
		if preserved, readErr := os.ReadFile(operatorMetadata); readErr != nil || !bytes.Equal(preserved, metadataBytes) {
			t.Fatalf("operator metadata copy was changed or removed: err=%v", readErr)
		}
	})
}

func TestInvalidUTF8ArgumentsAreRejectedWithStableOutput(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			var requests atomic.Int64
			arguments := []string{"content", "create", "--file", "/tmp/contentflow-invalid-\xff.json"}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, timeoutError{}
			})}, "", arguments...)
			if result.exitCode != ExitUsage || result.stdout != "" || requests.Load() != 0 || !utf8.ValidString(result.stderr) || strings.ContainsRune(result.stderr, '\ufffd') || !strings.Contains(result.stderr, "arguments must be valid UTF-8") {
				t.Fatalf("invalid UTF-8 argument output was unstable: result=%#v requests=%d", result, requests.Load())
			}
			if jsonOutput {
				var problem map[string]any
				if json.Unmarshal([]byte(result.stderr), &problem) != nil || problem["error"] != "usage_error" {
					t.Fatalf("invalid UTF-8 JSON error changed: %q", result.stderr)
				}
			}
		})
	}
	if got := safeHumanPath("/tmp/contentflow-invalid-\xff"); !utf8.ValidString(got) || !strings.Contains(got, `\xff`) {
		t.Fatalf("invalid UTF-8 recovery path was not byte-quoted: %q", got)
	}
}

func TestInvalidUTF8PreDispatchArgumentsAreRejected(t *testing.T) {
	positions := map[string][]string{
		"group":     {"content\xff", "list"},
		"command":   {"content", "list\xff"},
		"help tail": {"content", "list", "detail\xff", "--help"},
	}
	for name, baseArguments := range positions {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", name, jsonOutput), func(t *testing.T) {
				var requests atomic.Int64
				arguments := slices.Clone(baseArguments)
				if jsonOutput {
					arguments = append(arguments, "--json")
				}
				result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests.Add(1)
					return nil, timeoutError{}
				})}, "", arguments...)
				if result.exitCode != ExitUsage || result.stdout != "" || requests.Load() != 0 || !utf8.ValidString(result.stderr) || strings.ContainsRune(result.stderr, '\ufffd') || !strings.Contains(result.stderr, "arguments must be valid UTF-8") {
					t.Fatalf("invalid UTF-8 pre-dispatch output was unstable: result=%#v requests=%d", result, requests.Load())
				}
				if jsonOutput {
					var problem map[string]any
					if json.Unmarshal([]byte(result.stderr), &problem) != nil || problem["error"] != "usage_error" {
						t.Fatalf("invalid UTF-8 JSON error changed: %q", result.stderr)
					}
				}
			})
		}
	}
}

func TestDynamicUsageDiagnosticsAreSafelyQuoted(t *testing.T) {
	unsafe := "forged\noperation_id: forged\x1b[31m"
	tests := []struct {
		name      string
		arguments []string
	}{
		{"command", []string{"content", unsafe}},
		{"flag", []string{"content", "list", "--" + unsafe}},
		{"path", []string{"content", "create", "--file", filepath.Join(t.TempDir(), unsafe)}},
	}
	for _, test := range tests {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", test.name, jsonOutput), func(t *testing.T) {
				var requests atomic.Int64
				arguments := slices.Clone(test.arguments)
				if jsonOutput {
					arguments = append(arguments, "--json")
				}
				result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests.Add(1)
					return nil, timeoutError{}
				})}, "", arguments...)
				if result.exitCode != ExitUsage || result.stdout != "" || requests.Load() != 0 || strings.Contains(result.stderr, "\noperation_id: forged") || strings.ContainsRune(result.stderr, '\x1b') {
					t.Fatalf("dynamic diagnostic was not safely quoted: result=%#v requests=%d", result, requests.Load())
				}
				if jsonOutput {
					var problem struct {
						Error  string `json:"error"`
						Detail string `json:"detail"`
					}
					if json.Unmarshal([]byte(result.stderr), &problem) != nil || problem.Error != "usage_error" || strings.Contains(problem.Detail, "\noperation_id: forged") || strings.ContainsRune(problem.Detail, '\x1b') || !strings.Contains(problem.Detail, `\noperation_id: forged\x1b`) {
						t.Fatalf("dynamic JSON diagnostic was not safely quoted: %#v output=%q", problem, result.stderr)
					}
				} else if strings.Count(result.stderr, "\n") != 1 || !strings.Contains(result.stderr, `\noperation_id: forged\x1b`) {
					t.Fatalf("human diagnostic was not one stable line: %q", result.stderr)
				}
			})
		}
	}
}

func TestCraftedReplayMetadataCannotDeleteUnrelatedPath(t *testing.T) {
	operationID := "01J00000000000000000000078"
	request := writeTestFile(t, "crafted-cleanup-request.json", `{"type":"x","working_title":"Safe cleanup","status":"draft","content":{"body":"body"}}`)
	unrelated := writeTestFile(t, "must-survive.txt", "operator data")
	metadata := writeTestFile(t, "crafted-cleanup-metadata.json", fmt.Sprintf(`{"operation_id":%q,"origin":"https://contentflow.example","method":"POST","path":"/api/v1/content","request_sha256":"%064d","replay_before":%d,"temporary_request":true,"temporary_request_path":%q}`, operationID, 0, time.Now().Add(time.Hour).Unix(), unrelated))
	var requests atomic.Int64
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, timeoutError{}
	})}, "", "content", "create", "--file", request, "--operation-id", operationID, "--replay-metadata", metadata, "--json")
	preserved, err := os.ReadFile(unrelated)
	if result.exitCode != ExitUsage || result.stdout != "" || requests.Load() != 0 || err != nil || string(preserved) != "operator data" || strings.Contains(result.stderr, unrelated) {
		t.Fatalf("crafted cleanup metadata was unsafe: result=%#v requests=%d readErr=%v", result, requests.Load(), err)
	}
}

func TestTerminalReplayFromOperatorCopyRemovesHiddenGeneratedBundle(t *testing.T) {
	operationID := "01J00000000000000000000084"
	initial := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})}, `{"type":"x","working_title":"Terminal copy","status":"draft","content":{"body":"body"}}`, "content", "create", "--file", "-", "--operation-id", operationID, "--json")
	var recovery struct {
		ReplayFile     string `json:"replay_file"`
		ReplayMetadata string `json:"replay_metadata"`
	}
	if initial.exitCode != ExitUnavailable || json.Unmarshal([]byte(initial.stderr), &recovery) != nil || recovery.ReplayFile == "" || recovery.ReplayMetadata == "" {
		t.Fatalf("terminal copy precondition failed: %#v", initial)
	}
	t.Cleanup(func() {
		_ = os.Remove(recovery.ReplayFile)
		_ = os.Remove(recovery.ReplayMetadata)
		_ = os.Remove(filepath.Dir(recovery.ReplayFile))
	})
	frozen, err := os.ReadFile(recovery.ReplayFile)
	if err != nil {
		t.Fatal(err)
	}
	operatorCopy := writeTestFile(t, "terminal-operator-copy.json", string(frozen))
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid_request"}`))}, nil
	})}, "", "content", "create", "--file", operatorCopy, "--operation-id", operationID, "--replay-metadata", recovery.ReplayMetadata, "--json")
	var problem struct {
		Error       string `json:"error"`
		RequestFile string `json:"request_file"`
	}
	if result.exitCode != ExitInvalid || json.Unmarshal([]byte(result.stderr), &problem) != nil || problem.Error != "invalid_request" || problem.RequestFile != operatorCopy {
		t.Fatalf("terminal operator copy output changed: result=%#v problem=%#v", result, problem)
	}
	if preserved, err := os.ReadFile(operatorCopy); err != nil || !bytes.Equal(preserved, frozen) {
		t.Fatalf("terminal operator copy was removed: err=%v", err)
	}
	for _, generated := range []string{recovery.ReplayFile, recovery.ReplayMetadata, filepath.Dir(recovery.ReplayFile)} {
		if _, err := os.Stat(generated); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("terminal replay left hidden generated recovery: %s err=%v", generated, err)
		}
	}
}

func TestStdinMutationTerminalFailureUsesRequestSnapshot(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonOutput), func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_request"}`)),
				}, nil
			})
			arguments := []string{"content", "batch-create", "--file", "-", "--operation-id", "01J00000000000000000000073"}
			if jsonOutput {
				arguments = append(arguments, "--json")
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, `{"items":[{"type":"x","working_title":"Rejected","status":"draft","content":{"body":"body"}}]}`, arguments...)
			if result.exitCode != ExitInvalid || result.stdout != "" || strings.Contains(result.stderr, "replay_file") {
				t.Fatalf("terminal response was labeled replayable: %#v", result)
			}
			var requestPath string
			if jsonOutput {
				var problem struct {
					Error       string `json:"error"`
					OperationID string `json:"operation_id"`
					RequestFile string `json:"request_file"`
				}
				if err := json.Unmarshal([]byte(result.stderr), &problem); err != nil || problem.Error != "invalid_request" || problem.OperationID != "01J00000000000000000000073" {
					t.Fatalf("unexpected terminal JSON output: %q, %v", result.stderr, err)
				}
				requestPath = problem.RequestFile
			} else {
				const prefix = "error: invalid_request\noperation_id: 01J00000000000000000000073\nrequest_file: "
				if !strings.HasPrefix(result.stderr, prefix) || !strings.HasSuffix(result.stderr, "\n") {
					t.Fatalf("unexpected terminal human output: %q", result.stderr)
				}
				requestPath = strings.TrimSuffix(strings.TrimPrefix(result.stderr, prefix), "\n")
			}
			t.Cleanup(func() { _ = os.Remove(requestPath) })
			if info, err := os.Stat(requestPath); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("request snapshot is unavailable or unsafe: info=%v err=%v", info, err)
			}
		})
	}
}

func TestTranscriptOverridesRetainExactFrozenReplay(t *testing.T) {
	for _, test := range []struct {
		name         string
		jsonOutput   bool
		clear        bool
		wantFragment string
	}{
		{name: "path human", wantFragment: `"transcript":"merged transcript"`},
		{name: "path JSON", jsonOutput: true, wantFragment: `"transcript":"merged transcript"`},
		{name: "clear human", clear: true, wantFragment: `"transcript":""`},
		{name: "clear JSON", jsonOutput: true, clear: true, wantFragment: `"transcript":""`},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestFile := writeTestFile(t, "transcript-override.json", `{"type":"youtube","working_title":"Frozen transcript","status":"draft","content":{"transcript":"old transcript","sections":[]}}`)
			arguments := []string{"content", "create", "--file", requestFile, "--operation-id", "01J00000000000000000000079"}
			if test.clear {
				arguments = append(arguments, "--clear-transcript")
			} else {
				arguments = append(arguments, "--transcript-file", writeTestFile(t, "merged-transcript.txt", "merged transcript"))
			}
			if test.jsonOutput {
				arguments = append(arguments, "--json")
			}
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, timeoutError{}
			})}, "", arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || strings.Contains(result.stderr, fullToken) {
				t.Fatalf("transcript override was not retained: %#v", result)
			}
			var replayPath, metadataPath string
			if test.jsonOutput {
				var problem struct {
					Error          string `json:"error"`
					OperationID    string `json:"operation_id"`
					ReplayFile     string `json:"replay_file"`
					ReplayMetadata string `json:"replay_metadata"`
				}
				if err := json.Unmarshal([]byte(result.stderr), &problem); err != nil || problem.Error != "request_failed" || problem.OperationID != "01J00000000000000000000079" {
					t.Fatalf("unexpected transcript recovery output: %#v err=%v", problem, err)
				}
				replayPath, metadataPath = problem.ReplayFile, problem.ReplayMetadata
			} else {
				lines := strings.Split(strings.TrimSuffix(result.stderr, "\n"), "\n")
				if len(lines) != 5 || !strings.HasPrefix(lines[2], "replay_file: ") || !strings.HasPrefix(lines[3], "replay_metadata: ") {
					t.Fatalf("unexpected transcript recovery output: %q", result.stderr)
				}
				replayPath = strings.TrimPrefix(lines[2], "replay_file: ")
				metadataPath = strings.TrimPrefix(lines[3], "replay_metadata: ")
			}
			t.Cleanup(func() {
				_ = os.Remove(replayPath)
				_ = os.Remove(metadataPath)
			})
			replay, err := os.ReadFile(replayPath)
			if err != nil || !bytes.Contains(replay, []byte(test.wantFragment)) {
				t.Fatalf("frozen transcript request changed: err=%v body=%q", err, replay)
			}
			metadataBytes, err := os.ReadFile(metadataPath)
			var metadata replayMetadata
			if err != nil || decodeStrictJSON(metadataBytes, &metadata) != nil || metadata.TemporaryRequest == nil || !*metadata.TemporaryRequest {
				t.Fatalf("transcript recovery metadata is invalid: err=%v metadata=%#v", err, metadata)
			}
			var sent []byte
			replayArguments := []string{"content", "create", "--file", replayPath, "--operation-id", metadata.OperationID, "--replay-metadata", metadataPath, "--json"}
			replayed := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				sent, _ = io.ReadAll(request.Body)
				body := fmt.Sprintf(`{"operation_id":%q,"item_ids":["01J00000000000000000000080"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, metadata.OperationID)
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, "", replayArguments...)
			if replayed.exitCode != ExitSuccess || !bytes.Equal(sent, replay) {
				t.Fatalf("transcript replay changed frozen bytes: result=%#v sent=%q want=%q", replayed, sent, replay)
			}
			for _, path := range []string{replayPath, metadataPath} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("successful transcript replay retained %s: %v", path, err)
				}
			}
		})
	}
}

func TestReplayBeforePreservesCallerRecoveryDeadline(t *testing.T) {
	input := writeTestFile(t, "bounded-external-replay.json", `{"items":[{"type":"x","working_title":"Bounded","status":"draft","content":{"body":"body"}}]}`)
	deadline := time.Now().UTC().Add(time.Hour).Unix()
	operationID := "01J00000000000000000000078"
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})}, "", "content", "batch-create", "--file", input, "--operation-id", operationID, "--replay-before", fmt.Sprint(deadline), "--json")
	var problem struct {
		Error          string `json:"error"`
		OperationID    string `json:"operation_id"`
		ReplayMetadata string `json:"replay_metadata"`
		ReplayBefore   int64  `json:"replay_before"`
	}
	if result.exitCode != ExitUnavailable || json.Unmarshal([]byte(result.stderr), &problem) != nil || problem.Error != "request_failed" || problem.OperationID != operationID || problem.ReplayBefore != deadline || strings.Contains(result.stderr, fullToken) {
		t.Fatalf("caller replay deadline changed: result=%#v problem=%#v", result, problem)
	}
	t.Cleanup(func() { _ = os.Remove(problem.ReplayMetadata) })
	metadataBytes, err := os.ReadFile(problem.ReplayMetadata)
	var metadata replayMetadata
	if err != nil || decodeStrictJSON(metadataBytes, &metadata) != nil || metadata.ReplayBefore != deadline {
		t.Fatalf("persisted caller deadline changed: metadata=%#v err=%v", metadata, err)
	}

	requests := atomic.Int64{}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, timeoutError{}
	})}
	for _, value := range []string{fmt.Sprint(time.Now().UTC().Add(-time.Minute).Unix()), fmt.Sprint(time.Now().UTC().Add(24 * time.Hour).Unix())} {
		invalid := invoke(t, "https://contentflow.example", fullToken, client, "", "content", "batch-create", "--file", input, "--operation-id", operationID, "--replay-before", value, "--json")
		if invalid.exitCode != ExitUsage || invalid.stdout != "" || !strings.Contains(invalid.stderr, "future Unix time within 23 hours") {
			t.Fatalf("unsafe caller deadline was accepted: %#v", invalid)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe caller deadlines sent %d requests", requests.Load())
	}
}

func TestOperationIDsAreValidatedBeforeRequestsOrOutput(t *testing.T) {
	inputWithID := writeTestFile(t, "unsafe-operation.json", `{"type":"x","working_title":"Unsafe","status":"draft","operation_id":"bad\n\u001b[31m","content":{"body":"body"}}`)
	input := writeTestFile(t, "safe-operation-input.json", `{"type":"x","working_title":"Unsafe","status":"draft","content":{"body":"body"}}`)
	requests := atomic.Int64{}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})
	tests := []struct {
		name      string
		arguments []string
	}{
		{"file", []string{"content", "create", "--file", inputWithID}},
		{"flag JSON", []string{"content", "create", "--file", input, "--operation-id", "bad\n\x1b[31m", "--json"}},
		{"revision", []string{"content", "archive", "01J00000000000000000000001", "--revision", "1", "--operation-id", "not-a-ulid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUsage || result.stdout != "" || strings.Contains(result.stderr, "bad") || strings.ContainsRune(result.stderr, '\x1b') {
				t.Fatalf("unsafe operation ID reached output: %#v", result)
			}
		})
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run(t.Context(), []string{"content", "create", "--file", input}, Options{
		APIURL: "https://contentflow.example", Token: fullToken, HTTPClient: &http.Client{Transport: transport},
		Stdout: &stdout, Stderr: &stderr, NewOperationID: func() (string, error) { return "generated\nunsafe", nil },
	})
	if exitCode != ExitUsage || strings.Contains(stderr.String(), "generated") || requests.Load() != 0 {
		t.Fatalf("unsafe generated ID was accepted: exit=%d stderr=%q requests=%d", exitCode, stderr.String(), requests.Load())
	}
}

func TestBatchInputAndResponseRevisionAreBoundBeforeSuccess(t *testing.T) {
	requestCount := atomic.Int64{}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount.Add(1)
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"operation_id":"01J00000000000000000000073","item_ids":["01J00000000000000000000001"],"revisions":[2],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`))}, nil
	})
	for _, body := range []string{`{}`, `{"items":null}`, `{"items":[]}`, `{"items":{}}`} {
		result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "batch-create", "--file", writeTestFile(t, "invalid-batch.json", body), "--json")
		if result.exitCode != ExitUsage {
			t.Fatalf("invalid batch reached API: body=%s result=%#v", body, result)
		}
	}
	valid := writeTestFile(t, "revision-two-batch.json", `{"items":[{"type":"x","working_title":"Revision","status":"draft","content":{"body":"body"}}]}`)
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", "content", "batch-create", "--file", valid, "--operation-id", "01J00000000000000000000073", "--json")
	if result.exitCode != ExitUnavailable || result.stdout != "" || requestCount.Load() != 1 {
		t.Fatalf("invalid batch revision was accepted: %#v requests=%d", result, requestCount.Load())
	}
	assertFileMutationRecovery(t, result.stderr, true, "invalid_api_response", "01J00000000000000000000073")
}

func TestExhaustedMutationRetriesReportGeneratedOperationIDWithoutSecrets(t *testing.T) {
	tests := []struct {
		name       string
		operation  string
		arguments  []string
		jsonOutput bool
	}{
		{
			name:      "create human",
			operation: "01J00000000000000000000031",
			arguments: []string{"content", "create", "--file", writeTestFile(t, "exhausted-create.json", `{"type":"x","working_title":"Uncertain create","status":"draft","content":{"body":"body"}}`)},
		},
		{
			name:       "batch JSON",
			operation:  "01J00000000000000000000032",
			arguments:  []string{"content", "batch-create", "--file", writeTestFile(t, "exhausted-batch.json", `{"items":[{"type":"x","working_title":"Uncertain batch","status":"draft","content":{"body":"body"}}]}`), "--json"},
			jsonOutput: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var bodies [][]byte
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				bodies = append(bodies, body)
				return nil, timeoutError{}
			})
			var stdout, stderr bytes.Buffer
			exitCode := Run(context.Background(), test.arguments, Options{
				APIURL: "https://contentflow.example", Token: fullToken,
				HTTPClient: &http.Client{Transport: transport}, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
				NewOperationID: func() (string, error) { return test.operation, nil },
				Sleep:          func(context.Context, time.Duration) error { return nil },
			})
			if exitCode != ExitUnavailable || stdout.String() != "" || strings.Contains(stderr.String(), fullToken) {
				t.Fatalf("exhausted retry output was unsafe: exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			metadataPath, metadata := assertFileMutationRecovery(t, stderr.String(), test.jsonOutput, "request_failed", test.operation)
			if len(bodies) != mutationAttempts {
				t.Fatalf("sent %d requests, want %d", len(bodies), mutationAttempts)
			}
			for _, body := range bodies {
				if !bytes.Equal(body, bodies[0]) || bytes.Count(body, []byte(test.operation)) != 1 {
					t.Fatalf("retry body was not frozen: %q", body)
				}
			}
			metadataBytes, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			validMetadata := filepath.Join(t.TempDir(), "valid-file-replay.json")
			if err := os.WriteFile(validMetadata, metadataBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			replayArguments := append(slices.Clone(test.arguments), "--operation-id", test.operation, "--replay-metadata", validMetadata)
			var replayBody []byte
			validReplay := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				replayBody, _ = io.ReadAll(request.Body)
				responseBody := fmt.Sprintf(`{"operation_id":%q,"item_ids":["01J00000000000000000000080"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, test.operation)
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			})}, "", replayArguments...)
			if validReplay.exitCode != ExitSuccess || !bytes.Equal(replayBody, bodies[0]) {
				t.Fatalf("valid file-backed replay changed request: result=%#v body=%q want=%q", validReplay, replayBody, bodies[0])
			}
			if _, err := os.Stat(test.arguments[3]); err != nil {
				t.Fatalf("operator request file was removed: %v", err)
			}
			if preserved, err := os.ReadFile(validMetadata); err != nil || !bytes.Equal(preserved, metadataBytes) {
				t.Fatalf("operator replay metadata was not preserved: err=%v", err)
			}
			metadata.ReplayBefore = 1
			expiredBytes, err := marshalJSON(metadata)
			if err != nil {
				t.Fatalf("expire file-backed recovery metadata: %v", err)
			}
			if err := os.WriteFile(metadataPath, expiredBytes, 0o600); err != nil {
				t.Fatalf("expire file-backed recovery metadata: %v", err)
			}
			replayArguments[len(replayArguments)-1] = metadataPath
			var replayRequests atomic.Int64
			expired := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				replayRequests.Add(1)
				return nil, timeoutError{}
			})}, "", replayArguments...)
			if expired.exitCode != ExitUsage || expired.stdout != "" || replayRequests.Load() != 0 || !strings.Contains(expired.stderr, "replay deadline has passed") || strings.Contains(expired.stderr, fullToken) {
				t.Fatalf("expired file-backed replay was not blocked locally: %#v requests=%d", expired, replayRequests.Load())
			}
		})
	}
}

func TestIndeterminateMutationResponsesReportGeneratedOperationID(t *testing.T) {
	input := writeTestFile(t, "indeterminate.json", `{"type":"x","working_title":"Indeterminate","status":"draft","content":{"body":"body"}}`)
	batchInput := writeTestFile(t, "indeterminate-batch.json", `{"items":[{"type":"x","working_title":"One","status":"draft","content":{"body":"one"}},{"type":"x","working_title":"Two","status":"draft","content":{"body":"two"}}]}`)
	tests := []struct {
		name       string
		status     int
		body       string
		arguments  []string
		code       string
		jsonOutput bool
	}{
		{
			name: "gateway human", status: http.StatusBadGateway, body: `{"error":"upstream_failed","detail":"` + fullToken + `"}`,
			arguments: []string{"content", "create", "--file", input}, code: "api_error",
		},
		{
			name: "malformed success JSON", status: http.StatusCreated, body: `{` + fullToken,
			arguments: []string{"content", "create", "--file", input, "--json"}, code: "invalid_api_response", jsonOutput: true,
		},
		{
			name: "invalid success shape JSON", status: http.StatusCreated, body: `{}`,
			arguments: []string{"content", "create", "--file", input, "--json"}, code: "invalid_api_response", jsonOutput: true,
		},
		{
			name: "missing result fields human", status: http.StatusCreated, body: `{"operation_id":"01J00000000000000000000033","status":"created"}`,
			arguments: []string{"content", "create", "--file", input}, code: "invalid_api_response",
		},
		{
			name: "mismatched result arrays JSON", status: http.StatusCreated, body: `{"operation_id":"01J00000000000000000000033","item_ids":["01J00000000000000000000034"],"revisions":[],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`,
			arguments: []string{"content", "create", "--file", input, "--json"}, code: "invalid_api_response", jsonOutput: true,
		},
		{
			name: "wrong status human", status: http.StatusCreated, body: `{"operation_id":"01J00000000000000000000033","item_ids":["01J00000000000000000000034"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"updated"}`,
			arguments: []string{"content", "create", "--file", input}, code: "invalid_api_response",
		},
		{
			name: "partial batch JSON", status: http.StatusCreated, body: `{"operation_id":"01J00000000000000000000033","item_ids":["01J00000000000000000000034"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`,
			arguments: []string{"content", "batch-create", "--file", batchInput, "--json"}, code: "invalid_api_response", jsonOutput: true,
		},
		{
			name: "duplicate batch IDs human", status: http.StatusCreated, body: `{"operation_id":"01J00000000000000000000033","item_ids":["01J00000000000000000000034","01J00000000000000000000034"],"revisions":[1,1],"expires_at":["2026-10-10T09:00:00Z","2026-10-10T09:00:01Z"],"status":"created"}`,
			arguments: []string{"content", "batch-create", "--file", batchInput}, code: "invalid_api_response",
		},
		{
			name: "duplicate batch IDs JSON", status: http.StatusCreated, body: `{"operation_id":"01J00000000000000000000033","item_ids":["01J00000000000000000000034","01J00000000000000000000034"],"revisions":[1,1],"expires_at":["2026-10-10T09:00:00Z","2026-10-10T09:00:01Z"],"status":"created"}`,
			arguments: []string{"content", "batch-create", "--file", batchInput, "--json"}, code: "invalid_api_response", jsonOutput: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			var stdout, stderr bytes.Buffer
			exitCode := Run(context.Background(), test.arguments, Options{
				APIURL: "https://contentflow.example", Token: fullToken, HTTPClient: &http.Client{Transport: transport},
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
				NewOperationID: func() (string, error) { return "01J00000000000000000000033", nil },
				Sleep:          func(context.Context, time.Duration) error { return nil },
			})
			if exitCode != ExitUnavailable || stdout.String() != "" || strings.Contains(stderr.String(), fullToken) {
				t.Fatalf("indeterminate mutation output was unsafe: exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			assertFileMutationRecovery(t, stderr.String(), test.jsonOutput, test.code, "01J00000000000000000000033")
		})
	}
}

func TestMutationHTTPStatusMustMatchEndpointContract(t *testing.T) {
	operationID := "01J00000000000000000000033"
	contentID := "01J00000000000000000000047"
	createInput := writeTestFile(t, "http-status-create.json", `{"type":"x","working_title":"Create","status":"draft","content":{"body":"body"}}`)
	updateInput := writeTestFile(t, "http-status-update.json", `{"type":"x","working_title":"Update","status":"draft","revision":1,"content":{"body":"body"}}`)
	batchInput := writeTestFile(t, "http-status-batch.json", `{"items":[{"type":"x","working_title":"One","status":"draft","content":{"body":"one"}},{"type":"x","working_title":"Two","status":"draft","content":{"body":"two"}}]}`)
	tests := []struct {
		name       string
		httpStatus int
		body       string
		arguments  []string
		fileBacked bool
		jsonOutput bool
	}{
		{"create accepted human", http.StatusAccepted, `{"operation_id":"` + operationID + `","item_ids":["01J00000000000000000000034"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`, []string{"content", "create", "--file", createInput, "--operation-id", operationID}, true, false},
		{"batch OK JSON", http.StatusOK, `{"operation_id":"` + operationID + `","item_ids":["01J00000000000000000000034","01J00000000000000000000035"],"revisions":[1,1],"expires_at":["2026-10-10T09:00:00Z","2026-10-10T09:00:01Z"],"status":"created"}`, []string{"content", "batch-create", "--file", batchInput, "--operation-id", operationID, "--json"}, true, true},
		{"update created human", http.StatusCreated, `{"operation_id":"` + operationID + `","item_ids":["` + contentID + `"],"revisions":[2],"expires_at":["2026-10-10T09:00:00Z"],"status":"updated"}`, []string{"content", "update", contentID, "--file", updateInput, "--operation-id", operationID}, true, false},
		{"archive accepted JSON", http.StatusAccepted, `{"operation_id":"` + operationID + `","item_ids":["` + contentID + `"],"revisions":[2],"expires_at":["2026-10-10T09:00:00Z"],"status":"archived"}`, []string{"content", "archive", contentID, "--revision", "1", "--operation-id", operationID, "--json"}, false, true},
		{"restore created human", http.StatusCreated, `{"operation_id":"` + operationID + `","item_ids":["` + contentID + `"],"revisions":[2],"expires_at":["2026-10-10T09:00:00Z"],"status":"restored"}`, []string{"content", "restore", contentID, "--revision", "1", "--operation-id", operationID}, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.httpStatus, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || strings.Contains(result.stderr, fullToken) {
				t.Fatalf("wrong mutation HTTP status was accepted: %#v", result)
			}
			if test.fileBacked {
				assertFileMutationRecovery(t, result.stderr, test.jsonOutput, "invalid_api_response", operationID)
				return
			}
			want := "error: invalid_api_response\noperation_id: " + operationID + "\n"
			if test.jsonOutput {
				want = `{"error":"invalid_api_response","operation_id":"` + operationID + `"}` + "\n"
			}
			if result.stderr != want {
				t.Fatalf("wrong mutation status recovery changed: got=%q want=%q", result.stderr, want)
			}
		})
	}
}

func TestSingletonMutationResponseMustMatchRequestedContentID(t *testing.T) {
	input := writeTestFile(t, "mismatched-target.json", `{"type":"x","working_title":"Target","status":"draft","revision":1,"content":{"body":"body"}}`)
	tests := []struct {
		name       string
		status     string
		arguments  []string
		jsonOutput bool
	}{
		{"update human", "updated", []string{"content", "update", "01J00000000000000000000047", "--file", input, "--operation-id", "01J00000000000000000000049"}, false},
		{"archive JSON", "archived", []string{"content", "archive", "01J00000000000000000000047", "--revision", "1", "--operation-id", "01J00000000000000000000049", "--json"}, true},
		{"restore human", "restored", []string{"content", "restore", "01J00000000000000000000047", "--revision", "1", "--operation-id", "01J00000000000000000000049"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"operation_id":"01J00000000000000000000049","item_ids":["01J00000000000000000000048"],"revisions":[2],"expires_at":["2026-10-10T09:00:00Z"],"status":%q}`, test.status)
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" {
				t.Fatalf("mismatched target was accepted: %#v", result)
			}
			if test.status == "updated" {
				assertFileMutationRecovery(t, result.stderr, test.jsonOutput, "invalid_api_response", "01J00000000000000000000049")
			} else {
				want := "error: invalid_api_response\noperation_id: 01J00000000000000000000049\n"
				if test.jsonOutput {
					want = "{\"error\":\"invalid_api_response\",\"operation_id\":\"01J00000000000000000000049\"}\n"
				}
				if result.stderr != want {
					t.Fatalf("unexpected singleton recovery output: %#v", result)
				}
			}
		})
	}
}

func TestUnsafeAPIErrorCodesAreNotWrittenToTerminal(t *testing.T) {
	for _, remoteError := range []string{"forged\n\u001b[31m", fullToken} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("error=%q/json=%v", remoteError, jsonOutput), func(t *testing.T) {
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					body, _ := json.Marshal(map[string]string{"error": remoteError})
					return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
				})
				arguments := []string{"content", "list"}
				want := "error: api_error\n"
				if jsonOutput {
					arguments = append(arguments, "--json")
					want = "{\"error\":\"api_error\"}\n"
				}
				result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, "", arguments...)
				if result.exitCode != ExitInvalid || result.stdout != "" || result.stderr != want || strings.Contains(result.stderr, "forged") || strings.Contains(result.stderr, fullToken) || strings.ContainsRune(result.stderr, '\x1b') {
					t.Fatalf("unsafe API error reached output: %#v", result)
				}
			})
		}
	}
}

func TestMalformedAPIErrorBodiesAreRejectedAndRedacted(t *testing.T) {
	invalidUTF8 := append([]byte(`{"error":"revision_conflict","detail":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(fullToken+`"}`)...)
	duplicate := []byte(`{"error":"revision_conflict","error":"transcript_missing","detail":"` + fullToken + `"}`)
	input := writeTestFile(t, "malformed-error.json", `{"type":"x","working_title":"Malformed error","status":"draft","content":{"body":"body"}}`)
	tests := []struct {
		name       string
		body       []byte
		arguments  []string
		jsonOutput bool
		mutation   bool
	}{
		{"read duplicate human", duplicate, []string{"content", "list"}, false, false},
		{"read invalid UTF-8 JSON", invalidUTF8, []string{"content", "list", "--json"}, true, false},
		{"mutation missing error human", []byte(`{}`), []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000088"}, false, true},
		{"mutation null error JSON", []byte(`{"error":null}`), []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000088", "--json"}, true, true},
		{"mutation null current human", []byte(`{"error":"invalid_request","current":null}`), []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000088"}, false, true},
		{"mutation malformed current JSON", []byte(`{"error":"invalid_request","current":{}}`), []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000088", "--json"}, true, true},
		{"mutation invalid UTF-8 human", invalidUTF8, []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000088"}, false, true},
		{"mutation duplicate JSON", duplicate, []string{"content", "create", "--file", input, "--operation-id", "01J00000000000000000000088", "--json"}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(test.body))}, nil
			})}, "", test.arguments...)
			if result.exitCode != ExitUnavailable || result.stdout != "" || strings.Contains(result.stderr, fullToken) || strings.Contains(result.stderr, "transcript_missing") || strings.Contains(result.stderr, "revision_conflict") {
				t.Fatalf("malformed API error was trusted or leaked: %#v", result)
			}
			if test.mutation {
				assertFileMutationRecovery(t, result.stderr, test.jsonOutput, "invalid_api_response", "01J00000000000000000000088")
				return
			}
			want := "error: invalid_api_response\n"
			if test.jsonOutput {
				want = "{\"error\":\"invalid_api_response\"}\n"
			}
			if result.stderr != want {
				t.Fatalf("unexpected malformed API error output: %q", result.stderr)
			}
		})
	}
}

func TestHumanRecoveryQuotesControlCharactersInPaths(t *testing.T) {
	operationID := "01J00000000000000000000089"
	replayFile := "/tmp/request\noperation_id: forged\u2028split\u2029line"
	metadataFile := "/tmp/metadata\x1b[31m\u202Ehidden.json"
	expectation := mutationExpectation{operationID: operationID, replayFile: replayFile, replayMetadata: metadataFile, replayBefore: 123}
	var indeterminate bytes.Buffer
	writeMutationError(&indeterminate, false, "request_failed", expectation, true)
	wantIndeterminate := "error: request_failed\noperation_id: " + operationID + "\nreplay_file: " + strconv.Quote(replayFile) + "\nreplay_metadata: " + strconv.Quote(metadataFile) + "\nreplay_before: 123\n"
	if indeterminate.String() != wantIndeterminate {
		t.Fatalf("unsafe indeterminate recovery paths: %q", indeterminate.String())
	}
	var terminal bytes.Buffer
	runner{stderr: &terminal}.apiErrorWithOperation(apiResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":"invalid_request"}`)}, false, mutationExpectation{operationID: operationID, replayFile: replayFile})
	wantTerminal := "error: invalid_request\noperation_id: " + operationID + "\nrequest_file: " + strconv.Quote(replayFile) + "\n"
	if terminal.String() != wantTerminal {
		t.Fatalf("unsafe terminal recovery path: %q", terminal.String())
	}
}

func TestRequestFreezingDoesNotInflateValidHTMLCharacters(t *testing.T) {
	fixture := newAPIFixture(t)
	body := strings.Repeat("&", 200*1024)
	input := writeTestFile(t, "ampersands.json", fmt.Sprintf(`{"type":"x","working_title":"Ampersands","status":"draft","content":{"body":%q}}`, body))
	result := invoke(t, fixture.server.URL, fullToken, fixture.server.Client(), "", "content", "create", "--file", input, "--json")
	if result.exitCode != ExitSuccess || len(parseMutation(t, result.stdout).ItemIDs) != 1 {
		t.Fatalf("valid request was inflated or rejected: exit=%d stderr=%q", result.exitCode, result.stderr)
	}
}

func TestTranscriptInputDoesNotInflateValidHTMLCharacters(t *testing.T) {
	fixture := newAPIFixture(t)
	input := writeTestFile(t, "youtube-ampersands.json", `{"type":"youtube","working_title":"Ampersand transcript","status":"draft","content":{}}`)
	transcript := strings.Repeat("&", 200*1024)
	result := invoke(t, fixture.server.URL, fullToken, fixture.server.Client(), transcript, "content", "create", "--file", input, "--transcript-file", "-", "--json")
	if result.exitCode != ExitSuccess {
		t.Fatalf("valid transcript was inflated or rejected: exit=%d stderr=%q", result.exitCode, result.stderr)
	}
	id := parseMutation(t, result.stdout).ItemIDs[0]
	roundTrip := invoke(t, fixture.server.URL, fullToken, fixture.server.Client(), "", "content", "transcript", id)
	if roundTrip.exitCode != ExitSuccess || roundTrip.stdout != transcript {
		t.Fatalf("transcript round trip failed: exit=%d bytes=%d stderr=%q", roundTrip.exitCode, len(roundTrip.stdout), roundTrip.stderr)
	}
}

func TestTranscriptInputPreservesJSONLineSeparatorsWithinRequestLimit(t *testing.T) {
	transcript := strings.Repeat("\u2028\u2029", (500*1024)/6)
	transcript += strings.Repeat("x", 500*1024-len(transcript))
	input := writeTestFile(t, "youtube-line-separators.json", fmt.Sprintf(`{"type":"youtube","working_title":%q,"status":"draft","content":{}}`, strings.Repeat("t", 25*1024)))
	var frozen []byte
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		frozen, _ = io.ReadAll(request.Body)
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"operation_id":"01J00000000000000000000074","item_ids":["01J00000000000000000000001"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}`)),
		}, nil
	})
	result := invoke(t, "https://contentflow.example", fullToken, &http.Client{Transport: transport}, transcript, "content", "create", "--file", input, "--transcript-file", "-", "--operation-id", "01J00000000000000000000074", "--json")
	if result.exitCode != ExitSuccess || len(frozen) >= maxInputBytes || !bytes.Contains(frozen, []byte("\u2028")) || !bytes.Contains(frozen, []byte("\u2029")) || bytes.Contains(frozen, []byte(`\u2028`)) || bytes.Contains(frozen, []byte(`\u2029`)) {
		t.Fatalf("line separators were inflated: exit=%d frozen=%d stderr=%q", result.exitCode, len(frozen), result.stderr)
	}
	var request struct {
		Content struct {
			Transcript string `json:"transcript"`
		} `json:"content"`
	}
	if err := json.Unmarshal(frozen, &request); err != nil || request.Content.Transcript != transcript {
		t.Fatalf("line separator round trip failed: err=%v bytes=%d", err, len(request.Content.Transcript))
	}
	literal, err := marshalJSON(`literal \u2028 and \u2029`)
	if err != nil || string(literal) != `"literal \\u2028 and \\u2029"` {
		t.Fatalf("literal escape text changed: %q, %v", literal, err)
	}
}

func TestTimedOutRealAPIBatchRetryDoesNotDuplicate(t *testing.T) {
	fixture := newAPIFixture(t)
	var mu sync.Mutex
	var bodies [][]byte
	requestCount := 0
	wrapper := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		request.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		requestCount++
		count := requestCount
		bodies = append(bodies, slices.Clone(body))
		mu.Unlock()
		if count == 1 {
			recorder := httptest.NewRecorder()
			fixture.handler.ServeHTTP(recorder, request)
			time.Sleep(60 * time.Millisecond)
			return
		}
		fixture.handler.ServeHTTP(response, request)
	}))
	defer wrapper.Close()
	batch := writeTestFile(t, "batch.json", `{"items":[{"type":"x","working_title":"No duplicate one","status":"draft","content":{"body":"one"}},{"type":"x","working_title":"No duplicate two","status":"draft","content":{"body":"two"}}]}`)
	httpClient := wrapper.Client()
	httpClient.Timeout = 25 * time.Millisecond
	result := invoke(t, wrapper.URL, fullToken, httpClient, "", "content", "batch-create", "--file", batch, "--operation-id", "01J00000000000000000000066", "--json")
	if result.exitCode != ExitSuccess {
		t.Fatalf("retry failed: %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry bodies differ: %q", bodies)
	}
	list := invoke(t, fixture.server.URL, fullToken, fixture.server.Client(), "", "content", "list", "--search", "no duplicate", "--json")
	var listed listResponse
	_ = json.Unmarshal([]byte(list.stdout), &listed)
	if len(listed.Items) != 2 {
		t.Fatalf("batch retry created %d items, want 2: %s", len(listed.Items), list.stdout)
	}
}

func TestTranscriptUsesOnlyTranscriptEndpoint(t *testing.T) {
	fixture := newAPIFixture(t)
	create := writeTestFile(t, "youtube.json", `{"type":"youtube","working_title":"Endpoint","status":"draft","content":{"transcript":"only this","sections":[{"position":0,"title":"Secret script","body":"never fallback"}]}}`)
	created := invoke(t, fixture.server.URL, fullToken, fixture.server.Client(), "", "content", "create", "--file", create, "--json")
	id := parseMutation(t, created.stdout).ItemIDs[0]
	var mu sync.Mutex
	var paths []string
	wrapper := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		fixture.handler.ServeHTTP(response, request)
	}))
	defer wrapper.Close()
	result := invoke(t, wrapper.URL, fullToken, wrapper.Client(), "", "content", "transcript", id, "--json")
	mu.Lock()
	gotPaths := slices.Clone(paths)
	mu.Unlock()
	if result.exitCode != ExitSuccess || !slices.Equal(gotPaths, []string{"/api/v1/content/" + id + "/transcript"}) || strings.Contains(result.stdout, "Secret script") {
		t.Fatalf("transcript endpoint isolation failed: result=%#v paths=%v", result, gotPaths)
	}
}

func TestReferenceAgentWorkflowReadsTranscriptThenBatchCreatesStandaloneDrafts(t *testing.T) {
	fixture := newAPIFixture(t)
	create := writeTestFile(t, "source.json", `{"type":"youtube","working_title":"Reference source","status":"draft","content":{"transcript":"canonical input","sections":[{"position":0,"title":"Plan","body":"not canonical"}]}}`)
	created := invoke(t, fixture.server.URL, fullToken, fixture.server.Client(), "", "content", "create", "--file", create, "--json")
	id := parseMutation(t, created.stdout).ItemIDs[0]

	var mu sync.Mutex
	var workflowPaths []string
	wrapper := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		workflowPaths = append(workflowPaths, request.URL.Path)
		mu.Unlock()
		fixture.handler.ServeHTTP(response, request)
	}))
	defer wrapper.Close()
	transcript := invoke(t, wrapper.URL, fullToken, wrapper.Client(), "", "content", "transcript", id, "--json")
	if transcript.exitCode != ExitSuccess {
		t.Fatalf("reference transcript failed: %#v", transcript)
	}
	var source transcriptResponse
	_ = json.Unmarshal([]byte(transcript.stdout), &source)
	items := make([]map[string]any, 20)
	for index := range items {
		items[index] = map[string]any{
			"type": "x", "working_title": fmt.Sprintf("Standalone draft %d", index+1), "status": "draft",
			"content": map[string]string{"body": fmt.Sprintf("%s %d", source.Transcript, index+1)},
		}
	}
	batchJSON, _ := json.Marshal(map[string]any{"items": items})
	batch := writeTestFile(t, "drafts.json", string(batchJSON))
	createdBatch := invoke(t, wrapper.URL, fullToken, wrapper.Client(), "", "content", "batch-create", "--file", batch, "--json")
	if createdBatch.exitCode != ExitSuccess || len(parseMutation(t, createdBatch.stdout).ItemIDs) != 20 {
		t.Fatalf("reference batch failed: %#v", createdBatch)
	}
	wantPaths := []string{"/api/v1/content/" + id + "/transcript", "/api/v1/content/batches"}
	mu.Lock()
	gotPaths := slices.Clone(workflowPaths)
	mu.Unlock()
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("reference workflow called %v, want %v", gotPaths, wantPaths)
	}
}

func TestReferenceAgentScriptDefaultsToRepositoryFlowBinary(t *testing.T) {
	repository := t.TempDir()
	examplesDirectory := filepath.Join(repository, "examples")
	binDirectory := filepath.Join(repository, "bin")
	if err := os.MkdirAll(examplesDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptSource := filepath.Join("..", "..", "..", "..", "examples", "reference-agent.sh")
	script, err := os.ReadFile(scriptSource)
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(examplesDirectory, "reference-agent.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	flowPath := filepath.Join(binDirectory, "flow")
	flowStub := `#!/usr/bin/env bash
set -euo pipefail
[[ "$CONTENTFLOW_API_URL" == "https://contentflow.example" ]]
[[ "$CONTENTFLOW_API_TOKEN" == "cf_reference_secret" ]]
if [[ -r "/proc/$$/cmdline" ]]; then
  ! tr '\0' '\n' <"/proc/$$/cmdline" | grep -q 'cf_reference_secret'
else
  ! ps -o command= -p "$$" | grep -q 'cf_reference_secret'
fi
printf '%s\n' "$*" >>"$FLOW_LOG"
if [[ "$1 $2" == "content transcript" ]]; then
  printf 'canonical transcript'
elif [[ "$1 $2" == "content batch-create" ]]; then
  [[ "$3" == "--file" && "$4" == "/dev/fd/9" && "$5" == "--operation-id" && "$7" == "--replay-before" && "$8" =~ ^[0-9]+$ && "$9" == "--json" ]]
  if [[ -n "${FLOW_MUTATE_BATCH_PATH:-}" ]]; then
    printf '{"items":[{"type":"x","working_title":"Raced","status":"draft","content":{"body":"altered"}}]}' >"$FLOW_MUTATE_BATCH_PATH"
  fi
  if [[ -n "${FLOW_CAPTURE_BATCH:-}" ]]; then
    cat -- "$4" >"$FLOW_CAPTURE_BATCH"
  fi
  if [[ -n "${FLOW_PAUSE_BATCH_FILE:-}" ]]; then
    : >"$FLOW_PAUSE_BATCH_FILE"
    agent_pid=$PPID
    while kill -0 "$agent_pid" 2>/dev/null; do sleep 0.05; done
    exit 9
  fi
  if [[ "${FLOW_FAIL_BATCH:-0}" == "1" ]]; then
    printf '{"error":"request_failed","operation_id":"%s","replay_before":%s}\n' "$6" "$8" >&2
    exit 9
  fi
  if [[ "${FLOW_FAIL_USAGE:-0}" == "1" ]]; then
    printf '{"error":"usage_error"}\n' >&2
    exit 2
  fi
  if [[ -n "${FLOW_FAIL_EXIT:-}" ]]; then
    printf '{"error":"terminal_failure"}\n' >&2
    exit "$FLOW_FAIL_EXIT"
  fi
  if [[ "${FLOW_SIGNAL_BATCH:-0}" == "1" ]]; then
    kill -TERM "$PPID"
    sleep 0.1
  fi
  if [[ "${FLOW_HUP_BATCH:-0}" == "1" ]]; then
    kill -HUP "$PPID"
    sleep 0.1
  fi
  if [[ "${FLOW_KILL_BATCH_CHILD:-0}" == "1" ]]; then
    kill -KILL "$$"
  fi
  printf '{"operation_id":"01J00000000000000000000040","item_ids":["01J00000000000000000000041"],"revisions":[1],"expires_at":["2026-10-10T09:00:00Z"],"status":"created"}\n'
else
  exit 99
fi
`
	if err := os.WriteFile(flowPath, []byte(flowStub), 0o755); err != nil {
		t.Fatal(err)
	}
	builderPath := filepath.Join(repository, "builder.sh")
	builder := `#!/usr/bin/env bash
set -euo pipefail
[[ -z "${CONTENTFLOW_API_URL+x}" && -z "${CONTENTFLOW_API_TOKEN+x}" ]]
[[ "$(<"$1")" == "canonical transcript" ]]
if [[ "${BUILDER_FAIL:-0}" == "1" ]]; then
  printf '{"items":['
  exit 7
fi
printf '{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}'
`
	if err := os.WriteFile(builderPath, []byte(builder), 0o755); err != nil {
		t.Fatal(err)
	}
	unsafeBuilderPath := filepath.Join(repository, "unsafe-builder.sh")
	unsafeBuilder := `#!/usr/bin/env bash
set -euo pipefail
[[ "$(<"$1")" == "canonical transcript" ]]
for ((i = 0; i < 2048; i++)); do
  printf '%1024s' unsafe >&2
done
printf '\033[31mforged recovery\nreplay with: forged\n%s\n' "$(<"$1")" >&2
printf '{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}'
`
	if err := os.WriteFile(unsafeBuilderPath, []byte(unsafeBuilder), 0o755); err != nil {
		t.Fatal(err)
	}
	unsafeFailingBuilderPath := filepath.Join(repository, "unsafe-failing-builder.sh")
	failingBuilder := `#!/usr/bin/env bash
set -euo pipefail
printf '\033[31mforged recovery\n%s\n' "$(<"$1")" >&2
exit 7
`
	if err := os.WriteFile(unsafeFailingBuilderPath, []byte(failingBuilder), 0o755); err != nil {
		t.Fatal(err)
	}
	runnerPath := filepath.Join(repository, "runner.sh")
	runner := `#!/usr/bin/env bash
set -euo pipefail
[[ -z "${CONTENTFLOW_API_URL+x}" && -z "${CONTENTFLOW_API_TOKEN+x}" ]]
if [[ -r "/proc/$PPID/environ" ]]; then
  ! tr '\0' '\n' <"/proc/$PPID/environ" | grep -Eq '(^CONTENTFLOW_API_(URL|TOKEN)=|cf_reference_secret)'
else
  ! ps eww -p "$PPID" | grep -Eq 'CONTENTFLOW_API_(URL|TOKEN)=|cf_reference_secret'
fi
exec env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin "$1" "$2"
`
	if err := os.WriteFile(runnerPath, []byte(runner), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(repository, "flow.log")
	helperDirectory := t.TempDir()
	helperLog := filepath.Join(t.TempDir(), "helpers.log")
	for _, helperName := range []string{"dirname", "jq", "date", "sha256sum", "shasum", "cp"} {
		realHelper, err := exec.LookPath(helperName)
		if err != nil {
			continue
		}
		wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${CONTENTFLOW_API_URL+x}" || -n "${CONTENTFLOW_API_TOKEN+x}" || -n "${contentflow_api_url+x}" || -n "${contentflow_api_token+x}" ]]; then
  printf 'credential leak: %%s\n' "${0##*/}" >>"$HELPER_LOG"
  exit 98
fi
if [[ -r "/proc/$PPID/environ" ]]; then
  if tr '\0' '\n' <"/proc/$PPID/environ" | grep -Eq '(^CONTENTFLOW_API_(URL|TOKEN)=|^contentflow_api_(url|token)=|cf_reference_secret)'; then
    printf 'credential leak: %%s parent\n' "${0##*/}" >>"$HELPER_LOG"
    exit 98
  fi
elif ps eww -p "$PPID" | grep -Eq 'CONTENTFLOW_API_(URL|TOKEN)=|contentflow_api_(url|token)=|cf_reference_secret'; then
  printf 'credential leak: %%s parent\n' "${0##*/}" >>"$HELPER_LOG"
  exit 98
fi
printf '%%s\n' "${0##*/}" >>"$HELPER_LOG"
if [[ "${FLOW_FAIL_FREEZE_COPY:-0}" == "1" && "${0##*/}" == "cp" ]]; then
  exit 97
fi
if [[ "${FLOW_FAIL_RECOVERY_COPY:-0}" == "1" && "${0##*/}" == "cp" && " $* " == *" /dev/fd/10 "* ]]; then
  exit 97
fi
exec %q "$@"
`, realHelper)
		if err := os.WriteFile(filepath.Join(helperDirectory, helperName), []byte(wrapper), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000040")
	command.Dir = t.TempDir()
	recoveryRoot := t.TempDir()
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "FLOW_BIN=") && !strings.HasPrefix(value, "PATH=") && !strings.HasPrefix(value, "CONTENTFLOW_AGENT_RECOVERY_DIR=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "PATH="+helperDirectory+":"+os.Getenv("PATH"), "HELPER_LOG="+helperLog, "FLOW_LOG="+logPath, "CONTENTFLOW_AGENT_RUNNER="+runnerPath, "CONTENTFLOW_AGENT_RECOVERY_DIR="+recoveryRoot, "CONTENTFLOW_API_URL=https://contentflow.example", "CONTENTFLOW_API_TOKEN=cf_reference_secret", "contentflow_api_url=preexisting", "contentflow_api_token=preexisting")
	missingRecovery := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000039")
	missingRecovery.Dir = t.TempDir()
	missingRecovery.Env = slices.DeleteFunc(slices.Clone(command.Env), func(value string) bool { return strings.HasPrefix(value, "CONTENTFLOW_AGENT_RECOVERY_DIR=") })
	missingRecoveryOutput, missingRecoveryErr := missingRecovery.CombinedOutput()
	_, missingRecoveryLogErr := os.Stat(logPath)
	if missingRecoveryErr == nil || string(missingRecoveryOutput) != "CONTENTFLOW_AGENT_RECOVERY_DIR must be an existing absolute durable directory\n" || !errors.Is(missingRecoveryLogErr, os.ErrNotExist) {
		t.Fatalf("reference flow started without durable recovery storage: err=%v output=%q", missingRecoveryErr, missingRecoveryOutput)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("reference script failed: %v: %s", err, output)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	wantLog := "content transcript 01J00000000000000000000042\ncontent batch-create --file "
	if !strings.HasPrefix(string(log), wantLog) || !strings.Contains(string(log), " --operation-id 01J00000000000000000000040 --replay-before ") || !strings.Contains(string(log), " --json\n") {
		t.Fatalf("repository flow binary was not used: %q", log)
	}
	helperCalls, err := os.ReadFile(helperLog)
	if err != nil || !strings.Contains(string(helperCalls), "dirname\n") || !strings.Contains(string(helperCalls), "jq\n") || strings.Contains(string(helperCalls), "credential leak") {
		t.Fatalf("non-flow helper credential isolation failed: err=%v calls=%q", err, helperCalls)
	}
	unsafeBuilderCommand := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", unsafeBuilderPath, "01J00000000000000000000094")
	unsafeBuilderCommand.Dir = t.TempDir()
	unsafeBuilderCommand.Env = slices.Clone(command.Env)
	unsafeBuilderOutput, unsafeBuilderErr := unsafeBuilderCommand.CombinedOutput()
	if unsafeBuilderErr != nil || len(unsafeBuilderOutput) > 4096 || bytes.Contains(unsafeBuilderOutput, []byte{0x1b}) || strings.Contains(string(unsafeBuilderOutput), "forged recovery") || strings.Contains(string(unsafeBuilderOutput), "replay with: forged") || strings.Contains(string(unsafeBuilderOutput), "canonical transcript") {
		t.Fatalf("untrusted builder stderr escaped its boundary: err=%v bytes=%d output=%q", unsafeBuilderErr, len(unsafeBuilderOutput), unsafeBuilderOutput)
	}
	failingBuilderCommand := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", unsafeFailingBuilderPath, "01J00000000000000000000093")
	failingBuilderCommand.Dir = t.TempDir()
	failingBuilderCommand.Env = slices.Clone(command.Env)
	failingBuilderOutput, failingBuilderErr := failingBuilderCommand.CombinedOutput()
	var unsafeBuilderFailureExit *exec.ExitError
	if !errors.As(failingBuilderErr, &unsafeBuilderFailureExit) || unsafeBuilderFailureExit.ExitCode() != 7 || string(failingBuilderOutput) != "draft builder failed\n" {
		t.Fatalf("builder failure diagnostic was unsafe: err=%v output=%q", failingBuilderErr, failingBuilderOutput)
	}

	freezeFailureDirectory := t.TempDir()
	freezeFailure := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000097")
	freezeFailure.Dir = t.TempDir()
	freezeFailureEnvironment := slices.DeleteFunc(slices.Clone(command.Env), func(value string) bool { return strings.HasPrefix(value, "TMPDIR=") || strings.HasPrefix(value, "CONTENTFLOW_AGENT_RECOVERY_DIR=") })
	freezeFailure.Env = append(freezeFailureEnvironment, "TMPDIR="+freezeFailureDirectory, "CONTENTFLOW_AGENT_RECOVERY_DIR="+freezeFailureDirectory, "FLOW_FAIL_FREEZE_COPY=1")
	freezeFailureOutput, freezeFailureErr := freezeFailure.CombinedOutput()
	remainingFreezeFiles, readFreezeErr := os.ReadDir(freezeFailureDirectory)
	if freezeFailureErr == nil || readFreezeErr != nil || len(remainingFreezeFiles) != 0 || strings.Contains(string(freezeFailureOutput), "cf_reference_secret") {
		t.Fatalf("failed frozen copy leaked a snapshot or secret: err=%v readErr=%v files=%v output=%q", freezeFailureErr, readFreezeErr, remainingFreezeFiles, freezeFailureOutput)
	}
	recoveryCopyFailureDirectory := t.TempDir()
	recoveryCopyFailure := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000098")
	recoveryCopyFailure.Dir = t.TempDir()
	recoveryCopyFailureEnvironment := slices.DeleteFunc(slices.Clone(command.Env), func(value string) bool { return strings.HasPrefix(value, "TMPDIR=") || strings.HasPrefix(value, "CONTENTFLOW_AGENT_RECOVERY_DIR=") })
	recoveryCopyFailure.Env = append(recoveryCopyFailureEnvironment, "TMPDIR="+recoveryCopyFailureDirectory, "CONTENTFLOW_AGENT_RECOVERY_DIR="+recoveryCopyFailureDirectory, "FLOW_FAIL_BATCH=1", "FLOW_FAIL_RECOVERY_COPY=1")
	recoveryCopyFailureOutput, recoveryCopyFailureErr := recoveryCopyFailure.CombinedOutput()
	var leakedRetainedSnapshot bool
	if err := filepath.WalkDir(recoveryCopyFailureDirectory, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		leakedRetainedSnapshot = leakedRetainedSnapshot || strings.HasPrefix(entry.Name(), "contentflow-retained-")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if recoveryCopyFailureErr == nil || leakedRetainedSnapshot || !strings.Contains(string(recoveryCopyFailureOutput), "could not refresh retained batch snapshot") || !strings.Contains(string(recoveryCopyFailureOutput), "batch retained for retry:") || strings.Contains(string(recoveryCopyFailureOutput), "cf_reference_secret") {
		t.Fatalf("failed recovery copy was unsafe: err=%v leaked=%v output=%q", recoveryCopyFailureErr, leakedRetainedSnapshot, recoveryCopyFailureOutput)
	}

	crashTemporaryDirectory := t.TempDir()
	crashRecoveryDirectory := t.TempDir()
	pauseMarker := filepath.Join(crashTemporaryDirectory, "batch-started")
	killedAgent := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000096")
	killedAgent.Dir = t.TempDir()
	killedAgentEnvironment := slices.DeleteFunc(slices.Clone(command.Env), func(value string) bool { return strings.HasPrefix(value, "TMPDIR=") || strings.HasPrefix(value, "CONTENTFLOW_AGENT_RECOVERY_DIR=") })
	killedAgent.Env = append(killedAgentEnvironment, "TMPDIR="+crashTemporaryDirectory, "CONTENTFLOW_AGENT_RECOVERY_DIR="+crashRecoveryDirectory, "FLOW_PAUSE_BATCH_FILE="+pauseMarker)
	var killedAgentOutput bytes.Buffer
	killedAgent.Stdout, killedAgent.Stderr = &killedAgentOutput, &killedAgentOutput
	if err := killedAgent.Start(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 60; attempt++ {
		if _, err := os.Stat(pauseMarker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(pauseMarker); err != nil {
		_ = killedAgent.Process.Kill()
		_ = killedAgent.Wait()
		t.Fatalf("batch submission did not start before SIGKILL proof: %v output=%q", err, killedAgentOutput.String())
	}
	if err := killedAgent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	killedAgentErr := killedAgent.Wait()
	var killedAgentExit *exec.ExitError
	if !errors.As(killedAgentErr, &killedAgentExit) || killedAgentExit.ExitCode() == 0 {
		t.Fatalf("reference agent was not killed for crash proof: err=%v output=%q", killedAgentErr, killedAgentOutput.String())
	}
	var crashBatch, crashRecovery string
	var crashPaths []string
	if err := filepath.WalkDir(crashRecoveryDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		crashPaths = append(crashPaths, path)
		switch entry.Name() {
		case "batch.json":
			crashBatch = path
		case "recovery.json":
			crashRecovery = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	crashTemporaryEntries, temporaryReadErr := os.ReadDir(crashTemporaryDirectory)
	crashBatchBytes, batchReadErr := os.ReadFile(crashBatch)
	crashRecoveryBytes, recoveryReadErr := os.ReadFile(crashRecovery)
	crashBatchInfo, batchStatErr := os.Stat(crashBatch)
	crashRecoveryInfo, recoveryStatErr := os.Stat(crashRecovery)
	crashDirectoryInfo, directoryStatErr := os.Stat(filepath.Dir(crashBatch))
	var crashRecord struct {
		BatchFile     string `json:"batch_file"`
		OperationID   string `json:"operation_id"`
		RequestSHA256 string `json:"request_sha256"`
	}
	crashDigest := sha256.Sum256(crashBatchBytes)
	if crashBatch == "" || crashRecovery == "" || !strings.HasPrefix(crashBatch, crashRecoveryDirectory+string(os.PathSeparator)) || !strings.HasPrefix(crashRecovery, crashRecoveryDirectory+string(os.PathSeparator)) || temporaryReadErr != nil || len(crashTemporaryEntries) != 1 || crashTemporaryEntries[0].Name() != filepath.Base(pauseMarker) || batchReadErr != nil || recoveryReadErr != nil || batchStatErr != nil || recoveryStatErr != nil || directoryStatErr != nil || crashBatchInfo.Mode().Perm() != 0o600 || crashRecoveryInfo.Mode().Perm() != 0o600 || crashDirectoryInfo.Mode().Perm() != 0o700 || json.Unmarshal(crashRecoveryBytes, &crashRecord) != nil || crashRecord.BatchFile != crashBatch || crashRecord.OperationID != "01J00000000000000000000096" || crashRecord.RequestSHA256 != fmt.Sprintf("%x", crashDigest[:]) || string(crashBatchBytes) != `{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}` {
		t.Fatalf("SIGKILL lost durable exact batch recovery: batch=%q recovery=%q paths=%q tempEntries=%v tempErr=%v record=%#v batchErr=%v recoveryErr=%v batchStat=%v recoveryStat=%v directoryStat=%v output=%q", crashBatch, crashRecovery, crashPaths, crashTemporaryEntries, temporaryReadErr, crashRecord, batchReadErr, recoveryReadErr, batchStatErr, recoveryStatErr, directoryStatErr, killedAgentOutput.String())
	}

	traced := exec.CommandContext(t.Context(), "/bin/bash", "-x", scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000040")
	traced.Dir = t.TempDir()
	traced.Env = slices.Clone(command.Env)
	traceOutput, err := traced.CombinedOutput()
	if err != nil || strings.Contains(string(traceOutput), "cf_reference_secret") {
		t.Fatalf("xtrace exposed a secret or failed: err=%v output=%q", err, traceOutput)
	}

	unsafeTMP := filepath.Join(t.TempDir(), "tmp\noperation_id: forged\x1b[31m")
	if err := os.Mkdir(unsafeTMP, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafePathFailure := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000095")
	unsafePathFailure.Dir = t.TempDir()
	unsafePathEnvironment := slices.DeleteFunc(slices.Clone(command.Env), func(value string) bool { return strings.HasPrefix(value, "CONTENTFLOW_AGENT_RECOVERY_DIR=") })
	unsafePathFailure.Env = append(unsafePathEnvironment, "CONTENTFLOW_AGENT_RECOVERY_DIR="+unsafeTMP, "FLOW_FAIL_BATCH=1")
	unsafePathOutput, unsafePathErr := unsafePathFailure.CombinedOutput()
	if unsafePathErr == nil || bytes.Contains(unsafePathOutput, []byte{0x1b}) || strings.Contains(string(unsafePathOutput), "\noperation_id: forged") || !strings.Contains(string(unsafePathOutput), "batch retained for retry:") || !strings.Contains(string(unsafePathOutput), `\noperation_id`) || strings.Contains(string(unsafePathOutput), "cf_reference_secret") {
		t.Fatalf("recovery path diagnostic was unsafe: err=%v output=%q", unsafePathErr, unsafePathOutput)
	}

	failed := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000043")
	failed.Dir = t.TempDir()
	failed.Env = append(slices.Clone(command.Env), "FLOW_FAIL_BATCH=1")
	failureOutput, err := failed.CombinedOutput()
	if err == nil {
		t.Fatalf("failed batch unexpectedly succeeded: %s", failureOutput)
	}
	marker := "batch retained for retry: "
	remainder := string(failureOutput)
	start := strings.Index(remainder, marker)
	suffix := " (operation_id: 01J00000000000000000000043)"
	if start < 0 {
		t.Fatalf("failed batch did not report recovery path: %s", failureOutput)
	}
	remainder = remainder[start+len(marker):]
	end := strings.Index(remainder, suffix)
	if end < 0 {
		t.Fatalf("failed batch did not report recovery operation ID: %s", failureOutput)
	}
	retainedPath := remainder[:end]
	t.Cleanup(func() { _ = os.Remove(retainedPath) })
	retained, err := os.ReadFile(retainedPath)
	if err != nil {
		t.Fatalf("retained batch is unavailable: %v", err)
	}
	if string(retained) != `{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}` {
		t.Fatalf("retained batch bytes changed: %q", retained)
	}
	recoveryMarker := "recovery retained for retry: "
	recoveryStart := strings.Index(string(failureOutput), recoveryMarker)
	if recoveryStart < 0 {
		t.Fatalf("failed batch did not report recovery record: %s", failureOutput)
	}
	recoveryPath := strings.SplitN(string(failureOutput)[recoveryStart+len(recoveryMarker):], "\n", 2)[0]
	t.Cleanup(func() { _ = os.Remove(recoveryPath) })
	deadlineMarker := "replay before unix time: "
	deadlineStart := strings.Index(string(failureOutput), deadlineMarker)
	if deadlineStart < 0 || !strings.Contains(string(failureOutput), "replay with:") || !strings.Contains(string(failureOutput), " --replay ") {
		t.Fatalf("retained batch did not report a bounded replay command: %s", failureOutput)
	}
	deadlineText := strings.SplitN(string(failureOutput)[deadlineStart+len(deadlineMarker):], "\n", 2)[0]
	deadline, parseErr := strconv.ParseInt(deadlineText, 10, 64)
	remaining := time.Until(time.Unix(deadline, 0))
	if parseErr != nil || remaining < 22*time.Hour || remaining > 23*time.Hour {
		t.Fatalf("unsafe replay deadline %q: remaining=%v err=%v", deadlineText, remaining, parseErr)
	}
	var recovery struct {
		APIOrigin      string `json:"api_origin"`
		BatchFile      string `json:"batch_file"`
		OperationID    string `json:"operation_id"`
		ReplayDeadline int64  `json:"replay_deadline"`
		RequestSHA256  string `json:"request_sha256"`
	}
	recoveryBytes, err := os.ReadFile(recoveryPath)
	recoveryInfo, statErr := os.Stat(recoveryPath)
	retainedDigest := sha256.Sum256(retained)
	if err != nil || statErr != nil || recoveryInfo.Mode().Perm() != 0o600 || json.Unmarshal(recoveryBytes, &recovery) != nil || recovery.APIOrigin != "https://contentflow.example" || recovery.BatchFile != retainedPath || recovery.OperationID != "01J00000000000000000000043" || recovery.ReplayDeadline != deadline || recovery.RequestSHA256 != fmt.Sprintf("%x", retainedDigest[:]) {
		t.Fatalf("unsafe recovery record: recovery=%#v bytes=%q readErr=%v statErr=%v", recovery, recoveryBytes, err, statErr)
	}

	logBeforeTamper, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retainedPath, append(slices.Clone(retained), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedReplay := exec.CommandContext(t.Context(), scriptPath, "--replay", recoveryPath)
	tamperedReplay.Dir = t.TempDir()
	tamperedReplay.Env = slices.Clone(command.Env)
	tamperedOutput, err := tamperedReplay.CombinedOutput()
	var tamperedExit *exec.ExitError
	logAfterTamper, logErr := os.ReadFile(logPath)
	if err == nil || !errors.As(err, &tamperedExit) || tamperedExit.ExitCode() != ExitUsage || string(tamperedOutput) != "retained batch does not match recovery record\n" || logErr != nil || !bytes.Equal(logBeforeTamper, logAfterTamper) || strings.Contains(string(tamperedOutput), "cf_reference_secret") {
		t.Fatalf("tampered replay was not blocked locally: err=%v output=%q logErr=%v", err, tamperedOutput, logErr)
	}
	if err := os.WriteFile(retainedPath, retained, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidReplayBatch := writeTestFile(t, "invalid-replay-batch.json", `{"items":{}}`)
	invalidReplayBytes, err := os.ReadFile(invalidReplayBatch)
	if err != nil {
		t.Fatal(err)
	}
	invalidReplayDigest := sha256.Sum256(invalidReplayBytes)
	invalidReplayRecovery := writeTestFile(t, "invalid-replay-recovery.json", fmt.Sprintf(`{"api_origin":"https://contentflow.example","batch_file":%q,"operation_id":"01J00000000000000000000092","replay_deadline":%d,"request_sha256":"%x"}`, invalidReplayBatch, time.Now().Add(time.Hour).Unix(), invalidReplayDigest[:]))
	logBeforeInvalidReplay, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	invalidReplay := exec.CommandContext(t.Context(), scriptPath, "--replay", invalidReplayRecovery)
	invalidReplay.Dir = t.TempDir()
	invalidReplay.Env = slices.Clone(command.Env)
	invalidReplayOutput, invalidReplayErr := invalidReplay.CombinedOutput()
	var invalidReplayExit *exec.ExitError
	logAfterInvalidReplay, invalidReplayLogErr := os.ReadFile(logPath)
	if !errors.As(invalidReplayErr, &invalidReplayExit) || invalidReplayExit.ExitCode() != ExitUsage || string(invalidReplayOutput) != "retained batch does not match recovery record\n" || invalidReplayLogErr != nil || !bytes.Equal(logBeforeInvalidReplay, logAfterInvalidReplay) {
		t.Fatalf("structurally invalid replay reached flow: err=%v output=%q logErr=%v", invalidReplayErr, invalidReplayOutput, invalidReplayLogErr)
	}
	logBeforeWrongOrigin, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongOriginReplay := exec.CommandContext(t.Context(), scriptPath, "--replay", recoveryPath)
	wrongOriginReplay.Dir = t.TempDir()
	for _, value := range command.Env {
		if !strings.HasPrefix(value, "CONTENTFLOW_API_URL=") {
			wrongOriginReplay.Env = append(wrongOriginReplay.Env, value)
		}
	}
	wrongOriginReplay.Env = append(wrongOriginReplay.Env, "CONTENTFLOW_API_URL=https://other-contentflow.example")
	wrongOriginOutput, err := wrongOriginReplay.CombinedOutput()
	var wrongOriginExit *exec.ExitError
	logAfterWrongOrigin, logErr := os.ReadFile(logPath)
	if err == nil || !errors.As(err, &wrongOriginExit) || wrongOriginExit.ExitCode() != ExitUsage || string(wrongOriginOutput) != "retained recovery API origin does not match CONTENTFLOW_API_URL\n" || logErr != nil || !bytes.Equal(logBeforeWrongOrigin, logAfterWrongOrigin) || strings.Contains(string(wrongOriginOutput), "cf_reference_secret") {
		t.Fatalf("cross-origin replay was not blocked locally: err=%v output=%q logErr=%v", err, wrongOriginOutput, logErr)
	}
	failedReplay := exec.CommandContext(t.Context(), scriptPath, "--replay", recoveryPath)
	failedReplay.Dir = t.TempDir()
	failedReplay.Env = append(slices.Clone(command.Env), "FLOW_FAIL_BATCH=1")
	failedReplayOutput, err := failedReplay.CombinedOutput()
	var failedReplayExit *exec.ExitError
	var failedReplayProblem struct {
		Error          string `json:"error"`
		OperationID    string `json:"operation_id"`
		ReplayMetadata string `json:"replay_metadata"`
		ReplayBefore   int64  `json:"replay_before"`
	}
	decodeReplayErr := json.Unmarshal(bytes.TrimSpace(failedReplayOutput), &failedReplayProblem)
	if err == nil || !errors.As(err, &failedReplayExit) || failedReplayExit.ExitCode() != ExitUnavailable || decodeReplayErr != nil || failedReplayProblem.OperationID != recovery.OperationID || failedReplayProblem.ReplayBefore != recovery.ReplayDeadline || strings.Contains(string(failedReplayOutput), "cf_reference_secret") {
		t.Fatalf("indeterminate replay extended recovery: err=%v problem=%#v decodeErr=%v output=%q", err, failedReplayProblem, decodeReplayErr, failedReplayOutput)
	}

	replay := exec.CommandContext(t.Context(), scriptPath, "--replay", recoveryPath)
	replay.Dir = t.TempDir()
	capturedReplay := filepath.Join(t.TempDir(), "captured-replay.json")
	replay.Env = append(slices.Clone(command.Env), "FLOW_MUTATE_BATCH_PATH="+retainedPath, "FLOW_CAPTURE_BATCH="+capturedReplay)
	replayOutput, err := replay.CombinedOutput()
	capturedBytes, captureErr := os.ReadFile(capturedReplay)
	mutatedBytes, mutateErr := os.ReadFile(retainedPath)
	if err != nil || strings.Contains(string(replayOutput), "cf_reference_secret") || captureErr != nil || !bytes.Equal(capturedBytes, retained) || mutateErr != nil || bytes.Equal(mutatedBytes, retained) {
		t.Fatalf("bounded replay did not submit its frozen digest bytes: err=%v output=%s captureErr=%v mutateErr=%v captured=%q retained=%q", err, replayOutput, captureErr, mutateErr, capturedBytes, mutatedBytes)
	}
	if err := os.WriteFile(retainedPath, retained, 0o600); err != nil {
		t.Fatalf("restore retained batch after race proof: %v", err)
	}
	if err := os.Chtimes(retainedPath, time.Now().Add(48*time.Hour), time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expiredRecovery := writeTestFile(t, "expired-recovery.json", fmt.Sprintf(`{"api_origin":"https://contentflow.example","batch_file":%q,"operation_id":"01J00000000000000000000043","replay_deadline":1,"request_sha256":%q}`, retainedPath, recovery.RequestSHA256))
	logBeforeExpired, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	expiredReplay := exec.CommandContext(t.Context(), scriptPath, "--replay", expiredRecovery)
	expiredReplay.Dir = t.TempDir()
	expiredReplay.Env = slices.Clone(command.Env)
	expiredOutput, err := expiredReplay.CombinedOutput()
	var expiredExit *exec.ExitError
	logAfterExpired, logErr := os.ReadFile(logPath)
	if err == nil || !errors.As(err, &expiredExit) || expiredExit.ExitCode() != ExitUsage || string(expiredOutput) != "replay deadline has passed; reconcile batch state before any new submission\n" || logErr != nil || !bytes.Equal(logBeforeExpired, logAfterExpired) {
		t.Fatalf("expired replay was not blocked locally: err=%v output=%q logErr=%v", err, expiredOutput, logErr)
	}

	failingBuilderPath := filepath.Join(repository, "failing-builder.sh")
	if err := os.WriteFile(failingBuilderPath, []byte("#!/usr/bin/env bash\nset -euo pipefail\nprintf '{\"items\":['\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	failedBuilder := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", failingBuilderPath, "01J00000000000000000000044")
	failedBuilder.Dir = t.TempDir()
	failedBuilder.Env = slices.Clone(command.Env)
	builderFailureOutput, err := failedBuilder.CombinedOutput()
	if err == nil || strings.Contains(string(builderFailureOutput), marker) {
		t.Fatalf("builder failure was mislabeled replayable: err=%v output=%s", err, builderFailureOutput)
	}

	malformedBuilderPath := filepath.Join(repository, "malformed-builder.sh")
	if err := os.WriteFile(malformedBuilderPath, []byte("#!/usr/bin/env bash\nset -euo pipefail\nprintf '{\"items\":['\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	malformedBuilder := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", malformedBuilderPath, "01J00000000000000000000044")
	malformedBuilder.Dir = t.TempDir()
	malformedBuilder.Env = slices.Clone(command.Env)
	malformedOutput, err := malformedBuilder.CombinedOutput()
	var malformedBuilderExit *exec.ExitError
	if !errors.As(err, &malformedBuilderExit) || malformedBuilderExit.ExitCode() != ExitUsage || string(malformedOutput) != "draft builder output is not a valid batch\n" {
		t.Fatalf("malformed builder output was not rejected safely: err=%v output=%q", err, malformedOutput)
	}

	oversizedBuilderPath := filepath.Join(repository, "oversized-builder.sh")
	if err := os.WriteFile(oversizedBuilderPath, []byte("#!/usr/bin/env bash\nset -euo pipefail\nhead -c 1048449 /dev/zero | tr '\\0' x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oversizedBuilder := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", oversizedBuilderPath, "01J00000000000000000000044")
	oversizedBuilder.Dir = t.TempDir()
	oversizedBuilder.Env = slices.Clone(command.Env)
	oversizedOutput, err := oversizedBuilder.CombinedOutput()
	var oversizedExit *exec.ExitError
	if err == nil || !errors.As(err, &oversizedExit) || oversizedExit.ExitCode() != ExitUsage || !strings.Contains(string(oversizedOutput), "draft builder output exceeds the safe batch size") || strings.Contains(string(oversizedOutput), marker) || len(oversizedOutput) > 4096 {
		t.Fatalf("oversized builder output was not bounded locally: err=%v bytes=%d output=%q", err, len(oversizedOutput), oversizedOutput)
	}

	usageFailure := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000044")
	usageFailure.Dir = t.TempDir()
	usageFailure.Env = append(slices.Clone(command.Env), "FLOW_FAIL_USAGE=1")
	usageOutput, err := usageFailure.CombinedOutput()
	if err == nil || strings.Contains(string(usageOutput), marker) {
		t.Fatalf("local batch usage failure was mislabeled replayable: err=%v output=%s", err, usageOutput)
	}

	for _, terminalExit := range []int{ExitConflict, ExitInvalid, ExitRateLimited} {
		terminalFailure := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000044")
		terminalFailure.Dir = t.TempDir()
		terminalFailure.Env = append(slices.Clone(command.Env), fmt.Sprintf("FLOW_FAIL_EXIT=%d", terminalExit))
		terminalOutput, err := terminalFailure.CombinedOutput()
		var exitError *exec.ExitError
		if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != terminalExit || strings.Contains(string(terminalOutput), marker) {
			t.Fatalf("terminal exit %d was mislabeled replayable: err=%v output=%s", terminalExit, err, terminalOutput)
		}
	}

	interruptedBatch := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000045")
	interruptedBatch.Dir = t.TempDir()
	interruptedBatch.Env = append(slices.Clone(command.Env), "FLOW_SIGNAL_BATCH=1")
	interruptedOutput, err := interruptedBatch.CombinedOutput()
	var interruptedExit *exec.ExitError
	if err == nil || !errors.As(err, &interruptedExit) || interruptedExit.ExitCode() != ExitUnavailable || !strings.Contains(string(interruptedOutput), marker) {
		t.Fatalf("interrupted batch was not retained: err=%v output=%s", err, interruptedOutput)
	}
	interruptedRemainder := string(interruptedOutput)
	interruptedStart := strings.Index(interruptedRemainder, marker)
	interruptedRemainder = interruptedRemainder[interruptedStart+len(marker):]
	interruptedEnd := strings.Index(interruptedRemainder, " (operation_id: 01J00000000000000000000045)")
	if interruptedEnd < 0 {
		t.Fatalf("interrupted batch recovery ID missing: %s", interruptedOutput)
	}
	interruptedPath := interruptedRemainder[:interruptedEnd]
	t.Cleanup(func() { _ = os.Remove(interruptedPath) })
	if interrupted, readErr := os.ReadFile(interruptedPath); readErr != nil || string(interrupted) != `{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}` {
		t.Fatalf("interrupted batch bytes unavailable: err=%v body=%q", readErr, interrupted)
	}
	interruptedRecoveryStart := strings.Index(string(interruptedOutput), recoveryMarker)
	if interruptedRecoveryStart < 0 {
		t.Fatalf("interrupted recovery record missing: %s", interruptedOutput)
	}
	interruptedRecoveryPath := strings.SplitN(string(interruptedOutput)[interruptedRecoveryStart+len(recoveryMarker):], "\n", 2)[0]
	t.Cleanup(func() { _ = os.Remove(interruptedRecoveryPath) })

	hangupBatch := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000047")
	hangupBatch.Dir = t.TempDir()
	hangupBatch.Env = append(slices.Clone(command.Env), "FLOW_HUP_BATCH=1")
	hangupOutput, err := hangupBatch.CombinedOutput()
	var hangupExit *exec.ExitError
	if err == nil || !errors.As(err, &hangupExit) || hangupExit.ExitCode() != ExitUnavailable || !strings.Contains(string(hangupOutput), marker) || strings.Contains(string(hangupOutput), "cf_reference_secret") {
		t.Fatalf("hangup batch was not retained safely: err=%v output=%s", err, hangupOutput)
	}
	hangupRemainder := string(hangupOutput)
	hangupStart := strings.Index(hangupRemainder, marker)
	hangupRemainder = hangupRemainder[hangupStart+len(marker):]
	hangupEnd := strings.Index(hangupRemainder, " (operation_id: 01J00000000000000000000047)")
	if hangupEnd < 0 {
		t.Fatalf("hangup batch recovery ID missing: %s", hangupOutput)
	}
	hangupPath := hangupRemainder[:hangupEnd]
	t.Cleanup(func() { _ = os.Remove(hangupPath) })
	if hangupBody, readErr := os.ReadFile(hangupPath); readErr != nil || string(hangupBody) != `{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}` {
		t.Fatalf("hangup batch bytes unavailable: err=%v body=%q", readErr, hangupBody)
	}
	hangupRecoveryStart := strings.Index(string(hangupOutput), recoveryMarker)
	if hangupRecoveryStart < 0 {
		t.Fatalf("hangup recovery record missing: %s", hangupOutput)
	}
	hangupRecoveryPath := strings.SplitN(string(hangupOutput)[hangupRecoveryStart+len(recoveryMarker):], "\n", 2)[0]
	t.Cleanup(func() { _ = os.Remove(hangupRecoveryPath) })
	if recoveryInfo, statErr := os.Stat(hangupRecoveryPath); statErr != nil || recoveryInfo.Mode().Perm() != 0o600 {
		t.Fatalf("hangup recovery record is unavailable or unsafe: info=%v err=%v", recoveryInfo, statErr)
	}

	killedChild := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "01J00000000000000000000046")
	killedChild.Dir = t.TempDir()
	killedChild.Env = append(slices.Clone(command.Env), "FLOW_KILL_BATCH_CHILD=1")
	killedOutput, err := killedChild.CombinedOutput()
	var killedExit *exec.ExitError
	if err == nil || !errors.As(err, &killedExit) || killedExit.ExitCode() != 137 || !strings.Contains(string(killedOutput), marker) {
		t.Fatalf("signal-killed batch child was not retained: err=%v output=%s", err, killedOutput)
	}
	killedRemainder := string(killedOutput)
	killedStart := strings.Index(killedRemainder, marker)
	killedRemainder = killedRemainder[killedStart+len(marker):]
	killedEnd := strings.Index(killedRemainder, " (operation_id: 01J00000000000000000000046)")
	if killedEnd < 0 {
		t.Fatalf("signal-killed batch recovery ID missing: %s", killedOutput)
	}
	killedPath := killedRemainder[:killedEnd]
	t.Cleanup(func() { _ = os.Remove(killedPath) })
	if killed, readErr := os.ReadFile(killedPath); readErr != nil || string(killed) != `{"items":[{"type":"x","working_title":"Standalone","status":"draft","content":{"body":"draft"}}]}` {
		t.Fatalf("signal-killed batch bytes unavailable: err=%v body=%q", readErr, killed)
	}
	killedRecoveryStart := strings.Index(string(killedOutput), recoveryMarker)
	if killedRecoveryStart < 0 {
		t.Fatalf("signal-killed recovery record missing: %s", killedOutput)
	}
	killedRecoveryPath := strings.SplitN(string(killedOutput)[killedRecoveryStart+len(recoveryMarker):], "\n", 2)[0]
	t.Cleanup(func() { _ = os.Remove(killedRecoveryPath) })

	unsafeOperation := exec.CommandContext(t.Context(), scriptPath, "01J00000000000000000000042", builderPath, "bad\n\x1b[31m")
	unsafeOperation.Dir = t.TempDir()
	unsafeOperation.Env = slices.Clone(command.Env)
	unsafeOutput, err := unsafeOperation.CombinedOutput()
	if err == nil || string(unsafeOutput) != "operation ID must be a ULID\n" || strings.ContainsRune(string(unsafeOutput), '\x1b') {
		t.Fatalf("unsafe script operation ID was accepted or echoed: err=%v output=%q", err, unsafeOutput)
	}
}

func TestReferenceAgentSandboxDeniesHomeNetworkAndCloudEnvironment(t *testing.T) {
	sandboxPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "examples", "reference-agent-sandbox.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap is not installed")
		}
	} else if runtime.GOOS != "darwin" {
		t.Skip("reference sandbox supports macOS and Linux")
	}
	secretPath := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(secretPath, []byte("PRIVATE_HOST_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcriptPath := writeTestFile(t, "sandbox-transcript.txt", "canonical transcript")
	requests := atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	builderPath := writeTestFile(t, "sandbox-builder.sh", fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
[[ -z "${CONTENTFLOW_API_URL+x}" && -z "${CONTENTFLOW_API_TOKEN+x}" && -z "${GOOGLE_APPLICATION_CREDENTIALS+x}" ]]
if IFS= read -r inherited_input; then
  exit 96
fi
if /bin/cat %q >/dev/null 2>&1; then
  exit 91
fi
if /usr/bin/curl -fsS --max-time 1 %q >/dev/null 2>&1; then
  exit 92
fi
if /bin/cat /etc/hosts >/dev/null 2>&1; then
  exit 93
fi
if [[ -n "$(/bin/ps -p %d -o command= 2>/dev/null)" ]]; then
  exit 95
fi
[[ "$(<"$1")" == "canonical transcript" ]]
printf '{"items":[{"type":"x","working_title":"Sandboxed","status":"draft","content":{"body":"draft"}}]}'
`, secretPath, server.URL, os.Getpid()))
	if err := os.Chmod(builderPath, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), sandboxPath, builderPath, transcriptPath)
	command.Stdin = strings.NewReader("INHERITED_PRIVATE_INPUT\n")
	command.Env = append(os.Environ(),
		"CONTENTFLOW_API_URL=https://contentflow.example",
		"CONTENTFLOW_API_TOKEN=cf_secret",
		"GOOGLE_APPLICATION_CREDENTIALS="+secretPath,
	)
	output, err := command.CombinedOutput()
	if err != nil || requests.Load() != 0 || strings.Contains(string(output), "PRIVATE_HOST_SECRET") || !json.Valid(output) {
		t.Fatalf("sandbox isolation failed: err=%v requests=%d output=%q", err, requests.Load(), output)
	}
}

func TestReferenceAgentSandboxRejectsUnavailableInputsBeforeResolution(t *testing.T) {
	sandboxPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "examples", "reference-agent-sandbox.sh"))
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := writeTestFile(t, "available-transcript.txt", "canonical transcript")
	builderPath := writeTestFile(t, "available-builder.sh", "#!/usr/bin/env bash\nexit 0\n")
	if err := os.Chmod(builderPath, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		builder    string
		transcript string
		want       string
	}{
		{"builder", filepath.Join(t.TempDir(), "missing", "builder"), transcriptPath, "draft builder must be an executable file\n"},
		{"transcript", builderPath, filepath.Join(t.TempDir(), "missing", "transcript"), "transcript must be a readable file\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), sandboxPath, test.builder, test.transcript)
			output, runErr := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(runErr, &exitError) || exitError.ExitCode() != ExitUsage || string(output) != test.want {
				t.Fatalf("unavailable %s was not rejected safely: err=%v output=%q", test.name, runErr, output)
			}
		})
	}
}

func TestReferenceAgentSandboxDeniesUnrelatedMachLookup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Mach lookup applies only to macOS")
	}
	sandboxPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "examples", "reference-agent-sandbox.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source := writeTestFile(t, "mach-builder.c", `#include <mach/mach.h>
#include <servers/bootstrap.h>
#include <stdio.h>
int main(void) {
  mach_port_t service = MACH_PORT_NULL;
  if (bootstrap_look_up(bootstrap_port, "com.apple.cfprefsd.daemon", &service) == KERN_SUCCESS) {
    return 94;
  }
  fputs("{\"items\":[{\"type\":\"x\",\"working_title\":\"Sandboxed\",\"status\":\"draft\",\"content\":{\"body\":\"draft\"}}]}", stdout);
  return 0;
}`)
	builder := filepath.Join(t.TempDir(), "mach-builder")
	compile := exec.CommandContext(t.Context(), "cc", "-o", builder, source)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile Mach probe: %v: %s", err, output)
	}
	outside := exec.CommandContext(t.Context(), builder)
	var outsideExit *exec.ExitError
	if err := outside.Run(); !errors.As(err, &outsideExit) || outsideExit.ExitCode() != 94 {
		t.Fatalf("Mach probe precondition failed outside sandbox: %v", err)
	}
	transcript := writeTestFile(t, "mach-transcript.txt", "canonical transcript")
	command := exec.CommandContext(t.Context(), sandboxPath, builder, transcript)
	output, err := command.CombinedOutput()
	if err != nil || !json.Valid(output) {
		t.Fatalf("sandbox allowed unrelated Mach lookup or failed: err=%v output=%q", err, output)
	}
}
