package content

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxRequestBytes = 1 << 20
	MaxTextBytes    = 500 << 10
	// Bound the indexed title prefix while retaining the complete working title.
	MaxIndexedStringBytes = 1498
	MaxSections           = 200
	MaxBatchItems         = 50
	ReceiptLifetime       = 24 * time.Hour
)

type Type string

const (
	TypeTopic     Type = "topic"
	TypeYouTube   Type = "youtube"
	TypeLinkedIn  Type = "linkedin"
	TypeX         Type = "x"
	TypeInstagram Type = "instagram"
	TypeTikTok    Type = "tiktok"
	TypeEmail     Type = "email"
	TypeSubstack  Type = "substack"
)

type Status string

const (
	StatusIdea      Status = "idea"
	StatusDraft     Status = "draft"
	StatusReady     Status = "ready"
	StatusPublished Status = "published"
)

type Section struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

type YouTubeContent struct {
	Topic           string    `json:"topic"`
	ICP             string    `json:"icp"`
	Angle           string    `json:"angle"`
	CTA             string    `json:"cta"`
	PublishingTitle string    `json:"publishing_title"`
	Description     string    `json:"description"`
	Transcript      string    `json:"transcript"`
	Sections        []Section `json:"sections"`
}

type LinkedInContent struct {
	Body string `json:"body"`
}

type XContent struct {
	Body string `json:"body"`
}

type TopicContent struct {
	Source    string `json:"source"`
	SourceURL string `json:"source_url"`
}

type InstagramContent struct {
	Caption string `json:"caption"`
	Script  string `json:"script"`
}

type TikTokContent struct {
	Script string `json:"script"`
}

type EmailContent struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type SubstackContent struct {
	Headline    string `json:"headline"`
	Subheadline string `json:"subheadline"`
	Body        string `json:"body"`
}

type Item struct {
	TopicID     string `json:"topic_id,omitempty"`
	Format      string `json:"format,omitempty"`
	DocumentURL string `json:"document_url,omitempty"`
	VideoURL    string `json:"video_url,omitempty"`

	ID                     string     `json:"id"`
	WorkspaceID            string     `json:"-"`
	Type                   Type       `json:"type"`
	Status                 Status     `json:"status"`
	WorkingTitle           string     `json:"working_title"`
	NormalizedWorkingTitle string     `json:"-"`
	SearchableWorkingTitle string     `json:"-"`
	Revision               int64      `json:"revision"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	ExpiresAt              time.Time  `json:"expires_at"`
	ScheduledAt            *time.Time `json:"scheduled_at,omitempty"`
	Content                any        `json:"content"`
}

type Summary struct {
	TopicID     string `json:"topic_id,omitempty"`
	Format      string `json:"format,omitempty"`
	DocumentURL string `json:"document_url,omitempty"`
	VideoURL    string `json:"video_url,omitempty"`

	ID           string         `json:"id"`
	Type         Type           `json:"type"`
	Status       Status         `json:"status"`
	WorkingTitle string         `json:"working_title"`
	Revision     int64          `json:"revision"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
	ScheduledAt  *time.Time     `json:"scheduled_at,omitempty"`
	AssetCounts  map[string]int `json:"asset_counts"`
}

func (i Item) Summary() Summary {
	return Summary{
		TopicID: i.TopicID, Format: i.Format, DocumentURL: i.DocumentURL, VideoURL: i.VideoURL, ID: i.ID, Type: i.Type, Status: i.Status, WorkingTitle: i.WorkingTitle,
		Revision: i.Revision, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
		ExpiresAt: i.ExpiresAt, ScheduledAt: i.ScheduledAt, AssetCounts: map[string]int{},
	}
}

type Transcript struct {
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	Transcript string `json:"transcript"`
}

type MutationResult struct {
	OperationID string      `json:"operation_id"`
	ItemIDs     []string    `json:"item_ids"`
	Revisions   []int64     `json:"revisions"`
	ExpiresAt   []time.Time `json:"expires_at"`
	Status      string      `json:"status"`
}

type Receipt struct {
	WorkspaceID string
	RequestHash string
	Operation   string
	HTTPStatus  int
	MutationResult
	ErrorCode string
	Expires   time.Time
}

type CreateRequest struct {
	TopicID     string `json:"topic_id,omitempty"`
	Format      string `json:"format,omitempty"`
	DocumentURL string `json:"document_url,omitempty"`
	VideoURL    string `json:"video_url,omitempty"`

	Type         Type
	WorkingTitle string
	Status       Status
	OperationID  string
	ScheduledAt  *time.Time
	Content      any
}

