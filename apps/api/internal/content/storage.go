package content

import (
	"fmt"
	"reflect"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type parentDocument struct {
	WorkspaceID            string         `firestore:"workspace_id"`
	Type                   Type           `firestore:"type"`
	Status                 Status         `firestore:"status"`
	WorkingTitle           string         `firestore:"working_title"`
	NormalizedWorkingTitle string         `firestore:"normalized_working_title"`
	Revision               int64          `firestore:"revision"`
	CreatedAt              time.Time      `firestore:"created_at"`
	UpdatedAt              time.Time      `firestore:"updated_at"`
	ExpiresAt              time.Time      `firestore:"expires_at"`
	ArchivedAt             *time.Time     `firestore:"archived_at,omitempty"`
	Content                map[string]any `firestore:"content"`
}

type sectionDocument struct {
	Position    int       `firestore:"position"`
	Title       string    `firestore:"title"`
	Body        string    `firestore:"body"`
	WorkspaceID string    `firestore:"workspace_id"`
	ExpiresAt   time.Time `firestore:"expires_at"`
}

func toParentDocument(item Item) (parentDocument, error) {
	contentMap, err := typedContentMap(item.Content)
	if err != nil {
		return parentDocument{}, err
	}
	return parentDocument{
		WorkspaceID: item.WorkspaceID, Type: item.Type, Status: item.Status,
		WorkingTitle: item.WorkingTitle, NormalizedWorkingTitle: item.SearchableWorkingTitle,
		Revision: item.Revision, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		ExpiresAt: item.ExpiresAt, ArchivedAt: item.ArchivedAt, Content: contentMap,
	}, nil
}

func typedContentMap(value any) (map[string]any, error) {
	switch content := value.(type) {
	case YouTubeContent:
		return map[string]any{
			"topic": content.Topic, "icp": content.ICP, "angle": content.Angle,
			"cta": content.CTA, "publishing_title": content.PublishingTitle,
			"description": content.Description, "transcript": content.Transcript,
		}, nil
	case LinkedInContent:
		return map[string]any{"body": content.Body}, nil
	case XContent:
		return map[string]any{"body": content.Body}, nil
	case InstagramContent:
		return map[string]any{"script": content.Script}, nil
	case TikTokContent:
		return map[string]any{"script": content.Script}, nil
	case EmailContent:
		return map[string]any{"subject": content.Subject, "body": content.Body}, nil
	case SubstackContent:
		return map[string]any{"headline": content.Headline, "subheadline": content.Subheadline, "body": content.Body}, nil
	default:
		return nil, fmt.Errorf("unsupported content type %T", value)
	}
}

func contentFromMap(contentType Type, value map[string]any) (any, error) {
	stringField := func(name string) (string, error) {
		field, ok := value[name].(string)
		if !ok {
			return "", fmt.Errorf("content field %s is not a string", name)
		}
		return field, nil
	}
	switch contentType {
	case TypeYouTube:
		topic, e1 := stringField("topic")
		icp, e2 := stringField("icp")
		angle, e3 := stringField("angle")
		cta, e4 := stringField("cta")
		title, e5 := stringField("publishing_title")
		description, e6 := stringField("description")
		transcript, e7 := stringField("transcript")
		if err := firstError(e1, e2, e3, e4, e5, e6, e7); err != nil {
			return nil, err
		}
		return YouTubeContent{Topic: topic, ICP: icp, Angle: angle, CTA: cta, PublishingTitle: title, Description: description, Transcript: transcript, Sections: []Section{}}, nil
	case TypeLinkedIn:
		body, err := stringField("body")
		return LinkedInContent{Body: body}, err
	case TypeX:
		body, err := stringField("body")
		return XContent{Body: body}, err
	case TypeInstagram:
		script, err := stringField("script")
		return InstagramContent{Script: script}, err
	case TypeTikTok:
		script, err := stringField("script")
		return TikTokContent{Script: script}, err
	case TypeEmail:
		subject, e1 := stringField("subject")
		body, e2 := stringField("body")
		return EmailContent{Subject: subject, Body: body}, firstError(e1, e2)
	case TypeSubstack:
		headline, e1 := stringField("headline")
		subheadline, e2 := stringField("subheadline")
		body, e3 := stringField("body")
		return SubstackContent{Headline: headline, Subheadline: subheadline, Body: body}, firstError(e1, e2, e3)
	default:
		return nil, fmt.Errorf("invalid discriminator %q", contentType)
	}
}

func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}

