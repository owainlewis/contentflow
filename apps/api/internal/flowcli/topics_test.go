package flowcli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTopicAndResourceResponses(t *testing.T) {
	base := `"id":"01J00000000000000000000001","status":"draft","working_title":"Topic","revision":1,"created_at":"2026-09-19T12:00:00Z","updated_at":"2026-09-19T12:00:00Z","expires_at":"9999-12-31T00:00:00Z"`
	for _, kind := range []string{"topic", "instagram"} {
		t.Run(kind, func(t *testing.T) {
			metadata := `"format":"reel","topic_id":"01J00000000000000000000002","document_url":"https://docs.google.com/script","video_url":"https://frame.io/reel","scheduled_at":"2026-09-20T12:00:00Z"`
			content := `{"script":"script","caption":"caption"}`
			if kind == "topic" {
				metadata = `"document_url":"https://docs.google.com/source"`
				content = `{"source":"One idea","source_url":"https://docs.google.com/source"}`
			}
			raw := []byte(`{` + base + `,"type":"` + kind + `",` + metadata + `,"content":` + content + `}`)
			var output bytes.Buffer
			if err := writeItemJSONForID(&output, raw, ""); err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			_ = json.Unmarshal(raw, &want)
			_ = json.Unmarshal(output.Bytes(), &got)
			for _, field := range []string{"type", "topic_id", "format", "document_url", "video_url", "scheduled_at"} {
				if got[field] != want[field] {
					t.Fatalf("lost %s: %s", field, output.String())
				}
			}
			output.Reset()
			if err := renderItem(&output, raw); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "https://docs.google.com/") {
				t.Fatal("human item omitted document")
			}
			list := `{"items":[{` + base + `,"type":"` + kind + `",` + metadata + `,"asset_counts":{}}]}`
			output.Reset()
			if err := writeListJSONStream(&output, strings.NewReader(list)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "document_url") {
				t.Fatal("list lost metadata")
			}
			output.Reset()
			if err := renderList(&output, []byte(list)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "https://docs.google.com/") {
				t.Fatal("human list omitted document")
			}
		})
	}
}

func TestCLIRepurposingLifecycle(t *testing.T) {
	fixture := newAPIFixture(t)
	client := fixture.server.Client()
	run := func(args ...string) invocation { return invoke(t, fixture.server.URL, fullToken, client, "", args...) }

	create := func(name, body string) string {
		result := run("content", "create", "--file", writeTestFile(t, name, body), "--json")
		if result.exitCode != ExitSuccess {
			t.Fatalf("create failed %#v", result)
		}
		return parseMutation(t, result.stdout).ItemIDs[0]
	}
	topicID := create("topic.json", `{"type":"topic","working_title":"Repurpose","status":"draft","content":{"source":"One idea","source_url":"https://docs.google.com/source"}}`)
	metadata := `"topic_id":"` + topicID + `","format":"reel","document_url":"https://docs.google.com/script","video_url":"https://frame.io/video","scheduled_at":"2026-09-20T12:00:00Z"`
	body := `{"type":"instagram","working_title":"Reel","status":"draft",` + metadata + `,"content":{"script":"script","caption":"caption"}}`
	pieceID := create("reel.json", body)
	for _, jsonMode := range []bool{false, true} {
		suffix := []string{}
		if jsonMode {
			suffix = append(suffix, "--json")
		}
		for _, id := range []string{topicID, pieceID} {
			result := run(append([]string{"content", "show", id}, suffix...)...)
			if result.exitCode != ExitSuccess || !strings.Contains(result.stdout, "https://docs.google.com/") {
				t.Fatalf("show failed %#v", result)
			}
		}
		result := run(append([]string{"content", "list", "--type", "topic"}, suffix...)...)
		if result.exitCode != ExitSuccess || !strings.Contains(result.stdout, topicID) || strings.Contains(result.stdout, pieceID) {
			t.Fatalf("topic filter failed %#v", result)
		}
		result = run(append([]string{"content", "list", "--type", "instagram"}, suffix...)...)
		if result.exitCode != ExitSuccess || !strings.Contains(result.stdout, "https://frame.io/video") {
			t.Fatalf("resource list failed %#v", result)
		}
	}
	update := strings.Replace(body, `"status":"draft"`, `"revision":1,"status":"ready"`, 1)
	update = strings.Replace(update, `"caption":"caption"`, `"caption":"updated caption"`, 1)
	result := run("content", "update", pieceID, "--file", writeTestFile(t, "update.json", update), "--json")
	if result.exitCode != ExitSuccess {
		t.Fatalf("update failed %#v", result)
	}
	result = run("content", "show", pieceID, "--json")
	for _, want := range []string{`"caption":"updated caption"`, `"topic_id":"` + topicID + `"`, `"format":"reel"`, `"document_url":"https://docs.google.com/script"`, `"video_url":"https://frame.io/video"`, `"scheduled_at":"2026-09-20T12:00:00Z"`} {
		if !strings.Contains(result.stdout, want) {
			t.Fatalf("update lost %s: %#v", want, result)
		}
	}
	result = run("content", "batch-create", "--file", writeTestFile(t, "batch.json", `{"items":[`+body+`]}`), "--json")
	if result.exitCode != ExitSuccess {
		t.Fatalf("batch failed %#v", result)
	}
	result = run("content", "show", parseMutation(t, result.stdout).ItemIDs[0], "--json")
	if result.exitCode != ExitSuccess || !strings.Contains(result.stdout, `"topic_id":"`+topicID+`"`) || !strings.Contains(result.stdout, `"caption":"caption"`) {
		t.Fatalf("batch lost fields %#v", result)
	}

}

