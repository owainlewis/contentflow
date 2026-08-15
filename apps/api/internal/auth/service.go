package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/net/idna"
)

type Scope string

const (
	ScopeContentRead  Scope = "content:read"
	ScopeContentWrite Scope = "content:write"
	ScopeAssetsWrite  Scope = "assets:write"

	loginCookieName   = "contentflow_oauth"
	sessionCookieName = "contentflow_session"
)

var allowedScopes = []Scope{ScopeContentRead, ScopeContentWrite, ScopeAssetsWrite}

type Config struct {
	PublicOrigin  string
	OwnerIssuer   string
	OwnerSubject  string
	WorkspaceID   string
	CredentialKey []byte
}

type Service struct {
	config        Config
	provider      OAuthProvider
	store         Store
	now           func() time.Time
	random        func([]byte) (int, error)
	entropy       io.Reader
	ulidMu        sync.Mutex
	sealer        cipher.AEAD
	secureCookies bool
}

type Principal struct {
	WorkspaceID string
	Kind        string
	Scopes      []Scope
	CSRFToken   string
}

type principalKey struct{}

func New(config Config, provider OAuthProvider, store Store) (*Service, error) {
	canonicalOrigin, origin, err := normalizeOrigin(config.PublicOrigin)
	if err != nil {
		return nil, fmt.Errorf("valid public origin is required")
	}
	if origin.Scheme == "http" && !isLoopbackHostname(origin.Hostname()) {
		return nil, fmt.Errorf("authenticated HTTP origins are allowed only on loopback")
	}
	config.PublicOrigin = canonicalOrigin
	if config.OwnerIssuer == "" || config.OwnerSubject == "" || config.WorkspaceID == "" {
		return nil, fmt.Errorf("owner issuer, subject, and workspace are required")
	}
	if provider == nil || store == nil {
		return nil, fmt.Errorf("OAuth provider and auth store are required")
	}
	if len(config.CredentialKey) != 32 {
		return nil, fmt.Errorf("32-byte credential encryption key is required")
	}
	block, err := aes.NewCipher(config.CredentialKey)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	sealer, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential sealer: %w", err)
	}
	return &Service{
		config: config, provider: provider, store: store, now: time.Now, random: rand.Read,
		entropy: ulid.Monotonic(rand.Reader, 0), sealer: sealer, secureCookies: origin.Scheme == "https",
	}, nil
}

func (s *Service) HandleLogin(response http.ResponseWriter, request *http.Request) {
	attemptID, err := s.randomString(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	state, err := s.randomString(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	verifier, err := s.randomString(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	sealedVerifier, err := s.seal(verifier)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	attempt := LoginAttempt{ID: attemptID, State: credentialDocumentID(state), CodeVerifier: sealedVerifier, ExpiresAt: s.now().Add(10 * time.Minute)}
	if err := s.store.SaveLoginAttempt(request.Context(), attempt); err != nil {
		writeError(response, http.StatusServiceUnavailable, "authentication_unavailable")
		return
	}
	s.setCookie(response, loginCookieName, attemptID, attempt.ExpiresAt)
	challenge := sha256.Sum256([]byte(verifier))
	http.Redirect(response, request, s.provider.AuthorizationURL(state, base64.RawURLEncoding.EncodeToString(challenge[:])), http.StatusFound)
}

func (s *Service) HandleCallback(response http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie(loginCookieName)
	if err != nil || request.URL.Query().Get("code") == "" || request.URL.Query().Get("state") == "" {
		writeError(response, http.StatusUnauthorized, "oauth_state_invalid")
		return
	}
	attempt, err := s.store.TakeLoginAttempt(request.Context(), cookie.Value, credentialDocumentID(request.URL.Query().Get("state")), s.now())
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			writeError(response, http.StatusServiceUnavailable, "authentication_unavailable")
			return
		}
		writeError(response, http.StatusUnauthorized, "oauth_state_invalid")
		return
	}
	verifier, err := s.open(attempt.CodeVerifier)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "oauth_state_invalid")
		return
	}
	identity, err := s.provider.ExchangeIdentity(request.Context(), request.URL.Query().Get("code"), verifier)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "oauth_exchange_failed")
		return
	}
	if !secureEqual(identity.Issuer, s.config.OwnerIssuer) || !secureEqual(identity.Subject, s.config.OwnerSubject) {
		writeError(response, http.StatusForbidden, "owner_mismatch")
		return
	}
	sessionID, err := s.randomString(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	csrf, err := s.randomString(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "authentication_unavailable")
		return
	}
	session := Session{ID: sessionID, WorkspaceID: s.config.WorkspaceID, CSRFToken: csrf, ExpiresAt: s.now().Add(24 * time.Hour)}
	if err := s.store.SaveSession(request.Context(), session); err != nil {
		writeError(response, http.StatusServiceUnavailable, "authentication_unavailable")
		return
	}
	s.clearCookie(response, loginCookieName)
	s.setCookie(response, sessionCookieName, sessionID, session.ExpiresAt)
	http.Redirect(response, request, s.config.PublicOrigin+"/", http.StatusFound)
}

