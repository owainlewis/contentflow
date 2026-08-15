package main

import "testing"

func TestAuthenticationURLsCanonicalizeOAuthRedirect(t *testing.T) {
	publicOrigin, redirectURL, err := authenticationURLs("https://CONTENTFLOW.EXAMPLE:0443/")
	if err != nil {
		t.Fatal(err)
	}
	if publicOrigin != "https://contentflow.example" {
		t.Fatalf("canonical public origin is %q", publicOrigin)
	}
	if redirectURL != "https://contentflow.example/api/v1/auth/callback" {
		t.Fatalf("OAuth redirect URL is %q", redirectURL)
	}
}
