package auth

import (
	"net/url"
	"testing"

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
