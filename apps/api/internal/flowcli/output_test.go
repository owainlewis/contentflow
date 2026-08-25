package flowcli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHumanOutputGoldens(t *testing.T) {
	tests := []struct {
		name   string
		render func(*bytes.Buffer, []byte) error
		body   string
		golden string
	}{
		{
			name:   "list",
			render: func(output *bytes.Buffer, body []byte) error { return renderList(output, body) },
			body:   `{"items":[{"id":"01J00000000000000000000001","type":"youtube","status":"draft","working_title":"A useful title","revision":2,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","asset_counts":{}},{"id":"01J00000000000000000000002","type":"x","status":"ready","working_title":"Tabs\tand\nlines\u001b[31mred","revision":7,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00Z","expires_at":"2026-10-10T09:00:00Z","asset_counts":{"image":2,"video":1}}]}`,
			golden: "list.golden",
		},
		{
			name:   "item",
			render: func(output *bytes.Buffer, body []byte) error { return renderItem(output, body) },
			body:   `{"id":"01J00000000000000000000001","type":"youtube","status":"draft","working_title":"A useful\ntitle\u001b[31mred","revision":2,"created_at":"2026-08-15T09:00:00Z","updated_at":"2026-08-15T10:00:00.123456Z","expires_at":"2026-10-10T09:00:00Z","content":{"topic":"Topic","icp":"ICP","angle":"Angle","cta":"CTA","publishing_title":"Published title","description":"Description","sections":[],"transcript":"Exact transcript"}}`,
			golden: "item.golden",
		},
		{
			name:   "mutation",
			render: func(output *bytes.Buffer, body []byte) error { return renderMutation(output, body) },
			body:   `{"operation_id":"01J00000000000000000000009","item_ids":["01J00000000000000000000001","01J00000000000000000000002"],"revisions":[1,1],"expires_at":["2026-10-10T09:00:00Z","2026-10-10T09:00:01.25Z"],"status":"created"}`,
			golden: "mutation.golden",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.render(&output, []byte(test.body)); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", test.golden))
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != string(want) {
				t.Fatalf("golden mismatch\nwant:\n%q\ngot:\n%q", string(want), output.String())
			}
		})
	}
}

func TestTranscriptHumanOutputIsByteExact(t *testing.T) {
	var output bytes.Buffer
	if err := renderTranscript(&output, []byte(`{"id":"01J00000000000000000000001","revision":1,"transcript":"line one\nline two"}`)); err != nil {
		t.Fatal(err)
	}
	if output.String() != "line one\nline two" {
		t.Fatalf("transcript changed: %q", output.String())
	}
}
