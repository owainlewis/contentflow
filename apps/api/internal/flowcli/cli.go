package flowcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

const (
	ExitSuccess     = 0
	ExitUsage       = 2
	ExitAuth        = 3
	ExitForbidden   = 4
	ExitNotFound    = 5
	ExitConflict    = 6
	ExitInvalid     = 7
	ExitRateLimited = 8
	ExitUnavailable = 9

	maxInputBytes = 1 << 20
)

type Options struct {
	APIURL         string
	Token          string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	HTTPClient     *http.Client
	NewOperationID func() (string, error)
	Sleep          func(context.Context, time.Duration) error
}

type runner struct {
	client         *client
	stdin          io.Reader
	stdout         io.Writer
	stderr         io.Writer
	newOperationID func() (string, error)
	jsonOutput     bool
}

type mutationExpectation struct {
	operationID     string
	httpStatus      int
	status          string
	itemCount       int
	itemID          string
	revision        int64
	replayFile      string
	cleanupReplay   string
	replayMetadata  string
	cleanupMetadata string
	replayBefore    int64
}

type replayMetadata struct {
	OperationID          string `json:"operation_id"`
	Origin               string `json:"origin"`
	Method               string `json:"method"`
	Path                 string `json:"path"`
	RequestSHA256        string `json:"request_sha256"`
	ReplayBefore         int64  `json:"replay_before"`
	TemporaryRequest     *bool  `json:"temporary_request"`
	RecoveryRequestPath  string `json:"recovery_request_path,omitempty"`
	RecoveryMetadataPath string `json:"recovery_metadata_path,omitempty"`
	generatedRequest     string
	generatedMetadata    bool
}

func Run(ctx context.Context, arguments []string, options Options) int {
	options = defaultOptions(options)
	jsonOutput := requestsJSON(arguments)
	for _, argument := range arguments {
		if !utf8.ValidString(argument) {
			writeLocalError(options.Stderr, jsonOutput, "usage_error", "arguments must be valid UTF-8")
			return ExitUsage
		}
	}
	if wantsRootHelp(arguments) {
		_, _ = io.WriteString(options.Stdout, usage)
		return ExitSuccess
	}
	if len(arguments) < 2 || arguments[0] != "content" {
		writeLocalError(options.Stderr, jsonOutput, "usage_error", "expected 'content <command>'")
		return ExitUsage
	}
	if arguments[1] == "help" || arguments[1] == "--help" || arguments[1] == "-h" || containsHelp(arguments[2:]) {
		_, _ = io.WriteString(options.Stdout, usage)
		return ExitSuccess
	}
	apiClient, err := newClient(options.APIURL, options.Token, options.HTTPClient, options.Sleep)
	if err != nil {
		writeLocalError(options.Stderr, jsonOutput, "configuration_error", err.Error())
		return ExitUsage
	}
	commandRunner := runner{client: apiClient, stdin: options.Stdin, stdout: cancellableWriter{ctx: ctx, destination: options.Stdout}, stderr: options.Stderr, newOperationID: options.NewOperationID, jsonOutput: jsonOutput}
	return commandRunner.run(ctx, arguments[1], arguments[2:])
}

func requestsJSON(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--json" {
			return true
		}
	}
	return false
}

func defaultOptions(options Options) Options {
	if options.Stdin == nil {
		options.Stdin = strings.NewReader("")
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.NewOperationID == nil {
		options.NewOperationID = newOperationID
	}
	return options
}

func wantsRootHelp(arguments []string) bool {
	return len(arguments) == 0 || (len(arguments) == 1 && (arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h"))
}

func (r runner) run(ctx context.Context, command string, arguments []string) int {
	switch command {
	case "list":
		return r.list(ctx, arguments)
	case "show":
		return r.show(ctx, arguments)
	case "transcript":
		return r.transcript(ctx, arguments)
	case "create":
		return r.writeContent(ctx, http.MethodPost, "/api/v1/content", arguments, false)
	case "update":
		return r.update(ctx, arguments)
	case "batch-create":
		return r.writeContent(ctx, http.MethodPost, "/api/v1/content/batches", arguments, true)
	default:
		writeLocalError(r.stderr, r.jsonOutput, "usage_error", "unknown content command: "+safeHumanPath(command))
		return ExitUsage
	}
}

type flagKind int

const (
	booleanFlag flagKind = iota
	valueFlag
)

type parsedArguments struct {
	values      map[string]string
	booleans    map[string]bool
	positionals []string
}

func parseArguments(arguments []string, allowed map[string]flagKind) (parsedArguments, error) {
	parsed := parsedArguments{values: make(map[string]string), booleans: make(map[string]bool)}
	seen := make(map[string]bool)
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !utf8.ValidString(argument) {
			return parsedArguments{}, fmt.Errorf("arguments must be valid UTF-8")
		}
		if !strings.HasPrefix(argument, "--") {
			parsed.positionals = append(parsed.positionals, argument)
			continue
		}
		nameValue := strings.TrimPrefix(argument, "--")
		name, value, hasValue := strings.Cut(nameValue, "=")
		kind, ok := allowed[name]
		if !ok {
			return parsedArguments{}, fmt.Errorf("unknown flag %s", safeHumanPath("--"+name))
		}
		if seen[name] {
			return parsedArguments{}, fmt.Errorf("flag %s may be supplied only once", safeHumanPath("--"+name))
		}
		seen[name] = true
		if kind == booleanFlag {
			if hasValue {
				return parsedArguments{}, fmt.Errorf("flag %s does not take a value", safeHumanPath("--"+name))
			}
			parsed.booleans[name] = true
			continue
		}
		if !hasValue {
			index++
			if index >= len(arguments) || strings.HasPrefix(arguments[index], "--") {
				return parsedArguments{}, fmt.Errorf("flag %s requires a value", safeHumanPath("--"+name))
			}
			value = arguments[index]
		}
		if value == "" {
			return parsedArguments{}, fmt.Errorf("flag %s requires a value", safeHumanPath("--"+name))
		}
		if !utf8.ValidString(value) {
			return parsedArguments{}, fmt.Errorf("arguments must be valid UTF-8")
		}
		parsed.values[name] = value
	}
	return parsed, nil
}

func containsHelp(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--help" || argument == "-h" {
			return true
		}
	}
	return false
}