func (s *Service) HandleSession(response http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFromContext(request.Context())
	writeJSON(response, http.StatusOK, map[string]any{
		"workspace_id": principal.WorkspaceID,
		"identity":     principal.Kind,
		"csrf_token":   principal.CSRFToken,
		"scopes":       principal.Scopes,
	})
}

func (s *Service) HandleCreateToken(response http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFromContext(request.Context())
	if principal.Kind != "session" {
		writeError(response, http.StatusForbidden, "owner_session_required")
		return
	}
	var input struct {
		Scopes []Scope `json:"scopes"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF || len(input.Scopes) == 0 || !validScopes(input.Scopes) {
		writeError(response, http.StatusBadRequest, "invalid_scopes")
		return
	}
	rawBytes := make([]byte, 32)
	if _, err := s.random(rawBytes); err != nil {
		writeError(response, http.StatusInternalServerError, "token_generation_failed")
		return
	}
	raw := "cf_" + base64.RawURLEncoding.EncodeToString(rawBytes)
	id, err := s.newTokenID()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "token_generation_failed")
		return
	}
	token := Token{
		ID: id, WorkspaceID: principal.WorkspaceID, Prefix: raw[:12], Hash: sha256.Sum256([]byte(raw)),
		Scopes: slices.Clone(input.Scopes), CreatedAt: s.now(),
	}
	if err := s.store.SaveToken(request.Context(), token); err != nil {
		writeError(response, http.StatusServiceUnavailable, "token_storage_failed")
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"id": id, "prefix": token.Prefix, "token": raw, "scopes": token.Scopes})
}

func (s *Service) HandleRevokeToken(response http.ResponseWriter, request *http.Request, tokenID string) {
	principal, _ := PrincipalFromContext(request.Context())
	if principal.Kind != "session" {
		writeError(response, http.StatusForbidden, "owner_session_required")
		return
	}
	if err := s.store.RevokeToken(request.Context(), principal.WorkspaceID, tokenID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(response, http.StatusNotFound, "token_not_found")
		} else {
			writeError(response, http.StatusServiceUnavailable, "token_storage_failed")
		}
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Service) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		principal, status, code := s.authenticate(request)
		if status != 0 {
			writeError(response, status, code)
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), principalKey{}, principal)))
	})
}

func (s *Service) Authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		principal, _ := PrincipalFromContext(request.Context())
		if principal.Kind == "session" && isMutation(request.Method) {
			requestOrigin, _, err := normalizeOrigin(request.Header.Get("Origin"))
			if err != nil || !secureEqual(requestOrigin, s.config.PublicOrigin) || subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(principal.CSRFToken)) != 1 {
				writeError(response, http.StatusForbidden, "csrf_check_failed")
				return
			}
		}
		if principal.Kind == "token" {
			if isTokenAdministration(request.URL.Path) {
				writeError(response, http.StatusForbidden, "owner_session_required")
				return
			}
			required := requiredScope(request.Method, request.URL.Path)
			if required != "" && !slices.Contains(principal.Scopes, required) {
				writeError(response, http.StatusForbidden, "insufficient_scope")
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}

func (s *Service) authenticate(request *http.Request) (Principal, int, string) {
	authorization := request.Header.Get("Authorization")
	if authorization != "" {
		scheme, credential, found := strings.Cut(authorization, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") || len(strings.TrimSpace(credential)) == 0 {
			return Principal{}, http.StatusUnauthorized, "invalid_bearer_token"
		}
		raw := strings.TrimSpace(credential)
		hash := sha256.Sum256([]byte(raw))
		token, err := s.store.TokenByHash(request.Context(), hash)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return Principal{}, http.StatusServiceUnavailable, "authentication_unavailable"
			}
			return Principal{}, http.StatusUnauthorized, "invalid_bearer_token"
		}
		if !secureEqual(token.WorkspaceID, s.config.WorkspaceID) {
			return Principal{}, http.StatusUnauthorized, "invalid_bearer_token"
		}
		allowed, err := s.store.AllowTokenRequest(request.Context(), token.ID, s.now(), 120, time.Minute)
		if err != nil {
			return Principal{}, http.StatusServiceUnavailable, "authentication_unavailable"
		}
		if !allowed {
			return Principal{}, http.StatusTooManyRequests, "rate_limit_exceeded"
		}
		return Principal{WorkspaceID: token.WorkspaceID, Kind: "token", Scopes: slices.Clone(token.Scopes)}, 0, ""
	}
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return Principal{}, http.StatusUnauthorized, "authentication_required"
	}
	session, err := s.store.Session(request.Context(), cookie.Value, s.now())
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return Principal{}, http.StatusServiceUnavailable, "authentication_unavailable"
		}
		return Principal{}, http.StatusUnauthorized, "authentication_required"
	}
	if !secureEqual(session.WorkspaceID, s.config.WorkspaceID) {
		return Principal{}, http.StatusUnauthorized, "authentication_required"
	}
	return Principal{WorkspaceID: session.WorkspaceID, Kind: "session", Scopes: slices.Clone(allowedScopes), CSRFToken: session.CSRFToken}, 0, ""
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

func (s *Service) randomString(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := s.random(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Service) newTokenID() (string, error) {
	s.ulidMu.Lock()
	defer s.ulidMu.Unlock()
	id, err := ulid.New(ulid.Timestamp(s.now()), s.entropy)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (s *Service) seal(value string) (string, error) {
	nonce := make([]byte, s.sealer.NonceSize())
	if _, err := s.random(nonce); err != nil {
		return "", err
	}
	sealed := s.sealer.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Service) open(value string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < s.sealer.NonceSize() {
		return "", fmt.Errorf("invalid sealed credential")
	}
	nonce := sealed[:s.sealer.NonceSize()]
	plain, err := s.sealer.Open(nil, nonce, sealed[s.sealer.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Service) setCookie(response http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(response, &http.Cookie{Name: name, Value: value, Path: "/", Expires: expires, MaxAge: int(expires.Sub(s.now()).Seconds()), HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
}

func (s *Service) clearCookie(response http.ResponseWriter, name string) {
	http.SetCookie(response, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
}

func isLoopbackHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func normalizeOrigin(raw string) (string, *url.URL, error) {
	origin, err := url.Parse(raw)
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return "", nil, fmt.Errorf("invalid origin")
	}
	scheme := strings.ToLower(origin.Scheme)
	hostname := origin.Hostname()
	if ip := net.ParseIP(hostname); ip != nil {
		hostname = ip.String()
	} else {
		hostname, err = idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(hostname), "."))
		if err != nil || hostname == "" {
			return "", nil, fmt.Errorf("invalid origin hostname")
		}
	}
	port := origin.Port()
	if port != "" {
		numericPort, err := strconv.Atoi(port)
		if err != nil || numericPort < 1 || numericPort > 65535 {
			return "", nil, fmt.Errorf("invalid origin port")
		}
		port = strconv.Itoa(numericPort)
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	canonical := scheme + "://" + host
	parsed, err := url.Parse(canonical)
	if err != nil {
		return "", nil, err
	}
	return canonical, parsed, nil
}

func CanonicalOrigin(raw string) (string, error) {
	canonical, _, err := normalizeOrigin(raw)
	return canonical, err
}

func secureEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func validScopes(scopes []Scope) bool {
	seen := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		if !slices.Contains(allowedScopes, scope) {
			return false
		}
		if _, duplicate := seen[scope]; duplicate {
			return false
		}
		seen[scope] = struct{}{}
	}
	return true
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func isTokenAdministration(path string) bool {
	return path == "/api/v1/tokens" || strings.HasPrefix(path, "/api/v1/tokens/")
}

func requiredScope(method, path string) Scope {
	if strings.HasPrefix(path, "/api/v1/assets") || strings.Contains(path, "/assets") {
		if isMutation(method) {
			return ScopeAssetsWrite
		}
		return ScopeContentRead
	}
	if strings.HasPrefix(path, "/api/v1/content") {
		if isMutation(method) {
			return ScopeContentWrite
		}
		return ScopeContentRead
	}
	return ""
}

func writeError(response http.ResponseWriter, status int, code string) {
	writeJSON(response, status, map[string]string{"error": code})
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

type rateLimit struct {
	start time.Time
	count int
}
