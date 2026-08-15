package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
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

func TestOIDCJWKSFetchIsBoundedAndCanRecover(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatal(err)
	}

	firstJWKSStarted := make(chan struct{})
	firstJWKSCancelled := make(chan struct{})
	var jwksRequests int
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			response.Header().Set("Content-Type", "application/json")
			json.NewEncoder(response).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/token":
			now := time.Now()
			rawIDToken, signErr := jwt.Signed(signer).Claims(jwt.Claims{
				Issuer: issuer, Subject: "owner-subject", Audience: jwt.Audience{"client"},
				IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
			}).Serialize()
			if signErr != nil {
				t.Error(signErr)
				return
			}
			response.Header().Set("Content-Type", "application/json")
			json.NewEncoder(response).Encode(map[string]any{
				"access_token": "access", "token_type": "Bearer", "id_token": rawIDToken,
			})
		case "/keys":
			jwksRequests++
			if jwksRequests == 1 {
				close(firstJWKSStarted)
				<-request.Context().Done()
				close(firstJWKSCancelled)
				return
			}
			response.Header().Set("Content-Type", "application/json")
			json.NewEncoder(response).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
				Key: &privateKey.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig",
			}}})
		default:
			http.NotFound(response, request)
		}
	}))
	issuer = server.URL
	defer server.Close()

	clientContext := coreoidc.ClientContext(context.Background(), server.Client())
	provider, err := newOIDCProvider(clientContext, issuer, "client", "secret", "https://contentflow.example/api/v1/auth/callback", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.exchangeIdentityWithTimeout(clientContext, "code", "verifier", time.Second); err == nil || !strings.Contains(err.Error(), "verify ID token") {
		t.Fatalf("stalled signing-key request returned %v", err)
	}
	select {
	case <-firstJWKSStarted:
	default:
		t.Fatal("signing-key request did not start")
	}
	select {
	case <-firstJWKSCancelled:
	case <-time.After(time.Second):
		t.Fatal("stalled signing-key request was not cancelled")
	}

	identity, err := provider.exchangeIdentityWithTimeout(clientContext, "code", "verifier", time.Second)
	if err != nil {
		t.Fatalf("verification did not recover after signing-key timeout: %v", err)
	}
	if identity.Issuer != issuer || identity.Subject != "owner-subject" {
		t.Fatalf("recovered identity is %#v", identity)
	}
}

func TestOIDCDiscoveryRejectsPlaintextEndpointsForHTTPSIssuer(t *testing.T) {
	invalidEndpoints := map[string]string{
		"plaintext":           "http://credentials.example/endpoint",
		"missing hostname":    "https://:443/endpoint",
		"embedded credential": "https://user:secret@credentials.example/endpoint",
		"invalid port":        "https://credentials.example:70000/endpoint",
		"malformed":           "https://credentials.example/%zz",
	}
	for _, endpointName := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		for invalidName, invalidURL := range invalidEndpoints {
			t.Run(endpointName+"/"+invalidName, func(t *testing.T) {
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
				metadata[endpointName] = invalidURL
				ctx := coreoidc.ClientContext(context.Background(), server.Client())
				if _, err := NewOIDCProvider(ctx, server.URL, "client", "secret", "https://contentflow.example/api/v1/auth/callback"); err == nil {
					t.Fatalf("invalid %s %s was accepted", endpointName, invalidURL)
				}
			})
		}
	}
}

func TestOIDCDiscoveryRejectsRemoteEndpointsForLoopbackIssuer(t *testing.T) {
	for _, endpointName := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		t.Run(endpointName, func(t *testing.T) {
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
			metadata[endpointName] = "https://remote.example/endpoint"
			if _, err := NewOIDCProvider(context.Background(), server.URL, "client", "secret", "http://localhost:3000/api/v1/auth/callback"); err == nil || !strings.Contains(err.Error(), "loopback HTTP") {
				t.Fatalf("remote loopback-development %s returned %v", endpointName, err)
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