func (r runner) list(ctx context.Context, arguments []string) int {
	parsed, err := parseArguments(arguments, map[string]flagKind{"json": booleanFlag, "search": valueFlag, "type": valueFlag, "status": valueFlag})
	if err != nil || len(parsed.positionals) != 0 {
		return r.usageError(firstError(err, "list takes no positional arguments"))
	}
	query := make(url.Values)
	if value := parsed.values["search"]; value != "" {
		query.Set("q", value)
	}
	if value := parsed.values["type"]; value != "" {
		query.Set("type", value)
	}
	if value := parsed.values["status"]; value != "" {
		query.Set("status", value)
	}
	path := "/api/v1/content"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	output, err := os.CreateTemp("", "contentflow-list-*")
	if err != nil {
		writeStableError(r.stderr, parsed.booleans["json"], "request_failed")
		return ExitUnavailable
	}
	outputPath := output.Name()
	defer func() {
		_ = output.Close()
		_ = os.Remove(outputPath)
	}()
	consume := func(source io.Reader) error {
		if parsed.booleans["json"] {
			return writeListJSONStream(output, source)
		}
		return renderListStream(output, source)
	}
	response, requestErr := r.client.getStream(ctx, path, consume)
	if requestErr != nil {
		code := "request_failed"
		if response.Status >= 200 && response.Status < 300 {
			code = "invalid_api_response"
		}
		writeStableError(r.stderr, parsed.booleans["json"], code)
		return ExitUnavailable
	}
	if response.Status != http.StatusOK {
		return r.apiError(response, parsed.booleans["json"])
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		writeStableError(r.stderr, parsed.booleans["json"], "invalid_api_response")
		return ExitUnavailable
	}
	if _, err := io.Copy(r.stdout, output); err != nil {
		writeStableError(r.stderr, parsed.booleans["json"], "invalid_api_response")
		return ExitUnavailable
	}
	return ExitSuccess
}

func (r runner) show(ctx context.Context, arguments []string) int {
	parsed, err := parseArguments(arguments, map[string]flagKind{"json": booleanFlag})
	if err != nil || len(parsed.positionals) != 1 {
		return r.usageError(firstError(err, "show requires one content ID"))
	}
	contentID, err := canonicalContentID(parsed.positionals[0])
	if err != nil {
		return r.usageError(err.Error())
	}
	response, requestErr := r.client.get(ctx, "/api/v1/content/"+url.PathEscape(contentID))
	return r.finish(response, requestErr, parsed.booleans["json"],
		func(destination io.Writer, raw []byte) error { return renderItemForID(destination, raw, contentID) },
		func(destination io.Writer, raw []byte) error { return writeItemJSONForID(destination, raw, contentID) })
}

func (r runner) transcript(ctx context.Context, arguments []string) int {
	parsed, err := parseArguments(arguments, map[string]flagKind{"json": booleanFlag})
	if err != nil || len(parsed.positionals) != 1 {
		return r.usageError(firstError(err, "transcript requires one content ID"))
	}
	contentID, err := canonicalContentID(parsed.positionals[0])
	if err != nil {
		return r.usageError(err.Error())
	}
	response, requestErr := r.client.get(ctx, "/api/v1/content/"+url.PathEscape(contentID)+"/transcript")
	return r.finish(response, requestErr, parsed.booleans["json"],
		func(destination io.Writer, raw []byte) error {
			return renderTranscriptForID(destination, raw, contentID)
		},
		func(destination io.Writer, raw []byte) error {
			return writeTranscriptJSONForID(destination, raw, contentID)
		})
}

func (r runner) update(ctx context.Context, arguments []string) int {
	parsed, err := parseArguments(arguments, contentFileFlags())
	if err != nil || len(parsed.positionals) != 1 {
		return r.usageError(firstError(err, "update requires one content ID"))
	}
	contentID, err := canonicalContentID(parsed.positionals[0])
	if err != nil {
		return r.usageError(err.Error())
	}
	return r.writeParsedContent(ctx, http.MethodPut, "/api/v1/content/"+url.PathEscape(contentID), parsed, false, contentID)
}

