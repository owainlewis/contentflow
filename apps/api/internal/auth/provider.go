package auth

import "context"

type Identity struct {
	Issuer  string
	Subject string
}

type OAuthProvider interface {
	AuthorizationURL(state, codeChallenge string) string
	ExchangeIdentity(context.Context, string, string) (Identity, error)
}
