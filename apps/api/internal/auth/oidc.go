package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oauthExchangeTimeout = 10 * time.Second

type OIDCProvider struct {
	issuer   string
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	formPost bool
}

func NewOIDCProvider(ctx context.Context, issuer, clientID, clientSecret, redirectURL string) (*OIDCProvider, error) {
	return newOIDCProvider(ctx, issuer, clientID, clientSecret, redirectURL, oauthExchangeTimeout)
}

func newOIDCProvider(ctx context.Context, issuer, clientID, clientSecret, redirectURL string, networkTimeout time.Duration) (*OIDCProvider, error) {
	formPost, err := formPostForRedirect(redirectURL)
	if err != nil {
		return nil, err
	}
	ctx = boundedOIDCClientContext(ctx, networkTimeout)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OAuth issuer: %w", err)
	}
	endpoint := provider.Endpoint()
	var metadata struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("read OAuth discovery metadata: %w", err)
	}
	for name, rawURL := range map[string]string{
		"authorization": endpoint.AuthURL,
		"token":         endpoint.TokenURL,
		"signing keys":  metadata.JWKSURL,
	} {
		if err := validateDiscoveredEndpoint(issuer, name, rawURL); err != nil {
			return nil, err
		}
	}
	return &OIDCProvider{
		issuer: issuer,
		oauth: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{oidc.ScopeOpenID},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		formPost: formPost,
	}, nil
}

func boundedOIDCClientContext(ctx context.Context, timeout time.Duration) context.Context {
	client := &http.Client{Timeout: timeout}
	if configured, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		copy := *configured
		if copy.Timeout == 0 || copy.Timeout > timeout {
			copy.Timeout = timeout
		}
		client = &copy
	}
	return oidc.ClientContext(ctx, client)
}

func validateDiscoveredEndpoint(issuer, name, rawURL string) error {
	issuerURL, issuerErr := url.Parse(issuer)
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return fmt.Errorf("valid OAuth %s endpoint is required", name)
	}
	if port := endpoint.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("valid OAuth %s endpoint is required", name)
		}
	}
	if issuerErr == nil && issuerURL.Scheme == "https" {
		if endpoint.Scheme == "https" {
			return nil
		}
		return fmt.Errorf("OAuth %s endpoint must use HTTPS", name)
	}
	if issuerErr == nil && issuerURL.Scheme == "http" && isLoopbackHostname(issuerURL.Hostname()) {
		if endpoint.Scheme == "http" && isLoopbackHostname(endpoint.Hostname()) {
			return nil
		}
		return fmt.Errorf("OAuth %s endpoint must use loopback HTTP", name)
	}
	return fmt.Errorf("valid OAuth issuer is required")
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
	return p.exchangeIdentityWithTimeout(ctx, code, codeVerifier, oauthExchangeTimeout)
}

func (p *OIDCProvider) exchangeIdentityWithTimeout(ctx context.Context, code, codeVerifier string, timeout time.Duration) (Identity, error) {
	exchangeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	token, err := p.oauth.Exchange(exchangeContext, code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Identity{}, fmt.Errorf("OAuth response omitted ID token")
	}
	idToken, err := p.verifier.Verify(exchangeContext, rawIDToken)
	if err != nil {
		return Identity{}, fmt.Errorf("verify ID token: %w", err)
	}
	return Identity{Issuer: p.issuer, Subject: idToken.Subject}, nil
}