func (r runner) writeContent(ctx context.Context, method, path string, arguments []string, batch bool) int {
	flags := contentFileFlags()
	if batch {
		flags = map[string]flagKind{"json": booleanFlag, "file": valueFlag, "operation-id": valueFlag, "replay-metadata": valueFlag, "replay-before": valueFlag}
	}
	parsed, err := parseArguments(arguments, flags)
	if err != nil || len(parsed.positionals) != 0 {
		return r.usageError(firstError(err, "command takes no positional arguments"))
	}
	return r.writeParsedContent(ctx, method, path, parsed, batch, "")
}

func contentFileFlags() map[string]flagKind {
	return map[string]flagKind{
		"json": booleanFlag, "file": valueFlag, "operation-id": valueFlag,
		"transcript-file": valueFlag, "clear-transcript": booleanFlag, "replay-metadata": valueFlag, "replay-before": valueFlag,
	}
}

func (r runner) writeParsedContent(ctx context.Context, method, path string, parsed parsedArguments, batch bool, expectedItemID string) int {
	filePath := parsed.values["file"]
	if filePath == "" {
		return r.usageError("--file is required")
	}
	transcriptFile := parsed.values["transcript-file"]
	clearTranscript := parsed.booleans["clear-transcript"]
	replayMetadataPath := parsed.values["replay-metadata"]
	replayBefore := int64(0)
	if value := parsed.values["replay-before"]; value != "" {
		if parsed.values["operation-id"] == "" || replayMetadataPath != "" {
			return r.usageError("--replay-before requires --operation-id and cannot be combined with --replay-metadata")
		}
		parsedReplayBefore, parseErr := strconv.ParseInt(value, 10, 64)
		replayBefore = parsedReplayBefore
		now := time.Now().UTC().Unix()
		if parseErr != nil || replayBefore <= now || replayBefore > now+int64(23*time.Hour/time.Second)+60 {
			return r.usageError("--replay-before must be a future Unix time within 23 hours")
		}
	}
	if transcriptFile != "" && clearTranscript {
		return r.usageError("--transcript-file and --clear-transcript are mutually exclusive")
	}
	if filePath == "-" && transcriptFile == "-" {
		return r.usageError("request JSON and transcript cannot both read from stdin")
	}
	if replayMetadataPath != "" && (filePath == "-" || transcriptFile != "" || clearTranscript) {
		return r.usageError("--replay-metadata requires a request file without transcript flags")
	}
	body, regularRequest, err := r.readInput(ctx, filePath)
	if err != nil {
		return r.usageError(err.Error())
	}
	if replayMetadataPath != "" && !regularRequest {
		return r.usageError("--replay-metadata requires a regular request file without transcript flags")
	}
	var object map[string]json.RawMessage
	if err := decodeObject(body, &object); err != nil {
		return r.usageError("request file must contain one JSON object")
	}
	operationID, err := r.operationID(object, parsed.values["operation-id"])
	if err != nil {
		return r.usageError(err.Error())
	}
	encodedOperationID, _ := json.Marshal(operationID)
	object["operation_id"] = encodedOperationID
	if !batch && (transcriptFile != "" || clearTranscript) {
		transcript := []byte{}
		if transcriptFile != "" {
			transcript, _, err = r.readInput(ctx, transcriptFile)
			if err != nil {
				return r.usageError(err.Error())
			}
		}
		if err := setTranscript(object, string(transcript)); err != nil {
			return r.usageError(err.Error())
		}
	}
	frozen, err := marshalRequest(object)
	if err != nil {
		return r.usageError("request JSON could not be encoded")
	}
	if len(frozen) > maxInputBytes {
		return r.usageError("request JSON exceeds 1 MiB")
	}
	expectation := mutationExpectation{operationID: operationID, httpStatus: http.StatusCreated, status: "created", itemCount: 1, itemID: expectedItemID, revision: 1}
	if replayMetadataPath != "" {
		metadata, err := validateReplayMetadata(ctx, replayMetadataPath, frozen, operationID, r.client.baseURL, method, path)
		if err != nil {
			return r.usageError(err.Error())
		}
		if *metadata.TemporaryRequest {
			expectation.replayFile = filePath
			expectation.cleanupReplay = metadata.generatedRequest
		}
		expectation.replayMetadata = replayMetadataPath
		if metadata.generatedMetadata {
			expectation.cleanupMetadata = replayMetadataPath
		}
		expectation.replayBefore = metadata.ReplayBefore
	}
	if method == http.MethodPut {
		expectation.httpStatus = http.StatusOK
		expectation.status = "updated"
		var revision int64
		if err := json.Unmarshal(object["revision"], &revision); err != nil || revision < 1 {
			return r.usageError("update request revision must be a positive integer")
		}
		expectation.revision = revision + 1
	}
	if batch {
		var items []json.RawMessage
		if err := json.Unmarshal(object["items"], &items); err != nil || len(items) < 1 || len(items) > 50 {
			return r.usageError("batch request must contain 1 to 50 items")
		}
		expectation.itemCount = len(items)
	}
	temporaryRequest := !regularRequest || transcriptFile != "" || clearTranscript
	if temporaryRequest {
		expectation.replayFile, err = writeReplayFile(frozen)
		if err != nil {
			return r.usageError("could not retain request for recovery")
		}
		expectation.cleanupReplay = expectation.replayFile
	}
	if replayMetadataPath == "" {
		expectation.replayMetadata, expectation.replayBefore, err = writeReplayMetadata(frozen, operationID, r.client.baseURL, method, path, expectation.replayFile, replayBefore)
		if err != nil {
			removeRecoveryFiles(expectation.replayFile, "")
			removeRecoveryBundleDirectory(expectation.replayFile, filepath.Join(filepath.Dir(expectation.replayFile), "metadata.json"))
			return r.usageError("could not retain request recovery metadata")
		}
		expectation.cleanupMetadata = expectation.replayMetadata
	}
	response, requestErr := r.client.mutate(ctx, method, path, frozen)
	return r.finishMutation(response, requestErr, parsed.booleans["json"], expectation)
}

