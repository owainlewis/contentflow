package flowcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

type summary struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	WorkingTitle string          `json:"working_title"`
	Revision     int64           `json:"revision"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	AssetCounts  map[string]*int `json:"asset_counts"`
}

type listResponse struct {
	Items []summary `json:"items"`
}

type itemResponse struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	WorkingTitle string          `json:"working_title"`
	Revision     int64           `json:"revision"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	Content      json.RawMessage `json:"content"`
}

type transcriptResponse struct {
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	Transcript string `json:"transcript"`
}

type mutationResponse struct {
	OperationID string      `json:"operation_id"`
	ItemIDs     []string    `json:"item_ids"`
	Revisions   []int64     `json:"revisions"`
	ExpiresAt   []time.Time `json:"expires_at"`
	Status      string      `json:"status"`
}

func renderList(destination io.Writer, raw []byte) error {
	return renderListStream(destination, bytes.NewReader(raw))
}

func renderListStream(destination io.Writer, source io.Reader) error {
	table := tabwriter.NewWriter(destination, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "ID\tTYPE\tSTATUS\tREVISION\tASSETS\tWORKING TITLE")
	err := decodeListStream(source, func(item summary) error {
		assets := 0
		for _, count := range item.AssetCounts {
			assets += *count
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%s\n", item.ID, item.Type, item.Status, item.Revision, assets, quoteHumanText(item.WorkingTitle))
		return nil
	})
	if err != nil {
		return err
	}
	return table.Flush()
}

func validSummary(item summary) bool {
	if !isCanonicalULID(item.ID) || !validContentType(item.Type) || !validContentStatus(item.Status) || len([]byte(item.WorkingTitle)) > 500<<10 || item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || item.ExpiresAt.IsZero() || item.AssetCounts == nil {
		return false
	}
	for _, count := range item.AssetCounts {
		if count == nil || *count < 0 {
			return false
		}
	}
	return true
}

func quoteHumanText(value string) string { return strconv.Quote(value) }

func renderItem(destination io.Writer, raw []byte) error {
	return renderItemForID(destination, raw, "")
}

func renderItemForID(destination io.Writer, raw []byte, expectedID string) error {
	item, err := decodeItemResponse(raw)
	if err != nil || (expectedID != "" && item.ID != expectedID) {
		if err == nil {
			err = fmt.Errorf("invalid item response")
		}
		return err
	}
	var content bytes.Buffer
	if err := json.Indent(&content, item.Content, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintf(destination, "ID: %s\nType: %s\nStatus: %s\nWorking title: %s\nRevision: %d\nCreated at: %s\nUpdated at: %s\nExpires at: %s\nContent:\n%s\n",
		item.ID, item.Type, item.Status, quoteHumanText(item.WorkingTitle), item.Revision,
		item.CreatedAt.UTC().Format(time.RFC3339Nano), item.UpdatedAt.UTC().Format(time.RFC3339Nano), item.ExpiresAt.UTC().Format(time.RFC3339Nano), content.String())
	return err
}

func decodeItemResponse(raw []byte) (itemResponse, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return itemResponse{}, fmt.Errorf("invalid item response")
	}
	var item itemResponse
	if err := decodeStrictJSON(raw, &item); err != nil {
		return itemResponse{}, fmt.Errorf("invalid item response")
	}
	if !isCanonicalULID(item.ID) || !validContentType(item.Type) || !validContentStatus(item.Status) || len([]byte(item.WorkingTitle)) > 500<<10 || item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || item.ExpiresAt.IsZero() {
		return itemResponse{}, fmt.Errorf("invalid item response")
	}
	if !validItemContent(item.Type, item.Content) {
		return itemResponse{}, fmt.Errorf("invalid item response")
	}
	return item, nil
}

type responseSection struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

type youtubeResponseContent struct {
	Topic           string            `json:"topic"`
	ICP             string            `json:"icp"`
	Angle           string            `json:"angle"`
	CTA             string            `json:"cta"`
	PublishingTitle string            `json:"publishing_title"`
	Description     string            `json:"description"`
	Transcript      string            `json:"transcript"`
	Sections        []responseSection `json:"sections"`
}

func validItemContent(contentType string, raw []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil || hasNullField(fields) {
		return false
	}
	switch contentType {
	case "youtube":
		if _, exists := fields["transcript"]; !exists {
			return false
		}
		var content youtubeResponseContent
		if err := decodeStrictJSON(raw, &content); err != nil {
			return false
		}
		if len(content.Sections) > 200 || !validTextFields(content.Topic, content.ICP, content.Angle, content.CTA, content.PublishingTitle, content.Description, content.Transcript) {
			return false
		}
		var rawSections []json.RawMessage
		if raw, exists := fields["sections"]; exists {
			if err := json.Unmarshal(raw, &rawSections); err != nil {
				return false
			}
		}
		seenIDs := make(map[string]struct{}, len(content.Sections))
		for index, section := range content.Sections {
			var sectionFields map[string]json.RawMessage
			if err := json.Unmarshal(rawSections[index], &sectionFields); err != nil || hasNullField(sectionFields) {
				return false
			}
			if _, hasID := sectionFields["id"]; !hasID {
				return false
			}
			if _, hasPosition := sectionFields["position"]; !hasPosition {
				return false
			}
			if !isCanonicalULID(section.ID) || section.Position != index || !validTextFields(section.Title, section.Body) {
				return false
			}
			if _, exists := seenIDs[section.ID]; exists {
				return false
			}
			seenIDs[section.ID] = struct{}{}
		}
		return true
	case "linkedin", "x":
		var content struct {
			Body string `json:"body"`
		}
		return decodesStrictContent(raw, &content) && validTextFields(content.Body)
	case "instagram", "tiktok":
		var content struct {
			Script string `json:"script"`
		}
		return decodesStrictContent(raw, &content) && validTextFields(content.Script)
	case "email":
		var content struct {
			Subject string `json:"subject"`
			Body    string `json:"body"`
		}
		return decodesStrictContent(raw, &content) && validTextFields(content.Subject, content.Body)
	case "substack":
		var content struct {
			Headline    string `json:"headline"`
			Subheadline string `json:"subheadline"`
			Body        string `json:"body"`
		}
		return decodesStrictContent(raw, &content) && validTextFields(content.Headline, content.Subheadline, content.Body)
	default:
		return false
	}
}

func validTextFields(values ...string) bool {
	for _, value := range values {
		if len([]byte(value)) > 500<<10 {
			return false
		}
	}
	return true
}

func hasNullField(fields map[string]json.RawMessage) bool {
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return true
		}
	}
	return false
}

