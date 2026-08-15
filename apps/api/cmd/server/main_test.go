package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/contentflow/apps/api/internal/config"
)

func TestAuthenticationURLsPreserveExplicitOAuthRedirectPorts(t *testing.T) {
	tests := []struct {
		configuredOrigin string
		publicOrigin     string
		redirectURL      string
	}{
		{"https://CONTENTFLOW.EXAMPLE:0443/", "https://contentflow.example", "https://contentflow.example:443/api/v1/auth/callback"},
		{"https://CONTENTFLOW.EXAMPLE/", "https://contentflow.example", "https://contentflow.example/api/v1/auth/callback"},
		{"https://CONTENTFLOW.EXAMPLE:008080/", "https://contentflow.example:8080", "https://contentflow.example:8080/api/v1/auth/callback"},
	}
	for _, test := range tests {
		publicOrigin, redirectURL, err := authenticationURLs(test.configuredOrigin)
		if err != nil {
			t.Fatal(err)
		}
		if publicOrigin != test.publicOrigin {
			t.Fatalf("canonical public origin for %q is %q", test.configuredOrigin, publicOrigin)
		}
		if redirectURL != test.redirectURL {
			t.Fatalf("OAuth redirect URL for %q is %q", test.configuredOrigin, redirectURL)
		}
	}
}

func TestHTTPServerBoundsRequestHeadersAndBodies(t *testing.T) {
	httpServer := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if httpServer.ReadHeaderTimeout <= 0 {
		t.Fatal("HTTP server has no header read deadline")
	}
	if httpServer.ReadTimeout != requestReadTimeout {
		t.Fatalf("HTTP server read timeout is %s, want %s", httpServer.ReadTimeout, requestReadTimeout)
	}
}

func TestRunBoundsOIDCDiscoveryBeforeStartingServers(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer issuer.Close()

	cfg := config.Config{
		Environment: "development", PublicAddress: "127.0.0.1:0", PrivateAddress: "127.0.0.1:0", AssetDirectory: t.TempDir(),
		GoogleProject: "project", PublicOrigin: "https://contentflow.example", OAuthIssuer: issuer.URL,
		OAuthClientID: "client", OAuthSecret: "secret", OwnerSubject: "owner", WorkspaceID: "workspace",
	}
	started := time.Now()
	err := run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "discover OAuth issuer") {
		t.Fatalf("stalled OIDC discovery returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > oidcDiscoveryTimeout+2*time.Second {
		t.Fatalf("stalled OIDC discovery took %s", elapsed)
	}
}
