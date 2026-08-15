package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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