func writeReplayFile(frozen []byte) (string, error) {
	directory, err := os.MkdirTemp("", "contentflow-recovery-*")
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "request.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(directory)
		return "", err
	}
	if _, err := file.Write(frozen); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(directory)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	return path, nil
}

func writeReplayMetadata(frozen []byte, operationID, origin, method, path, temporaryRequestPath string, replayBefore int64) (string, int64, error) {
	digest := sha256.Sum256(frozen)
	if replayBefore == 0 {
		replayBefore = time.Now().UTC().Add(23 * time.Hour).Unix()
	}
	var file *os.File
	var err error
	if temporaryRequestPath != "" {
		directory, valid := ownedRecoveryBundleDirectory(temporaryRequestPath, filepath.Join(filepath.Dir(temporaryRequestPath), "metadata.json"))
		if !valid {
			return "", 0, fmt.Errorf("invalid recovery bundle")
		}
		file, err = os.OpenFile(filepath.Join(directory, "metadata.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	} else {
		file, err = os.CreateTemp("", "contentflow-replay-metadata-*.json")
	}
	if err != nil {
		return "", 0, err
	}
	metadataPath := file.Name()
	temporary := temporaryRequestPath != ""
	encoded, err := marshalJSON(replayMetadata{
		OperationID: operationID, Origin: origin, Method: method, Path: path, TemporaryRequest: &temporary,
		RequestSHA256: fmt.Sprintf("%x", digest[:]), ReplayBefore: replayBefore,
		RecoveryRequestPath: temporaryRequestPath, RecoveryMetadataPath: metadataPath,
	})
	if err != nil {
		_ = file.Close()
		_ = os.Remove(metadataPath)
		return "", 0, err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(metadataPath)
		return "", 0, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(metadataPath)
		return "", 0, err
	}
	return metadataPath, replayBefore, nil
}

func validateReplayMetadata(ctx context.Context, metadataPath string, frozen []byte, operationID, origin, method, path string) (replayMetadata, error) {
	file, err := openFileWithContext(ctx, metadataPath)
	if err != nil {
		if ctx.Err() != nil {
			return replayMetadata{}, fmt.Errorf("replay metadata read was interrupted")
		}
		return replayMetadata{}, fmt.Errorf("replay metadata is unavailable")
	}
	defer func() { _ = file.Close() }()
	contents, err := readBoundedWithContext(ctx, file, file, maxInputBytes+1)
	if ctx.Err() != nil {
		return replayMetadata{}, fmt.Errorf("replay metadata read was interrupted")
	}
	if err != nil || len(contents) > maxInputBytes {
		return replayMetadata{}, fmt.Errorf("replay metadata is unavailable")
	}
	var metadata replayMetadata
	if err := decodeStrictJSON(contents, &metadata); err != nil || metadata.OperationID == "" || metadata.Origin == "" || metadata.Method == "" || metadata.Path == "" || metadata.RequestSHA256 == "" || metadata.ReplayBefore < 1 || metadata.TemporaryRequest == nil {
		return replayMetadata{}, fmt.Errorf("replay metadata is invalid")
	}
	if *metadata.TemporaryRequest {
		requestPath, owned, valid := validateRecoveryBundle(metadataPath)
		if !valid {
			return replayMetadata{}, fmt.Errorf("replay metadata is invalid")
		}
		if owned && metadata.RecoveryRequestPath == requestPath && metadata.RecoveryMetadataPath == metadataPath {
			metadata.generatedRequest = requestPath
			metadata.generatedMetadata = true
		}
	} else if metadata.RecoveryRequestPath == "" && metadata.RecoveryMetadataPath == metadataPath && ownedStandaloneMetadata(metadataPath) {
		metadata.generatedMetadata = true
	}
	if time.Now().UTC().Unix() >= metadata.ReplayBefore {
		return replayMetadata{}, fmt.Errorf("replay deadline has passed; reconcile mutation state before any new submission")
	}
	digest := sha256.Sum256(frozen)
	if metadata.OperationID != operationID || metadata.Origin != origin || metadata.Method != method || metadata.Path != path || metadata.RequestSHA256 != fmt.Sprintf("%x", digest[:]) {
		return replayMetadata{}, fmt.Errorf("replay metadata does not match request")
	}
	return metadata, nil
}

func recoveryBundleDirectory(requestPath, metadataPath string) (string, bool) {
	if !filepath.IsAbs(requestPath) || !filepath.IsAbs(metadataPath) || filepath.Base(requestPath) != "request.json" || filepath.Base(metadataPath) != "metadata.json" {
		return "", false
	}
	directory := filepath.Dir(requestPath)
	if filepath.Dir(metadataPath) != directory {
		return "", false
	}
	return directory, true
}

func ownedRecoveryBundleDirectory(requestPath, metadataPath string) (string, bool) {
	directory, valid := recoveryBundleDirectory(requestPath, metadataPath)
	if !valid || filepath.Dir(directory) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(directory), "contentflow-recovery-") {
		return "", false
	}
	return directory, true
}

func validateRecoveryBundle(metadataPath string) (string, bool, bool) {
	requestPath := filepath.Join(filepath.Dir(metadataPath), "request.json")
	directory, valid := recoveryBundleDirectory(requestPath, metadataPath)
	if !valid {
		return "", false, false
	}
	for path, wantMode := range map[string]os.FileMode{directory: 0o700 | os.ModeDir, requestPath: 0o600, metadataPath: 0o600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode() != wantMode {
			return "", false, false
		}
	}
	_, owned := ownedRecoveryBundleDirectory(requestPath, metadataPath)
	return requestPath, owned, true
}

func ownedStandaloneMetadata(metadataPath string) bool {
	if !filepath.IsAbs(metadataPath) || filepath.Dir(metadataPath) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(metadataPath), "contentflow-replay-metadata-") {
		return false
	}
	info, err := os.Lstat(metadataPath)
	return err == nil && info.Mode() == 0o600
}

