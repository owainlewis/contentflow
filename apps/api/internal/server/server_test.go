package server

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

type fakeChecker map[string]error

func (f fakeChecker) Check(context.Context) map[string]error {
	return f
}

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":    {Data: []byte(`<!doctype html><title>ContentFlow</title><meta property="og:image" content="__CONTENTFLOW_SOCIAL_IMAGE__"><meta name="twitter:image" content="__CONTENTFLOW_SOCIAL_IMAGE__"><div id=root></div>`)},
		"assets/app.js": {Data: []byte("console.log('ContentFlow')")},
	}
}

func TestHealthEndpointsReportLiveAndDependencyState(t *testing.T) {
	t.Parallel()

	api := NewAPI(fakeChecker{"assets": nil, "firestore": io.EOF})
	application := NewApplication(testAssets(), api)

	live := httptest.NewRecorder()
	application.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"status":"live"`) {
		t.Fatalf("unexpected live response: %d %s", live.Code, live.Body.String())
	}

	ready := httptest.NewRecorder()
	application.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"firestore":"unavailable"`) {
		t.Fatalf("unexpected ready response: %d %s", ready.Code, ready.Body.String())
	}
}

func TestApplicationServesAssetsAndFallsBackForSPARoutes(t *testing.T) {
	t.Parallel()

	application := NewApplication(testAssets(), NewAPI(fakeChecker{"assets": nil}))

	for _, route := range []string{"/", "/content/01ABC"} {
		response := httptest.NewRecorder()
		application.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ContentFlow") {
			t.Fatalf("route %s did not serve the SPA: %d %s", route, response.Code, response.Body.String())
		}
	}

	for _, route := range []string{"/assets/missing.js", "/assets/missing", "/assets"} {
		missingAsset := httptest.NewRecorder()
		application.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, route, nil))
		if missingAsset.Code != http.StatusNotFound {
			t.Fatalf("missing static route %s returned %d", route, missingAsset.Code)
		}
	}

	apiRoute := httptest.NewRecorder()
	application.ServeHTTP(apiRoute, httptest.NewRequest(http.MethodGet, "/api/v1/content", nil))
	if apiRoute.Code != http.StatusNotFound || !strings.Contains(apiRoute.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("API route fell through to the SPA: %d %s", apiRoute.Code, apiRoute.Body.String())
	}

	invalidHealth := httptest.NewRecorder()
	application.ServeHTTP(invalidHealth, httptest.NewRequest(http.MethodGet, "/health/ready/", nil))
	if invalidHealth.Code != http.StatusNotFound || !strings.Contains(invalidHealth.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("invalid health route fell through to the SPA: %d %s", invalidHealth.Code, invalidHealth.Body.String())
	}
}

func TestApplicationInjectsOriginQualifiedSocialImages(t *testing.T) {
	t.Parallel()

	application := NewApplication(testAssets(), NewAPI(fakeChecker{"assets": nil}))
	request := httptest.NewRequest(http.MethodGet, "http://internal/content/01ABC", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "contentflow.example")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)

	const socialImage = "https://contentflow.example/og.png"
	if response.Code != http.StatusOK {
		t.Fatalf("nested SPA route returned %d", response.Code)
	}
	if count := strings.Count(response.Body.String(), socialImage); count != 2 {
		t.Fatalf("expected both social image tags to use %s, found %d", socialImage, count)
	}
	if strings.Contains(response.Body.String(), socialImagePlaceholder) {
		t.Fatal("social image placeholder was not replaced")
	}
}

func TestPrivateLocalAPIRequiresGeneratedProxySecret(t *testing.T) {
	t.Parallel()

	private := RequireProxySecret(NewAPI(fakeChecker{"assets": nil}), "generated-secret")

	unauthorized := httptest.NewRecorder()
	private.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("direct private request returned %d", unauthorized.Code)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	authorizedRequest.Header.Set(proxySecretHeader, "generated-secret")
	authorized := httptest.NewRecorder()
	private.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authenticated proxy request returned %d", authorized.Code)
	}
}

func TestLocalPublicApplicationInjectsProxySecret(t *testing.T) {
	t.Parallel()

	private := httptest.NewServer(RequireProxySecret(NewAPI(fakeChecker{"assets": nil}), "generated-secret"))
	defer private.Close()
	privateURL, err := url.Parse(private.URL)
	if err != nil {
		t.Fatal(err)
	}
	public := NewLocalPublicApplication(testAssets(), privateURL, "generated-secret")

	response := httptest.NewRecorder()
	public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("public proxy returned %d: %s", response.Code, response.Body.String())
	}
}