type BatchItemRequest struct {
	TopicID     string `json:"topic_id,omitempty"`
	Format      string `json:"format,omitempty"`
	DocumentURL string `json:"document_url,omitempty"`
	VideoURL    string `json:"video_url,omitempty"`

	Type         Type
	WorkingTitle string
	Status       Status
	ScheduledAt  *time.Time
	Content      any
}

type BatchRequest struct {
	OperationID string
	Items       []BatchItemRequest
}

type ReplaceRequest struct {
	CreateRequest
	Revision int64
}

type RevisionRequest struct {
	OperationID string `json:"operation_id"`
	Revision    int64  `json:"revision"`
}

type ListQuery struct {
	Type        Type
	Status      Status
	TitlePrefix string
}

type Error struct {
	Code    string
	Status  int
	Current *Item
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Cause.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Cause }

func problem(status int, code string) error { return &Error{Status: status, Code: code} }

func DecodeCreate(raw []byte) (CreateRequest, error) {
	var wire struct {
		TopicID     string `json:"topic_id,omitempty"`
		Format      string `json:"format,omitempty"`
		DocumentURL string `json:"document_url,omitempty"`
		VideoURL    string `json:"video_url,omitempty"`

		Type         Type            `json:"type"`
		WorkingTitle string          `json:"working_title"`
		Status       Status          `json:"status"`
		OperationID  string          `json:"operation_id"`
		ScheduledAt  *time.Time      `json:"scheduled_at"`
		Content      json.RawMessage `json:"content"`
	}
	if err := decodeExact(raw, &wire); err != nil {
		return CreateRequest{}, problem(400, "invalid_request")
	}
	contentValue, err := decodeTypedContent(wire.Type, wire.Content)
	if err != nil {
		return CreateRequest{}, err
	}
	request := CreateRequest{Type: wire.Type, WorkingTitle: wire.WorkingTitle, Status: wire.Status, OperationID: wire.OperationID, ScheduledAt: wire.ScheduledAt, TopicID: wire.TopicID, Format: wire.Format, DocumentURL: wire.DocumentURL, VideoURL: wire.VideoURL, Content: contentValue}
	if err := validateRequest(request); err != nil {
		return CreateRequest{}, err
	}
	return request, nil
}

func DecodeBatch(raw []byte) (BatchRequest, error) {
	var wire struct {
		OperationID string            `json:"operation_id"`
		Items       []json.RawMessage `json:"items"`
	}
	if err := decodeExact(raw, &wire); err != nil {
		return BatchRequest{}, problem(400, "invalid_request")
	}
	request := BatchRequest{OperationID: wire.OperationID, Items: make([]BatchItemRequest, len(wire.Items))}
	if err := validateBatchOperation(request.OperationID, len(request.Items)); err != nil {
		return BatchRequest{}, err
	}
	for index, rawItem := range wire.Items {
		item, err := decodeBatchItem(rawItem, request.OperationID)
		if err != nil {
			return BatchRequest{}, err
		}
		request.Items[index] = item
	}
	return request, nil
}

func decodeBatchItem(raw []byte, operationID string) (BatchItemRequest, error) {
	var wire struct {
		TopicID     string `json:"topic_id,omitempty"`
		Format      string `json:"format,omitempty"`
		DocumentURL string `json:"document_url,omitempty"`
		VideoURL    string `json:"video_url,omitempty"`

		Type         Type            `json:"type"`
		WorkingTitle string          `json:"working_title"`
		Status       Status          `json:"status"`
		ScheduledAt  *time.Time      `json:"scheduled_at"`
		Content      json.RawMessage `json:"content"`
	}
	if err := decodeExact(raw, &wire); err != nil {
		return BatchItemRequest{}, problem(400, "invalid_request")
	}
	contentValue, err := decodeTypedContent(wire.Type, wire.Content)
	if err != nil {
		return BatchItemRequest{}, err
	}
	item := BatchItemRequest{Type: wire.Type, WorkingTitle: wire.WorkingTitle, Status: wire.Status, ScheduledAt: wire.ScheduledAt, TopicID: wire.TopicID, Format: wire.Format, DocumentURL: wire.DocumentURL, VideoURL: wire.VideoURL, Content: contentValue}
	if err := validateBatchItem(item, operationID); err != nil {
		return BatchItemRequest{}, err
	}
	return item, nil
}