func marshalRequest(object map[string]json.RawMessage) ([]byte, error) {
	return marshalJSON(object)
}

func marshalJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return preserveJSONLineSeparators(bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})), nil
}

func preserveJSONLineSeparators(encoded []byte) []byte {
	var result []byte
	for index := 0; index < len(encoded); {
		if encoded[index] != '\\' {
			if result != nil {
				result = append(result, encoded[index])
			}
			index++
			continue
		}
		runEnd := index
		for runEnd < len(encoded) && encoded[runEnd] == '\\' {
			runEnd++
		}
		separator := runEnd+5 <= len(encoded) && (bytes.Equal(encoded[runEnd:runEnd+5], []byte("u2028")) || bytes.Equal(encoded[runEnd:runEnd+5], []byte("u2029")))
		if separator && (runEnd-index)%2 == 1 {
			if result == nil {
				result = make([]byte, 0, len(encoded))
				result = append(result, encoded[:index]...)
			}
			result = append(result, encoded[index:runEnd-1]...)
			if encoded[runEnd+4] == '8' {
				result = append(result, "\u2028"...)
			} else {
				result = append(result, "\u2029"...)
			}
			index = runEnd + 5
			continue
		}
		if result != nil {
			result = append(result, encoded[index:runEnd]...)
		}
		index = runEnd
	}
	if result == nil {
		return encoded
	}
	return result
}

func (r runner) operationID(object map[string]json.RawMessage, supplied string) (string, error) {
	existing := ""
	if raw, ok := object["operation_id"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil || existing == "" {
			return "", fmt.Errorf("operation_id in request file must be a string")
		}
	}
	if supplied != "" && existing != "" && supplied != existing {
		return "", fmt.Errorf("--operation-id conflicts with operation_id in request file")
	}
	if supplied != "" {
		return validateOperationID(supplied)
	}
	if existing != "" {
		return validateOperationID(existing)
	}
	generated, err := r.newOperationID()
	if err != nil {
		return "", fmt.Errorf("could not generate operation ID")
	}
	return validateOperationID(generated)
}

func validateOperationID(value string) (string, error) {
	if !isCanonicalULID(value) {
		return "", fmt.Errorf("operation ID must be a ULID")
	}
	return value, nil
}

func canonicalContentID(value string) (string, error) {
	if !isCanonicalULID(value) {
		return "", fmt.Errorf("content ID must be a ULID")
	}
	return value, nil
}

func isCanonicalULID(value string) bool {
	id, err := ulid.ParseStrict(value)
	return err == nil && id.String() == value
}

func setTranscript(object map[string]json.RawMessage, transcript string) error {
	if !utf8.ValidString(transcript) {
		return fmt.Errorf("transcript must be valid UTF-8")
	}
	var contentType string
	if err := json.Unmarshal(object["type"], &contentType); err != nil || contentType != "youtube" {
		return fmt.Errorf("transcript flags require a YouTube request")
	}
	var contentObject map[string]json.RawMessage
	if err := decodeObject(object["content"], &contentObject); err != nil {
		return fmt.Errorf("YouTube content must be a JSON object")
	}
	encoded, err := marshalJSON(transcript)
	if err != nil {
		return err
	}
	contentObject["transcript"] = encoded
	encodedContent, err := marshalJSON(contentObject)
	if err != nil {
		return err
	}
	object["content"] = encodedContent
	return nil
}

