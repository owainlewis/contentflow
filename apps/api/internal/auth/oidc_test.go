package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func TestOIDCAuthorizationResponseModeKeepsProductionCredentialsOutOfURLs(t *testing.T) {
	for _, test := range []struct {
		name     string
		formPost bool
		wantMode string
	}{
		{name: "HTTPS production", formPost: true, wantMode: "form_post"},
		{name: "HTTP loopback development", formPost: false, wantMode: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			redirectScheme := "http"
			if test.formPost {
				redirectScheme = "https"
			}
			formPost, err := formPostForRedirect(redirectScheme + "://contentflow.example/api/v1/auth/callback")
			if err != nil {
				t.Fatal(err)
			}
			provider := &OIDCProvider{
				oauth:    oauth2.Config{ClientID: "client", RedirectURL: "https://contentflow.example/api/v1/auth/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/authorize"}},
				formPost: formPost,
			}
			authorizationURL, err := url.Parse(provider.AuthorizationURL("state", "challenge"))
			if err != nil {
				t.Fatal(err)
			}
			if got := authorizationURL.Query().Get("response_mode"); got != test.wantMode {
				t.Fatalf("response_mode is %q, want %q", got, test.wantMode)
			}
		})
	}
}

func TestOIDCTokenExchangeHasAnInternalDeadline(t *testing.T) {
	releaseEndpoint := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseEndpoint) }) }
	tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-releaseEndpoint:
		}
	}))
	defer func() {
		release()
		tokenEndpoint.Close()
	}()

	provider := &OIDCProvider{
		oauth: oauth2.Config{
			ClientID:     "client",
			ClientSecret: "secret",
			Endpoint:     oauth2.Endpoint{TokenURL: tokenEndpoint.URL},
		},
	}
	type exchangeResult struct {
		err     error
		elapsed time.Duration
	}
	started := time.Now()
	resultChannel := make(chan exchangeResult, 1)
	go func() {
		_, err := provider.exchangeIdentityWithTimeout(context.Background(), "code", "verifier", 50*time.Millisecond)
		resultChannel <- exchangeResult{err: err, elapsed: time.Since(started)}
	}()
	var result exchangeResult
	select {
	case result = <-resultChannel:
	case <-time.After(time.Second):
		release()
		<-resultChannel
		t.Fatal("stalled OAuth exchange exceeded its internal deadline")
	}
	err := result.err
	if err == nil || !strings.Contains(err.Error(), "exchange authorization code") {
		t.Fatalf("stalled OAuth exchange returned %v", err)
	}
	if result.elapsed > time.Second {
		t.Fatalf("stalled OAuth exchange took %s", result.elapsed)
	}
}

func TestOIDCDiscoveryRejectsPlaintextEndpointsForHTTPSIssuer(t *testing.T) {
	for _, endpointName := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		t.Run(endpointName, func(t *testing.T) {
			var metadata map[string]string
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/.well-known/openid-configuration" {
					http.NotFound(response, request)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(response).Encode(metadata); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			metadata = map[string]string{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
				"jwks_uri":               server.URL + "/keys",
			}
			metadata[endpointName] = "http://credentials.example/" + endpointName
			ctx := coreoidc.ClientContext(context.Background(), server.Client())
			if _, err := NewOIDCProvider(ctx, server.URL, "client", "secret", "https://contentflow.example/api/v1/auth/callback"); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
				t.Fatalf("plaintext %s returned %v", endpointName, err)
			}
		})
	}
}

func TestOIDCDiscoveryAllowsExplicitLoopbackHTTPDevelopment(t *testing.T) {
	var metadata map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(metadata); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	metadata = map[string]string{
		"issuer":                 server.URL,
		"authorization_endpoint": server.URL + "/authorize",
		"token_endpoint":         server.URL + "/token",
		"jwks_uri":               server.URL + "/keys",
	}
	if _, err := NewOIDCProvider(context.Background(), server.URL, "client", "secret", "http://localhost:3000/api/v1/auth/callback"); err != nil {
		t.Fatalf("loopback HTTP discovery failed: %v", err)
	}
}