func DecodeReplace(raw []byte) (ReplaceRequest, error) {
	var wire struct {
		TopicID     string `json:"topic_id,omitempty"`
		Format      string `json:"format,omitempty"`
		DocumentURL string `json:"document_url,omitempty"`
		VideoURL    string `json:"video_url,omitempty"`

		Type         Type            `json:"type"`
		WorkingTitle string          `json:"working_title"`
		Status       Status          `json:"status"`
		OperationID  string          `json:"operation_id"`
		Revision     *int64          `json:"revision"`
		ScheduledAt  *time.Time      `json:"scheduled_at"`
		Content      json.RawMessage `json:"content"`
	}
	if err := decodeExact(raw, &wire); err != nil || wire.Revision == nil {
		return ReplaceRequest{}, problem(400, "invalid_request")
	}
	contentValue, err := decodeTypedContent(wire.Type, wire.Content)
	if err != nil {
		return ReplaceRequest{}, err
	}
	request := ReplaceRequest{CreateRequest: CreateRequest{Type: wire.Type, WorkingTitle: wire.WorkingTitle, Status: wire.Status, OperationID: wire.OperationID, ScheduledAt: wire.ScheduledAt, TopicID: wire.TopicID, Format: wire.Format, DocumentURL: wire.DocumentURL, VideoURL: wire.VideoURL, Content: contentValue}, Revision: *wire.Revision}
	if request.Revision < 1 {
		return ReplaceRequest{}, problem(400, "invalid_revision")
	}
	if err := validateRequest(request.CreateRequest); err != nil {
		return ReplaceRequest{}, err
	}
	return request, nil
}

func DecodeRevision(raw []byte) (RevisionRequest, error) {
	var wire struct {
		OperationID string `json:"operation_id"`
		Revision    *int64 `json:"revision"`
	}
	if err := decodeExact(raw, &wire); err != nil || wire.Revision == nil || *wire.Revision < 1 {
		return RevisionRequest{}, problem(400, "invalid_request")
	}
	request := RevisionRequest{OperationID: wire.OperationID, Revision: *wire.Revision}
	if err := validateRevisionRequest(request); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

func decodeTypedContent(contentType Type, raw json.RawMessage) (any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, problem(400, "invalid_content")
	}
	switch contentType {
	case TypeTopic:
		var value TopicContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeYouTube:
		var presence map[string]json.RawMessage
		if err := json.Unmarshal(raw, &presence); err != nil {
			return nil, problem(400, "invalid_content")
		}
		if _, exists := presence["transcript"]; !exists {
			return nil, problem(400, "transcript_required")
		}
		var value YouTubeContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeLinkedIn:
		var value LinkedInContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeX:
		var value XContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeInstagram:
		var value InstagramContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeTikTok:
		var value TikTokContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeEmail:
		var value EmailContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	case TypeSubstack:
		var value SubstackContent
		if err := decodeExact(raw, &value); err != nil {
			return nil, problem(400, "invalid_content")
		}
		return value, nil
	default:
		return nil, problem(400, "invalid_discriminator")
	}
}