func (r runner) readInput(ctx context.Context, path string) ([]byte, bool, error) {
	var reader io.Reader
	var closeReader io.Closer
	regular := false
	if path == "-" {
		reader = r.stdin
		closeReader, _ = r.stdin.(io.Closer)
	} else {
		file, err := openFileWithContext(ctx, path)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, fmt.Errorf("read %s: input interrupted", safeHumanPath(path))
			}
			return nil, false, fmt.Errorf("read %s: file is unavailable", safeHumanPath(path))
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return nil, false, fmt.Errorf("read %s: file is unavailable", safeHumanPath(path))
		}
		regular = info.Mode().IsRegular()
		reader = file
		closeReader = file
	}
	contents, err := readBoundedWithContext(ctx, reader, closeReader, maxInputBytes+1)
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("read %s: input interrupted", safeHumanPath(path))
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: input failed", safeHumanPath(path))
	}
	if len(contents) > maxInputBytes {
		return nil, false, fmt.Errorf("read %s: input exceeds 1 MiB", safeHumanPath(path))
	}
	return contents, regular, nil
}

func readBoundedWithContext(ctx context.Context, reader io.Reader, closer io.Closer, limit int64) ([]byte, error) {
	type readResult struct {
		contents []byte
		err      error
	}
	result := make(chan readResult, 1)
	go func() {
		contents, err := io.ReadAll(io.LimitReader(reader, limit))
		result <- readResult{contents: contents, err: err}
	}()
	select {
	case read := <-result:
		return read.contents, read.err
	case <-ctx.Done():
		if closer != nil {
			_ = closer.Close()
		}
		return nil, ctx.Err()
	}
}

type cancellableWriter struct {
	ctx         context.Context
	destination io.Writer
}

func (writer cancellableWriter) Write(contents []byte) (int, error) {
	type writeResult struct {
		count int
		err   error
	}
	result := make(chan writeResult, 1)
	go func() {
		count, err := writer.destination.Write(contents)
		result <- writeResult{count: count, err: err}
	}()
	select {
	case written := <-result:
		return written.count, written.err
	case <-writer.ctx.Done():
		if closer, ok := writer.destination.(io.Closer); ok {
			_ = closer.Close()
		}
		return 0, writer.ctx.Err()
	}
}

func openFileWithContext(ctx context.Context, path string) (*os.File, error) {
	type openResult struct {
		file *os.File
		err  error
	}
	namedPipe := false
	if info, err := os.Stat(path); err == nil {
		namedPipe = info.Mode()&os.ModeNamedPipe != 0
	}
	result := make(chan openResult)
	go func() {
		file, err := os.Open(path)
		select {
		case result <- openResult{file: file, err: err}:
		case <-ctx.Done():
			if file != nil {
				_ = file.Close()
			}
		}
	}()
	select {
	case opened := <-result:
		return opened.file, opened.err
	case <-ctx.Done():
		if namedPipe {
			unblockNamedPipeOpen(path)
		}
		return nil, ctx.Err()
	}
}

