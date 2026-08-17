package server

import (
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"html"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/content"
	"github.com/owainlewis/contentflow/apps/api/internal/health"
)

const proxySecretHeader = "X-ContentFlow-Proxy-Secret"
const socialImagePlaceholder = "__CONTENTFLOW_SOCIAL_IMAGE__"

type statusResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewAPI(checker health.Checker, authentication *auth.Service) http.Handler {
	return NewAPIWithContent(checker, authentication, nil)
}

func NewAPIWithContent(checker health.Checker, authentication *auth.Service, contentHandler *content.HTTPHandler) http.Handler {
	router := newRouter(checker)
	if authentication != nil {
		service := authentication
		router.Get("/api/v1/auth/login", service.HandleLogin)
		router.Get("/api/v1/auth/callback", service.HandleCallback)
		router.Post("/api/v1/auth/callback", service.HandleCallback)
		router.Post("/api/v1/auth/password", service.HandlePasswordLogin)
		router.Group(func(protected chi.Router) {
			protected.Use(service.Authenticate, service.Authorize)
			protected.Get("/api/v1/session", service.HandleSession)
			protected.Post("/api/v1/tokens", service.HandleCreateToken)
			protected.Delete("/api/v1/tokens/{tokenID}", func(response http.ResponseWriter, request *http.Request) {
				service.HandleRevokeToken(response, request, chi.URLParam(request, "tokenID"))
			})
			if contentHandler != nil {
				contentHandler.Register(protected)
			}
			protected.Handle("/api/v1/*", http.HandlerFunc(notFound))
		})
	}
	router.NotFound(notFound)
	return router
}

func NewLocalAPIWithContent(checker health.Checker, contentHandler *content.HTTPHandler, workspaceID string) http.Handler {
	router := newRouter(checker)
	csrfToken := cryptorand.Text()
	router.Group(func(protected chi.Router) {
		protected.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if !trustedLocalHost(request.Host) {
					writeJSON(response, http.StatusForbidden, errorResponse{Error: "untrusted_local_host"})
					return
				}
				if localMutation(request.Method) && !validLocalCSRF(request, csrfToken) {
					writeJSON(response, http.StatusForbidden, errorResponse{Error: "csrf_check_failed"})
					return
				}
				principal := auth.Principal{WorkspaceID: workspaceID, Kind: "session", CSRFToken: csrfToken, Scopes: []auth.Scope{auth.ScopeContentRead, auth.ScopeContentWrite, auth.ScopeAssetsWrite}}
				next.ServeHTTP(response, request.WithContext(auth.ContextWithPrincipal(request.Context(), principal)))
			})
		})
		protected.Get("/api/v1/session", func(response http.ResponseWriter, _ *http.Request) {
			writeJSON(response, http.StatusOK, map[string]any{"workspace_id": workspaceID, "identity": "local", "csrf_token": csrfToken, "scopes": []auth.Scope{auth.ScopeContentRead, auth.ScopeContentWrite, auth.ScopeAssetsWrite}})
		})
		contentHandler.Register(protected)
		protected.Handle("/api/v1/*", http.HandlerFunc(notFound))
	})
	router.NotFound(notFound)
	return router
}

func localMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func trustedLocalHost(hostPort string) bool {
	parsed, err := url.Parse("//" + hostPort)
	if err != nil || parsed.User != nil {
		return false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "localhost" {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func validLocalCSRF(request *http.Request, csrfToken string) bool {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	expectedOrigin, err := auth.CanonicalOrigin(scheme + "://" + request.Host)
	if err != nil {
		return false
	}
	requestOrigin, err := auth.CanonicalOrigin(request.Header.Get("Origin"))
	return err == nil && subtle.ConstantTimeCompare([]byte(requestOrigin), []byte(expectedOrigin)) == 1 && subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(csrfToken)) == 1
}

func newRouter(checker health.Checker) *chi.Mux {
	router := chi.NewRouter()
	router.Use(redactedRequestLogger)
	router.Get("/health/live", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, statusResponse{Status: "live"})
	})
	router.Get("/health/ready", func(response http.ResponseWriter, request *http.Request) {
		results := checker.Check(request.Context())
		checks := make(map[string]string, len(results))
		status := http.StatusOK
		state := "ready"
		for name, err := range results {
			checks[name] = "ok"
			if err != nil {
				checks[name] = "unavailable"
				status = http.StatusServiceUnavailable
				state = "not_ready"
			}
		}
		writeJSON(response, status, statusResponse{Status: state, Checks: checks})
	})
	return router
}

func notFound(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusNotFound, errorResponse{Error: "not_found"})
}

func redactedRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		slog.DebugContext(request.Context(), "HTTP request", "method", request.Method, "path", request.URL.Path)
		next.ServeHTTP(response, request)
	})
}

func NewApplication(assets fs.FS, api http.Handler) http.Handler {
	static := staticHandler(assets)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if isAPIPath(request.URL.Path) {
			api.ServeHTTP(response, request)
			return
		}
		static.ServeHTTP(response, request)
	})
}

func NewLocalPublicApplication(assets fs.FS, privateAPI *url.URL, proxySecret string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(privateAPI)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Header.Set(proxySecretHeader, proxySecret)
	}

	static := staticHandler(assets)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if isAPIPath(request.URL.Path) {
			proxy.ServeHTTP(response, request)
			return
		}
		static.ServeHTTP(response, request)
	})
}

func RequireProxySecret(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get(proxySecretHeader)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			writeJSON(response, http.StatusUnauthorized, errorResponse{Error: "local_proxy_auth_required"})
			return
		}
		next.ServeHTTP(response, request)
	})
}

func staticHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if name == "." || name == "" {
			serveIndex(response, request, assets)
			return
		}

		if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
			if name == "index.html" {
				serveIndex(response, request, assets)
				return
			}
			serveFile(response, request, assets, name)
			return
		}
		if name == "assets" || strings.HasPrefix(name, "assets/") {
			http.NotFound(response, request)
			return
		}
		if path.Ext(name) == "" {
			serveIndex(response, request, assets)
			return
		}
		http.NotFound(response, request)
	})
}

func serveIndex(response http.ResponseWriter, request *http.Request, assets fs.FS) {
	contents, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(response, "web build is unavailable", http.StatusServiceUnavailable)
		return
	}
	socialImage := html.EscapeString(absoluteRequestURL(request, "/og.png"))
	contents = []byte(strings.ReplaceAll(string(contents), socialImagePlaceholder, socialImage))
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(response, request, "index.html", time.Time{}, strings.NewReader(string(contents)))
}

func absoluteRequestURL(request *http.Request, assetPath string) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if forwardedScheme := firstForwardedValue(request.Header.Get("X-Forwarded-Proto")); forwardedScheme == "http" || forwardedScheme == "https" {
		scheme = forwardedScheme
	}

	host := firstForwardedValue(request.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = request.Host
	}
	if host == "" {
		return assetPath
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: assetPath}).String()
}

func firstForwardedValue(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(first)
}

func serveFile(response http.ResponseWriter, request *http.Request, assets fs.FS, name string) {
	contents, err := fs.ReadFile(assets, name)
	if err != nil {
		http.Error(response, "web build is unavailable", http.StatusServiceUnavailable)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		response.Header().Set("Content-Type", contentType)
	}
	if name == "index.html" {
		response.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(response, request, name, time.Time{}, strings.NewReader(string(contents)))
}

func isAPIPath(requestPath string) bool {
	return requestPath == "/health" || strings.HasPrefix(requestPath, "/health/") ||
		requestPath == "/api" || strings.HasPrefix(requestPath, "/api/")
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}
