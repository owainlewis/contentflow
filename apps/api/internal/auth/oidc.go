package auth

import (
	"context"
	"fmt"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCProvider struct {
	issuer   string
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	formPost bool
}

func NewOIDCProvider(ctx context.Context, issuer, clientID, clientSecret, redirectURL string) (*OIDCProvider, error) {
	formPost, err := formPostForRedirect(redirectURL)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OAuth issuer: %w", err)
	}
	return &OIDCProvider{
		issuer: issuer,
		oauth: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       []string{oidc.ScopeOpenID},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		formPost: formPost,
	}, nil
}

func formPostForRedirect(redirectURL string) (bool, error) {
	redirect, err := url.Parse(redirectURL)
	if err != nil || redirect.Host == "" || (redirect.Scheme != "http" && redirect.Scheme != "https") {
		return false, fmt.Errorf("valid OAuth redirect URL is required")
	}
	return redirect.Scheme == "https", nil
}

func (p *OIDCProvider) AuthorizationURL(state, codeChallenge string) string {
	options := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	if p.formPost {
		options = append(options, oauth2.SetAuthURLParam("response_mode", "form_post"))
	}
	return p.oauth.AuthCodeURL(state, options...)
}

func (p *OIDCProvider) ExchangeIdentity(ctx context.Context, code, codeVerifier string) (Identity, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Identity{}, fmt.Errorf("OAuth response omitted ID token")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Identity{}, fmt.Errorf("verify ID token: %w", err)
	}
	return Identity{Issuer: p.issuer, Subject: idToken.Subject}, nil
}