func decodeObject(raw []byte, destination *map[string]json.RawMessage) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	if err := validateUniqueJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil || *destination == nil {
		return errors.New("invalid JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func newOperationID() (string, error) {
	id, err := ulid.New(ulid.Timestamp(time.Now().UTC()), rand.Reader)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (r runner) finish(response apiResponse, requestErr error, jsonOutput bool, renderHuman, renderJSON func(io.Writer, []byte) error) int {
	if requestErr != nil {
		writeStableError(r.stderr, jsonOutput, "request_failed")
		return ExitUnavailable
	}
	if response.Status != http.StatusOK {
		return r.apiError(response, jsonOutput)
	}
	if jsonOutput {
		if err := renderJSON(r.stdout, response.Body); err != nil {
			writeStableError(r.stderr, true, "invalid_api_response")
			return ExitUnavailable
		}
		return ExitSuccess
	}
	if err := renderHuman(r.stdout, response.Body); err != nil {
		writeStableError(r.stderr, false, "invalid_api_response")
		return ExitUnavailable
	}
	return ExitSuccess
}

func (r runner) finishMutation(response apiResponse, requestErr error, jsonOutput bool, expectation mutationExpectation) int {
	if requestErr != nil {
		writeMutationError(r.stderr, jsonOutput, "request_failed", expectation, true)
		return ExitUnavailable
	}
	if response.Status < 200 || response.Status >= 300 {
		return r.apiErrorWithOperation(response, jsonOutput, expectation)
	}
	if response.Status != expectation.httpStatus {
		writeMutationError(r.stderr, jsonOutput, "invalid_api_response", expectation, true)
		return ExitUnavailable
	}
	mutation, err := decodeMutationResponse(response.Body)
	if err != nil || mutation.OperationID != expectation.operationID || mutation.Status != expectation.status || len(mutation.ItemIDs) != expectation.itemCount || (expectation.itemID != "" && mutation.ItemIDs[0] != expectation.itemID) || !allRevisionsEqual(mutation.Revisions, expectation.revision) {
		writeMutationError(r.stderr, jsonOutput, "invalid_api_response", expectation, true)
		return ExitUnavailable
	}
	if jsonOutput {
		if err := writeMutationJSON(r.stdout, mutation); err != nil {
			writeMutationError(r.stderr, true, "invalid_api_response", expectation, true)
			return ExitUnavailable
		}
		removeRecoveryFiles(expectation.cleanupReplay, expectation.cleanupMetadata)
		removeRecoveryBundleDirectory(expectation.cleanupReplay, expectation.cleanupMetadata)
		return ExitSuccess
	}
	if err := renderMutation(r.stdout, response.Body); err != nil {
		writeMutationError(r.stderr, false, "invalid_api_response", expectation, true)
		return ExitUnavailable
	}
	removeRecoveryFiles(expectation.cleanupReplay, expectation.cleanupMetadata)
	removeRecoveryBundleDirectory(expectation.cleanupReplay, expectation.cleanupMetadata)
	return ExitSuccess
}

func removeRecoveryBundleDirectory(requestPath, metadataPath string) {
	if directory, valid := ownedRecoveryBundleDirectory(requestPath, metadataPath); valid {
		_ = os.Remove(directory)
	}
}

func allRevisionsEqual(revisions []int64, expected int64) bool {
	for _, revision := range revisions {
		if revision != expected {
			return false
		}
	}
	return true
}

func removeRecoveryFiles(paths ...string) {
	for _, path := range paths {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func (r runner) apiError(response apiResponse, jsonOutput bool) int {
	return r.apiErrorWithOperation(response, jsonOutput, mutationExpectation{})
}

func (r runner) apiErrorWithOperation(response apiResponse, jsonOutput bool, expectation mutationExpectation) int {
	var rawProblem struct {
		Error string `json:"error"`
	}
	var problemFields map[string]json.RawMessage
	validProblemBody := decodeObject(response.Body, &problemFields) == nil
	if validProblemBody {
		rawError, present := problemFields["error"]
		validProblemBody = present && !bytes.Equal(bytes.TrimSpace(rawError), []byte("null")) && json.Unmarshal(rawError, &rawProblem.Error) == nil && rawProblem.Error != ""
	}
	var decodedCurrent *itemResponse
	if validProblemBody {
		if rawCurrent, present := problemFields["current"]; present {
			decoded, err := decodeItemResponse(rawCurrent)
			if err != nil {
				validProblemBody = false
			} else {
				decodedCurrent = &decoded
			}
		}
	}
	var current *itemResponse
	if validProblemBody && response.Status == http.StatusConflict && rawProblem.Error == "revision_conflict" && expectation.itemID != "" {
		if decodedCurrent == nil || decodedCurrent.ID != expectation.itemID {
			validProblemBody = false
		} else {
			current = decodedCurrent
		}
	}
	replayable := !terminalMutationStatus(response.Status) || !validProblemBody
	problem := struct {
		Error          string        `json:"error"`
		Current        *itemResponse `json:"current,omitempty"`
		OperationID    string        `json:"operation_id,omitempty"`
		ReplayFile     string        `json:"replay_file,omitempty"`
		ReplayMetadata string        `json:"replay_metadata,omitempty"`
		ReplayBefore   int64         `json:"replay_before,omitempty"`
		RequestFile    string        `json:"request_file,omitempty"`
	}{Error: "api_error", OperationID: expectation.operationID}
	if !validProblemBody {
		problem.Error = "invalid_api_response"
	}
	if replayable {
		problem.ReplayFile = expectation.replayFile
		problem.ReplayMetadata = expectation.replayMetadata
		problem.ReplayBefore = expectation.replayBefore
	} else {
		problem.RequestFile = expectation.replayFile
		if expectation.cleanupReplay != "" && expectation.cleanupReplay != expectation.replayFile {
			removeRecoveryFiles(expectation.cleanupReplay, expectation.cleanupMetadata)
			removeRecoveryBundleDirectory(expectation.cleanupReplay, expectation.cleanupMetadata)
		} else {
			removeRecoveryFiles(expectation.cleanupMetadata)
		}
	}
	if validProblemBody && validErrorCode(rawProblem.Error) {
		problem.Error = rawProblem.Error
		problem.Current = current
	}
	if jsonOutput {
		body, _ := json.Marshal(problem)
		_, _ = fmt.Fprintln(r.stderr, string(body))
	} else {
		writeStableError(r.stderr, false, problem.Error)
		if expectation.operationID != "" {
			_, _ = fmt.Fprintf(r.stderr, "operation_id: %s\n", expectation.operationID)
		}
		if expectation.replayFile != "" {
			label := "request_file"
			if replayable {
				label = "replay_file"
			}
			_, _ = fmt.Fprintf(r.stderr, "%s: %s\n", label, safeHumanPath(expectation.replayFile))
		}
		if replayable && expectation.replayMetadata != "" {
			_, _ = fmt.Fprintf(r.stderr, "replay_metadata: %s\nreplay_before: %d\n", safeHumanPath(expectation.replayMetadata), expectation.replayBefore)
		}
	}
	if !validProblemBody {
		return ExitUnavailable
	}
	switch response.Status {
	case http.StatusUnauthorized:
		return ExitAuth
	case http.StatusForbidden:
		return ExitForbidden
	case http.StatusNotFound:
		return ExitNotFound
	case http.StatusConflict:
		return ExitConflict
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return ExitInvalid
	case http.StatusTooManyRequests:
		return ExitRateLimited
	default:
		return ExitUnavailable
	}
}

func terminalMutationStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func validErrorCode(value string) bool {
	switch value {
	case "authentication_required", "authentication_unavailable", "insufficient_scope", "invalid_bearer_token", "rate_limit_exceeded",
		"batch_item_not_standalone", "content_document_too_large", "content_encoding_failed", "content_not_found", "content_unavailable",
		"duplicate_section_id", "id_generation_failed", "invalid_batch_size", "invalid_content", "invalid_discriminator", "invalid_operation_id",
		"invalid_query", "invalid_request", "invalid_revision", "invalid_section_id", "invalid_section_order", "invalid_status", "invalid_status_filter",
		"invalid_title_prefix", "invalid_type_filter", "operation_id_conflict", "receipt_document_too_large", "request_too_large", "revision_conflict",
		"section_document_too_large", "section_id_not_allowed", "text_field_too_large", "too_many_sections", "transcript_missing",
		"transcript_not_supported", "transcript_required", "unknown_section_id":
		return true
	default:
		return false
	}
}

func (r runner) usageError(message string) int {
	writeLocalError(r.stderr, r.jsonOutput, "usage_error", message)
	return ExitUsage
}

func writeLocalError(destination io.Writer, jsonOutput bool, code, detail string) {
	if jsonOutput {
		encoded, _ := json.Marshal(struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}{Error: code, Detail: detail})
		_, _ = fmt.Fprintln(destination, string(encoded))
		return
	}
	_, _ = fmt.Fprintf(destination, "error: %s\n", detail)
}

func writeStableError(destination io.Writer, jsonOutput bool, code string) {
	if jsonOutput {
		encoded, _ := json.Marshal(map[string]string{"error": code})
		_, _ = fmt.Fprintln(destination, string(encoded))
		return
	}
	_, _ = fmt.Fprintf(destination, "error: %s\n", code)
}

func writeMutationError(destination io.Writer, jsonOutput bool, code string, expectation mutationExpectation, replayable bool) {
	if jsonOutput {
		problem := struct {
			Error          string `json:"error"`
			OperationID    string `json:"operation_id"`
			ReplayFile     string `json:"replay_file,omitempty"`
			ReplayMetadata string `json:"replay_metadata,omitempty"`
			ReplayBefore   int64  `json:"replay_before,omitempty"`
			RequestFile    string `json:"request_file,omitempty"`
		}{Error: code, OperationID: expectation.operationID}
		if replayable {
			problem.ReplayFile = expectation.replayFile
			problem.ReplayMetadata = expectation.replayMetadata
			problem.ReplayBefore = expectation.replayBefore
		} else {
			problem.RequestFile = expectation.replayFile
		}
		encoded, _ := json.Marshal(problem)
		_, _ = fmt.Fprintln(destination, string(encoded))
		return
	}
	_, _ = fmt.Fprintf(destination, "error: %s\noperation_id: %s\n", code, expectation.operationID)
	if expectation.replayFile != "" {
		label := "request_file"
		if replayable {
			label = "replay_file"
		}
		_, _ = fmt.Fprintf(destination, "%s: %s\n", label, safeHumanPath(expectation.replayFile))
	}
	if replayable && expectation.replayMetadata != "" {
		_, _ = fmt.Fprintf(destination, "replay_metadata: %s\nreplay_before: %d\n", safeHumanPath(expectation.replayMetadata), expectation.replayBefore)
	}
}

func safeHumanPath(value string) string {
	if !utf8.ValidString(value) || strings.IndexFunc(value, func(character rune) bool { return !unicode.IsPrint(character) }) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

func firstError(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

const usage = `flow is the scoped HTTP client for ContentFlow.

Usage:
  flow content list [--search PREFIX] [--type TYPE] [--status STATUS] [--json]
  flow content show ID [--json]
  flow content transcript ID [--json]
  flow content create --file PATH [--transcript-file PATH|- | --clear-transcript] [--operation-id ID] [--replay-metadata PATH | --replay-before UNIX] [--json]
  flow content update ID --file PATH [--transcript-file PATH|- | --clear-transcript] [--operation-id ID] [--replay-metadata PATH | --replay-before UNIX] [--json]
  flow content batch-create --file PATH [--operation-id ID] [--replay-metadata PATH | --replay-before UNIX] [--json]

Configuration:
  CONTENTFLOW_API_URL    HTTPS origin (HTTP requires a literal loopback IP)
  CONTENTFLOW_API_TOKEN  bearer token with content:read or content:write scope
`