func validateEncodedSizes(item Item) error {
	document, err := toParentDocument(item)
	if err != nil {
		return problem(400, "invalid_discriminator")
	}
	parentSize, err := encodedFirestoreSize(document)
	if err != nil {
		return &Error{Status: 500, Code: "content_encoding_failed", Cause: err}
	}
	if parentSize > MaxParentDocumentBytes {
		return problem(413, "content_document_too_large")
	}
	if youtube, ok := item.Content.(YouTubeContent); ok {
		for _, section := range youtube.Sections {
			size, err := encodedFirestoreSize(sectionDocument{Position: section.Position, Title: section.Title, Body: section.Body, WorkspaceID: item.WorkspaceID, ExpiresAt: item.ExpiresAt})
			if err != nil {
				return &Error{Status: 500, Code: "content_encoding_failed", Cause: err}
			}
			if size > MaxFirestoreBytes {
				return problem(413, "section_document_too_large")
			}
		}
	}
	return nil
}

func validateReceiptSize(receipt Receipt) error {
	size, err := encodedFirestoreSize(receiptToDocument(receipt))
	if err != nil {
		return &Error{Status: 500, Code: "content_encoding_failed", Cause: err}
	}
	if size > MaxFirestoreBytes {
		return problem(413, "receipt_document_too_large")
	}
	return nil
}

func encodedFirestoreSize(value any) (int, error) {
	fields, err := firestoreFields(reflect.ValueOf(value))
	if err != nil {
		return 0, err
	}
	return proto.Size(&firestorepb.Document{Fields: fields}), nil
}

func firestoreFields(value reflect.Value) (map[string]*firestorepb.Value, error) {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return map[string]*firestorepb.Value{}, nil
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Map {
		fields := make(map[string]*firestorepb.Value, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			name, ok := iterator.Key().Interface().(string)
			if !ok {
				return nil, fmt.Errorf("Firestore map key is not a string")
			}
			encoded, err := firestoreValue(iterator.Value())
			if err != nil {
				return nil, err
			}
			fields[name] = encoded
		}
		return fields, nil
	}
	if value.Kind() != reflect.Struct {
		return nil, fmt.Errorf("Firestore document is %s", value.Kind())
	}
	fields := make(map[string]*firestorepb.Value)
	valueType := value.Type()
	for index := 0; index < value.NumField(); index++ {
		fieldType := valueType.Field(index)
		name, options, _ := stringsCut(fieldType.Tag.Get("firestore"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = fieldType.Name
		}
		field := value.Field(index)
		if options == "omitempty" && isEmptyValue(field) {
			continue
		}
		encoded, err := firestoreValue(field)
		if err != nil {
			return nil, err
		}
		fields[name] = encoded
	}
	return fields, nil
}

func firestoreValue(value reflect.Value) (*firestorepb.Value, error) {
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return &firestorepb.Value{ValueType: &firestorepb.Value_NullValue{}}, nil
		}
		value = value.Elem()
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return &firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: timestamppb.New(value.Interface().(time.Time))}}, nil
	}
	switch value.Kind() {
	case reflect.String:
		return &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: value.String()}}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: value.Int()}}, nil
	case reflect.Bool:
		return &firestorepb.Value{ValueType: &firestorepb.Value_BooleanValue{BooleanValue: value.Bool()}}, nil
	case reflect.Map, reflect.Struct:
		fields, err := firestoreFields(value)
		if err != nil {
			return nil, err
		}
		return &firestorepb.Value{ValueType: &firestorepb.Value_MapValue{MapValue: &firestorepb.MapValue{Fields: fields}}}, nil
	case reflect.Slice, reflect.Array:
		values := make([]*firestorepb.Value, value.Len())
		for index := range values {
			encoded, err := firestoreValue(value.Index(index))
			if err != nil {
				return nil, err
			}
			values[index] = encoded
		}
		return &firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: &firestorepb.ArrayValue{Values: values}}}, nil
	default:
		return nil, fmt.Errorf("unsupported Firestore value %s", value.Kind())
	}
}

func stringsCut(value, separator string) (string, string, bool) {
	for index := 0; index+len(separator) <= len(value); index++ {
		if value[index:index+len(separator)] == separator {
			return value[:index], value[index+len(separator):], true
		}
	}
	return value, "", false
}

func isEmptyValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return value.IsNil()
	case reflect.String, reflect.Array:
		return value.Len() == 0
	case reflect.Bool:
		return !value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	default:
		return value.IsZero()
	}
}
