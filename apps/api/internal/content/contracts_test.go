package content

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAPIContainsTypedReplacementAndSummaryContracts(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "openapi", "v1.yaml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contract := string(contents)
	for _, required := range []string{
		"openapi: 3.1.0", "/content/{id}/transcript:", "transcript_missing", "ReplaceContentRequest:",
		"unevaluatedProperties: false", "x-max-utf8-bytes: 512000", "Summary-only results",
		"JSON over 1 MiB", "parent document over 900 KiB", "maxItems: 200",
	} {
		if !strings.Contains(contract, required) {
			t.Fatalf("OpenAPI is missing %q", required)
		}
	}
	for _, contentType := range []Type{TypeYouTube, TypeLinkedIn, TypeX, TypeInstagram, TypeTikTok, TypeEmail, TypeSubstack} {
		if !strings.Contains(contract, "const: "+string(contentType)) {
			t.Fatalf("OpenAPI lacks strict %s discriminator", contentType)
		}
	}
	if strings.Contains(contract, "/content/batches:") {
		t.Fatal("out-of-scope batch endpoint is documented")
	}
	if !strings.Contains(contract, "required: [transcript]") || strings.Contains(contract, "required: [topic, icp, angle, cta, publishing_title, description, transcript, sections]") {
		t.Fatal("OpenAPI does not match optional YouTube replacement fields")
	}
	if !strings.Contains(contract, "required: [position]") || strings.Contains(contract, "required: [position, title, body]") {
		t.Fatal("OpenAPI does not match optional section text replacement fields")
	}
}

func TestFirestoreIndexesCoverSupportedQueriesAndExcludeLongText(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "firestore.indexes.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		Indexes []struct {
			CollectionGroup string `json:"collectionGroup"`
			Fields          []struct {
				FieldPath string `json:"fieldPath"`
			} `json:"fields"`
		} `json:"indexes"`
		Overrides []struct {
			CollectionGroup string `json:"collectionGroup"`
			FieldPath       string `json:"fieldPath"`
			Indexes         []any  `json:"indexes"`
		} `json:"fieldOverrides"`
	}
	if err := json.Unmarshal(contents, &configuration); err != nil {
		t.Fatal(err)
	}
	wantIndexes := [][]string{{"workspace_id", "expires_at", "updated_at"}, {"workspace_id", "type", "expires_at"}, {"workspace_id", "status", "expires_at"}, {"workspace_id", "normalized_working_title", "expires_at"}}
	if len(configuration.Indexes) != len(wantIndexes) {
		t.Fatalf("found %d indexes, want %d", len(configuration.Indexes), len(wantIndexes))
	}
	for index, want := range wantIndexes {
		got := make([]string, len(configuration.Indexes[index].Fields))
		for fieldIndex, field := range configuration.Indexes[index].Fields {
			got[fieldIndex] = field.FieldPath
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("index %d is %v, want %v", index, got, want)
		}
	}
	wantExemptions := map[string]bool{"content.transcript": false, "content.description": false, "content.body": false, "content.script": false, "body": false}
	for _, override := range configuration.Overrides {
		if len(override.Indexes) != 0 {
			continue
		}
		if _, tracked := wantExemptions[override.FieldPath]; tracked {
			wantExemptions[override.FieldPath] = true
		}
	}
	for field, found := range wantExemptions {
		if !found {
			t.Fatalf("long text field %s remains indexed", field)
		}
	}
}