func decodesStrictContent(raw []byte, destination any) bool {
	return decodeStrictJSON(raw, destination) == nil
}

func renderTranscript(destination io.Writer, raw []byte) error {
	return renderTranscriptForID(destination, raw, "")
}

func renderTranscriptForID(destination io.Writer, raw []byte, expectedID string) error {
	transcript, err := decodeTranscriptResponse(raw)
	if err != nil || (expectedID != "" && transcript.ID != expectedID) {
		if err == nil {
			err = fmt.Errorf("invalid transcript response")
		}
		return err
	}
	_, err = io.WriteString(destination, transcript.Transcript)
	return err
}

func decodeTranscriptResponse(raw []byte) (transcriptResponse, error) {
	var transcript transcriptResponse
	if err := decodeStrictJSON(raw, &transcript); err != nil || transcript.Revision < 1 || strings.TrimSpace(transcript.Transcript) == "" || !validTextFields(transcript.Transcript) {
		return transcriptResponse{}, fmt.Errorf("invalid transcript response")
	}
	if !isCanonicalULID(transcript.ID) {
		return transcriptResponse{}, fmt.Errorf("invalid transcript response")
	}
	return transcript, nil
}

func renderMutation(destination io.Writer, raw []byte) error {
	mutation, err := decodeMutationResponse(raw)
	if err != nil {
		return err
	}
	expires := make([]string, len(mutation.ExpiresAt))
	for index, value := range mutation.ExpiresAt {
		expires[index] = value.UTC().Format(time.RFC3339Nano)
	}
	revisions := make([]string, len(mutation.Revisions))
	for index, value := range mutation.Revisions {
		revisions[index] = fmt.Sprint(value)
	}
	_, err = fmt.Fprintf(destination, "Status: %s\nOperation ID: %s\nItem IDs: %s\nRevisions: %s\nExpires at: %s\n",
		mutation.Status, mutation.OperationID, strings.Join(mutation.ItemIDs, ","), strings.Join(revisions, ","), strings.Join(expires, ","))
	return err
}