func decodeExact(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func validateRequest(request CreateRequest) error {
	if _, err := ulid.ParseStrict(request.OperationID); err != nil {
		return problem(400, "invalid_operation_id")
	}
	if !validType(request.Type) {
		return problem(400, "invalid_discriminator")
	}
	if !validStatus(request.Status) {
		return problem(400, "invalid_status")
	}
	if request.ScheduledAt != nil && request.ScheduledAt.IsZero() {
		return problem(400, "invalid_scheduled_at")
	}
	// A working title is optional. Only YouTube has a title the writer actually
	// authors; every other type is identified by its ID until it is named.
	if request.TopicID != "" {
		if _, err := ulid.ParseStrict(request.TopicID); err != nil {
			return problem(400, "invalid_topic_id")
		}
	}
	if request.Type == TypeTopic && (request.TopicID != "" || request.ScheduledAt != nil) {
		return problem(400, "invalid_topic")
	}
	for _, link := range []string{request.DocumentURL, request.VideoURL} {
		if !validExternalURL(link) {
			return problem(400, "invalid_external_url")
		}
	}
	if len(request.Format) > 100 {
		return problem(400, "invalid_format")
	}
	texts := []string{request.WorkingTitle}
	switch value := request.Content.(type) {
	case TopicContent:
		if request.Type != TypeTopic {
			return problem(400, "invalid_discriminator")
		}
		if !validExternalURL(value.SourceURL) {
			return problem(400, "invalid_external_url")
		}
		texts = append(texts, value.Source)
	case YouTubeContent:
		if request.Type != TypeYouTube {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Topic, value.ICP, value.Angle, value.CTA, value.PublishingTitle, value.Description, value.Transcript)
		if len(value.Sections) > MaxSections {
			return problem(400, "too_many_sections")
		}
		seenIDs := make(map[string]struct{}, len(value.Sections))
		for index, section := range value.Sections {
			if section.Position != index {
				return problem(400, "invalid_section_order")
			}
			if section.ID != "" {
				if _, err := ulid.ParseStrict(section.ID); err != nil {
					return problem(400, "invalid_section_id")
				}
				if _, exists := seenIDs[section.ID]; exists {
					return problem(400, "duplicate_section_id")
				}
				seenIDs[section.ID] = struct{}{}
			}
			texts = append(texts, section.Title, section.Body)
		}
	case LinkedInContent:
		if request.Type != TypeLinkedIn {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Body)
	case XContent:
		if request.Type != TypeX {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Body)
	case InstagramContent:
		if request.Type != TypeInstagram {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Script, value.Caption)
	case TikTokContent:
		if request.Type != TypeTikTok {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Script)
	case EmailContent:
		if request.Type != TypeEmail {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Subject, value.Body)
	case SubstackContent:
		if request.Type != TypeSubstack {
			return problem(400, "invalid_discriminator")
		}
		texts = append(texts, value.Headline, value.Subheadline, value.Body)
	default:
		return problem(400, "invalid_discriminator")
	}
	for _, text := range texts {
		if len([]byte(text)) > MaxTextBytes {
			return problem(413, "text_field_too_large")
		}
	}
	return nil
}

func validateBatchRequest(request BatchRequest) error {
	if err := validateBatchOperation(request.OperationID, len(request.Items)); err != nil {
		return err
	}
	for _, item := range request.Items {
		if err := validateBatchItem(item, request.OperationID); err != nil {
			return err
		}
	}
	return nil
}

func validateBatchOperation(operationID string, itemCount int) error {
	if _, err := ulid.ParseStrict(operationID); err != nil {
		return problem(400, "invalid_operation_id")
	}
	if itemCount < 1 || itemCount > MaxBatchItems {
		return problem(400, "invalid_batch_size")
	}
	return nil
}

func validateBatchItem(item BatchItemRequest, operationID string) error {
	request := CreateRequest{
		Type: item.Type, WorkingTitle: item.WorkingTitle, Status: item.Status,
		TopicID: item.TopicID, Format: item.Format, DocumentURL: item.DocumentURL, VideoURL: item.VideoURL, OperationID: operationID, ScheduledAt: item.ScheduledAt, Content: item.Content,
	}
	if err := validateRequest(request); err != nil {
		return err
	}
	if youtube, ok := item.Content.(YouTubeContent); ok && len(youtube.Sections) != 0 {
		return problem(400, "batch_item_not_standalone")
	}
	return nil
}

func validateRevisionRequest(request RevisionRequest) error {
	if _, err := ulid.ParseStrict(request.OperationID); err != nil {
		return problem(400, "invalid_operation_id")
	}
	if request.Revision < 1 {
		return problem(400, "invalid_revision")
	}
	return nil
}

func validType(value Type) bool {
	switch value {
	case TypeTopic, TypeYouTube, TypeLinkedIn, TypeX, TypeInstagram, TypeTikTok, TypeEmail, TypeSubstack:
		return true
	default:
		return false
	}
}

func validStatus(value Status) bool {
	switch value {
	case StatusIdea, StatusDraft, StatusReady, StatusPublished:
		return true
	default:
		return false
	}
}

func NormalizeTitle(value string) string {
	return cases.Fold().String(norm.NFKC.String(value))
}

func SearchableTitle(value string) string {
	if len(value) <= MaxIndexedStringBytes {
		return value
	}
	end := 0
	for index := range value {
		if index > MaxIndexedStringBytes {
			break
		}
		end = index
	}
	if end == 0 {
		return ""
	}
	return value[:end]
}

func titlePrefixSuccessor(value string) (string, bool) {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] == unicode.MaxRune {
			continue
		}
		if runes[index] == 0xD7FF {
			runes[index] = 0xE000
		} else {
			runes[index]++
		}
		return string(runes[:index+1]), true
	}
	return "", false
}

func transcriptMissing(value string) bool {
	return strings.TrimFunc(value, unicode.IsSpace) == ""
}

type idGenerator struct {
	mu       sync.Mutex
	entropy  io.Reader
	lastTime uint64
}

func newIDGenerator() *idGenerator { return &idGenerator{entropy: ulid.Monotonic(rand.Reader, 0)} }

func (g *idGenerator) New(now time.Time) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	timestamp := ulid.Timestamp(now)
	if timestamp < g.lastTime {
		timestamp = g.lastTime
	}
	id, err := ulid.New(timestamp, g.entropy)
	if err != nil {
		return "", err
	}
	g.lastTime = timestamp
	return id.String(), nil
}

func validExternalURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 8192 {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Hostname() != "" && parsed.User == nil
}

// ExpiresAt remains on the wire for older clients; stored content has no expiry.
var permanentContentExpiry = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