func TestResourceResponseValidationBounds(t *testing.T) {
	base := `"id":"01J00000000000000000000001","type":"instagram","status":"draft","working_title":"Reel","revision":1,"created_at":"2026-09-19T12:00:00Z","updated_at":"2026-09-19T12:00:00Z","expires_at":"9999-12-31T00:00:00Z"`
	badFields := []string{
		`"topic_id":"invalid"`, `"topic_id":null`, `"format":null`, `"video_url":null`, `"document_url":null`, `"scheduled_at":null`,
		`"scheduled_at":"0001-01-01T00:00:00Z"`, `"video_url":"javascript:alert(1)"`, `"document_url":"file:///tmp/source"`,
		`"video_url":"https://user:password@frame.io/video"`, `"format":"` + strings.Repeat("x", 101) + `"`,
		`"document_url":"https://example.com/` + strings.Repeat("x", 8192) + `"`,
	}
	for _, fields := range badFields {
		raw := []byte(`{` + base + `,` + fields + `,"content":{"script":"script","caption":"caption"}}`)
		if _, err := decodeItemResponse(raw); err == nil {
			t.Errorf("accepted invalid item field %.80s", fields)
		}
		list := `{"items":[{` + base + `,` + fields + `,"asset_counts":{}}]}`
		if err := decodeListStream(strings.NewReader(list), func(summary) error { return nil }); err == nil {
			t.Errorf("accepted invalid summary field %.80s", fields)
		}
	}
	for _, test := range []struct{ kind, body string }{
		{"topic", `{"source_url":"javascript:alert(1)"}`},
		{"topic", `{"source":null}`},
		{"topic", `{"source":"` + strings.Repeat("x", (500<<10)+1) + `"}`},
		{"instagram", `{"caption":null}`},
		{"instagram", `{"caption":"` + strings.Repeat("x", (500<<10)+1) + `"}`},
		{"instagram", `{"caption":"ok","unknown":"bad"}`},
		{"tiktok", `{"caption":"wrong type"}`},
	} {
		if validItemContent(test.kind, []byte(test.body)) {
			t.Errorf("accepted invalid %s content", test.kind)
		}
	}
}