func decodeMutationResponse(raw []byte) (mutationResponse, error) {
	var mutation mutationResponse
	if err := decodeStrictJSON(raw, &mutation); err != nil || mutation.OperationID == "" || !validMutationStatus(mutation.Status) || len(mutation.ItemIDs) == 0 || len(mutation.ItemIDs) != len(mutation.Revisions) || len(mutation.ItemIDs) != len(mutation.ExpiresAt) {
		return mutationResponse{}, fmt.Errorf("invalid mutation response")
	}
	if !isCanonicalULID(mutation.OperationID) {
		return mutationResponse{}, fmt.Errorf("invalid mutation response")
	}
	seenIDs := make(map[string]struct{}, len(mutation.ItemIDs))
	for index, itemID := range mutation.ItemIDs {
		if !isCanonicalULID(itemID) || mutation.Revisions[index] < 1 || mutation.ExpiresAt[index].IsZero() {
			return mutationResponse{}, fmt.Errorf("invalid mutation response")
		}
		if _, exists := seenIDs[itemID]; exists {
			return mutationResponse{}, fmt.Errorf("invalid mutation response")
		}
		seenIDs[itemID] = struct{}{}
	}
	return mutation, nil
}

func writeListJSONStream(destination io.Writer, source io.Reader) error {
	if _, err := io.WriteString(destination, `{"items":[`); err != nil {
		return err
	}
	first := true
	err := decodeListStream(source, func(item summary) error {
		if !first {
			if _, err := io.WriteString(destination, ","); err != nil {
				return err
			}
		}
		first = false
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		_, err = destination.Write(encoded)
		return err
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(destination, "]}\n")
	return err
}

func decodeListStream(source io.Reader, visit func(summary) error) error {
	decoder := json.NewDecoder(source)
	decoder.DisallowUnknownFields()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("invalid list response")
	}
	field, err := decoder.Token()
	if err != nil || field != "items" {
		return fmt.Errorf("invalid list response")
	}
	items, err := decoder.Token()
	if err != nil || items != json.Delim('[') {
		return fmt.Errorf("invalid list response")
	}
	seenIDs := make(map[string]struct{})
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || !utf8.Valid(raw) {
			return fmt.Errorf("invalid list response")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("invalid list response")
		}
		var item summary
		if err := decodeStrictJSON(raw, &item); err != nil || !validSummary(item) {
			return fmt.Errorf("invalid list response")
		}
		if _, exists := seenIDs[item.ID]; exists {
			return fmt.Errorf("invalid list response")
		}
		seenIDs[item.ID] = struct{}{}
		if err := visit(item); err != nil {
			return err
		}
	}
	closingItems, err := decoder.Token()
	if err != nil || closingItems != json.Delim(']') || decoder.More() {
		return fmt.Errorf("invalid list response")
	}
	closingObject, err := decoder.Token()
	if err != nil || closingObject != json.Delim('}') {
		return fmt.Errorf("invalid list response")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid list response")
	}
	return nil
}

func explicitNull(fields map[string]json.RawMessage, name string) bool {
	value, exists := fields[name]
	return exists && bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func writeItemJSONForID(destination io.Writer, raw []byte, expectedID string) error {
	response, err := decodeItemResponse(raw)
	if err != nil || (expectedID != "" && response.ID != expectedID) {
		if err == nil {
			err = fmt.Errorf("invalid item response")
		}
		return err
	}
	return writeCanonicalJSON(destination, response)
}

func writeTranscriptJSONForID(destination io.Writer, raw []byte, expectedID string) error {
	response, err := decodeTranscriptResponse(raw)
	if err != nil || (expectedID != "" && response.ID != expectedID) {
		if err == nil {
			err = fmt.Errorf("invalid transcript response")
		}
		return err
	}
	return writeCanonicalJSON(destination, response)
}

func writeMutationJSON(destination io.Writer, response mutationResponse) error {
	return writeCanonicalJSON(destination, response)
}

func writeCanonicalJSON(destination io.Writer, value any) error {
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func decodeStrictJSON(raw []byte, destination any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid UTF-8")
	}
	if err := validateUniqueJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func validateUniqueJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate object key")
			}
			seen[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	return nil
}

func validContentType(value string) bool {
	switch value {
	case "youtube", "linkedin", "x", "instagram", "tiktok", "email", "substack":
		return true
	default:
		return false
	}
}

func validContentStatus(value string) bool {
	switch value {
	case "idea", "draft", "ready", "published":
		return true
	default:
		return false
	}
}

func validMutationStatus(value string) bool {
	switch value {
	case "created", "updated", "deleted":
		return true
	default:
		return false
	}
}
