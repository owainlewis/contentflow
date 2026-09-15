package content_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/contentflow/apps/api/internal/content"
)

func TestWeeklyRhythmHTTP(t *testing.T) {
	api, token := newContentAPI(t)
	path := "/api/v1/content/rhythm"
	if response := performAPI(api, "", http.MethodGet, path, ""); response.Code != 401 {
		t.Fatalf("anonymous read: %d", response.Code)
	}
	response := performAPI(api, token, http.MethodGet, path, "")
	var rhythm content.WeeklyRhythm
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rhythm) != nil {
		t.Fatalf("read: %d %s", response.Code, response.Body.String())
	}
	rhythm.Targets[content.TypeYouTube] = 2
	body, _ := json.Marshal(rhythm)
	for range 2 {
		response = performAPI(api, token, http.MethodPut, path, string(body))
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"revision":1`) {
			t.Fatalf("save/retry: %d %s", response.Code, response.Body.String())
		}
	}
	for _, invalid := range []string{
		`{}`, `{"revision":0,"targets":{}}`,
		strings.Replace(string(body), `"youtube":2`, `"youtube":-1`, 1),
		strings.Replace(string(body), `"youtube":2`, `"youtube":36`, 1),
		strings.Replace(string(body), `"youtube":2`, `"youtube":1.5`, 1),
		strings.Replace(string(body), `"youtube":2`, `"youtube":null`, 1),
		strings.Replace(string(body), `"youtube":2`, `"unknown":2`, 1),
		strings.Replace(string(body), `"revision":0`, `"extra":true,"revision":0`, 1),
	} {
		response = performAPI(api, token, http.MethodPut, path, invalid)
		if response.Code != 400 {
			t.Fatalf("invalid rhythm returned %d: %s", response.Code, invalid)
		}
	}
	rhythm.Targets[content.TypeYouTube] = 3
	body, _ = json.Marshal(rhythm)
	response = performAPI(api, token, http.MethodPut, path, string(body))
	if response.Code != 409 {
		t.Fatalf("stale write: %d %s", response.Code, response.Body.String())
	}
}
