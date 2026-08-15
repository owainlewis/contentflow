package content

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
)

type HTTPHandler struct{ service *Service }

func NewHTTPHandler(service *Service) *HTTPHandler { return &HTTPHandler{service: service} }

func (h *HTTPHandler) Register(router chi.Router) {
	router.Get("/api/v1/content", h.list)
	router.Post("/api/v1/content", h.create)
	router.Get("/api/v1/content/{id}", h.get)
	router.Get("/api/v1/content/{id}/transcript", h.transcript)
	router.Put("/api/v1/content/{id}", h.replace)
	router.Post("/api/v1/content/{id}/archive", h.archive)
	router.Post("/api/v1/content/{id}/restore", h.restore)
	router.Delete("/api/v1/content/{id}", h.delete)
}

func (h *HTTPHandler) list(response http.ResponseWriter, request *http.Request) {
	for key, values := range request.URL.Query() {
		if (key != "type" && key != "status" && key != "q") || len(values) != 1 {
			writeContentError(response, problem(400, "invalid_query"))
			return
		}
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	items, err := h.service.List(request.Context(), principal.WorkspaceID, ListQuery{
		Type: Type(request.URL.Query().Get("type")), Status: Status(request.URL.Query().Get("status")), TitlePrefix: request.URL.Query().Get("q"),
	})
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPHandler) create(response http.ResponseWriter, request *http.Request) {
	body, err := readMutationBody(response, request)
	if err != nil {
		writeContentError(response, err)
		return
	}
	input, err := DecodeCreate(body)
	if err != nil {
		writeContentError(response, err)
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	result, err := h.service.Create(request.Context(), principal.WorkspaceID, input, RequestHash(request.Method, request.URL.Path, body))
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusCreated, result)
}

func (h *HTTPHandler) get(response http.ResponseWriter, request *http.Request) {
	id, ok := contentID(response, request)
	if !ok {
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	item, err := h.service.Get(request.Context(), principal.WorkspaceID, id)
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, item)
}

func (h *HTTPHandler) transcript(response http.ResponseWriter, request *http.Request) {
	id, ok := contentID(response, request)
	if !ok {
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	transcript, err := h.service.Transcript(request.Context(), principal.WorkspaceID, id)
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, transcript)
}

func (h *HTTPHandler) replace(response http.ResponseWriter, request *http.Request) {
	id, ok := contentID(response, request)
	if !ok {
		return
	}
	body, err := readMutationBody(response, request)
	if err != nil {
		writeContentError(response, err)
		return
	}
	input, err := DecodeReplace(body)
	if err != nil {
		writeContentError(response, err)
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	result, err := h.service.Replace(request.Context(), principal.WorkspaceID, id, input, RequestHash(request.Method, request.URL.Path, body))
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, result)
}

func (h *HTTPHandler) archive(response http.ResponseWriter, request *http.Request) {
	h.setArchived(response, request, true)
}
func (h *HTTPHandler) restore(response http.ResponseWriter, request *http.Request) {
	h.setArchived(response, request, false)
}

func (h *HTTPHandler) setArchived(response http.ResponseWriter, request *http.Request, archived bool) {
	id, ok := contentID(response, request)
	if !ok {
		return
	}
	body, err := readMutationBody(response, request)
	if err != nil {
		writeContentError(response, err)
		return
	}
	input, err := DecodeRevision(body)
	if err != nil {
		writeContentError(response, err)
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	result, err := h.service.SetArchived(request.Context(), principal.WorkspaceID, id, input, archived, RequestHash(request.Method, request.URL.Path, body))
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, result)
}

func (h *HTTPHandler) delete(response http.ResponseWriter, request *http.Request) {
	id, ok := contentID(response, request)
	if !ok {
		return
	}
	body, err := readMutationBody(response, request)
	if err != nil {
		writeContentError(response, err)
		return
	}
	input, err := DecodeRevision(body)
	if err != nil {
		writeContentError(response, err)
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	result, err := h.service.Delete(request.Context(), principal.WorkspaceID, id, input, RequestHash(request.Method, request.URL.Path, body))
	if err != nil {
		writeContentError(response, err)
		return
	}
	writeContentJSON(response, http.StatusOK, result)
}

func contentID(response http.ResponseWriter, request *http.Request) (string, bool) {
	id := chi.URLParam(request, "id")
	if _, err := ulid.ParseStrict(id); err != nil {
		writeContentError(response, notFound())
		return "", false
	}
	return id, true
}

func readMutationBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	request.Body = http.MaxBytesReader(response, request.Body, MaxRequestBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, problem(413, "request_too_large")
		}
		return nil, problem(400, "invalid_request")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, problem(400, "invalid_request")
	}
	return body, nil
}

func writeContentError(response http.ResponseWriter, err error) {
	var contentError *Error
	if !errors.As(err, &contentError) {
		contentError = &Error{Status: 503, Code: "content_unavailable", Cause: err}
	}
	body := map[string]any{"error": contentError.Code}
	if contentError.Current != nil {
		body["current"] = contentError.Current
	}
	writeContentJSON(response, contentError.Status, body)
}

func writeContentJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}
