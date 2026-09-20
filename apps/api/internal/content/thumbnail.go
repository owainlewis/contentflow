package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"

	"github.com/owainlewis/contentflow/apps/api/internal/auth"
)

const MaxThumbnailBytes = 5 << 20
const MaxThumbnailDimension = 8192
const MaxThumbnailPixels = 16_000_000

type Thumbnail struct {
	Data        []byte
	ContentType string
}

// Decode and re-encode raster images so stored files contain only image data.
func validateThumbnail(data []byte, contentType string) (Thumbnail, error) {
	if len(data) > MaxThumbnailBytes {
		return Thumbnail{}, problem(413, "thumbnail_too_large")
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "image/jpeg" && mediaType != "image/png") {
		return Thumbnail{}, problem(400, "invalid_thumbnail")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || mediaType != "image/"+format {
		return Thumbnail{}, problem(400, "invalid_thumbnail")
	}
	if config.Width < 1 || config.Height < 1 || config.Width > MaxThumbnailDimension || config.Height > MaxThumbnailDimension || int64(config.Width)*int64(config.Height) > MaxThumbnailPixels {
		return Thumbnail{}, problem(400, "thumbnail_dimensions_exceeded")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Thumbnail{}, problem(400, "invalid_thumbnail")
	}
	var encoded bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&encoded, decoded, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&encoded, decoded)
	}
	if err != nil {
		return Thumbnail{}, problem(400, "invalid_thumbnail")
	}
	if encoded.Len() > MaxThumbnailBytes {
		return Thumbnail{}, problem(413, "thumbnail_too_large")
	}
	return Thumbnail{Data: encoded.Bytes(), ContentType: mediaType}, nil
}

func thumbnailType(item Item) error {
	if item.Type != TypeYouTube {
		return problem(400, "thumbnail_not_supported")
	}
	return nil
}

func (h *HTTPHandler) thumbnail(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "private, no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	id, ok := contentID(response, request)
	if !ok {
		return
	}
	principal, _ := auth.PrincipalFromContext(request.Context())
	switch request.Method {
	case http.MethodGet:
		thumbnail, err := h.service.store.GetThumbnail(request.Context(), principal.WorkspaceID, id)
		if err != nil {
			writeContentError(response, err)
			return
		}
		digest := sha256.Sum256(thumbnail.Data)
		response.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
		response.Header().Set("Content-Type", thumbnail.ContentType)
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(thumbnail.Data)
	case http.MethodPut:
		request.Body = http.MaxBytesReader(response, request.Body, MaxThumbnailBytes)
		data, err := io.ReadAll(request.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeContentError(response, problem(413, "thumbnail_too_large"))
			} else {
				writeContentError(response, problem(400, "invalid_thumbnail"))
			}
			return
		}
		thumbnail, err := validateThumbnail(data, request.Header.Get("Content-Type"))
		if err != nil {
			writeContentError(response, err)
			return
		}
		if err = h.service.store.PutThumbnail(request.Context(), principal.WorkspaceID, id, thumbnail); err != nil {
			writeContentError(response, err)
			return
		}
		writeContentJSON(response, http.StatusOK, map[string]string{"url": "/api/v1/content/" + id + "/thumbnail"})
	case http.MethodDelete:
		if err := h.service.store.DeleteThumbnail(request.Context(), principal.WorkspaceID, id); err != nil {
			writeContentError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}
}

func (s *MemoryStore) thumbnailItemLocked(workspaceID, id string) error {
	item, ok := s.items[memoryKey(workspaceID, id)]
	if !ok {
		return notFound()
	}
	return thumbnailType(item)
}
func (s *MemoryStore) GetThumbnail(_ context.Context, workspaceID, id string) (Thumbnail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.thumbnailItemLocked(workspaceID, id); err != nil {
		return Thumbnail{}, err
	}
	thumbnail, ok := s.thumbnails[memoryKey(workspaceID, id)]
	if !ok {
		return Thumbnail{}, problem(404, "thumbnail_not_found")
	}
	thumbnail.Data = bytes.Clone(thumbnail.Data)
	return thumbnail, nil
}
func (s *MemoryStore) PutThumbnail(_ context.Context, workspaceID, id string, thumbnail Thumbnail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.thumbnailItemLocked(workspaceID, id); err != nil {
		return err
	}
	thumbnail.Data = bytes.Clone(thumbnail.Data)
	s.thumbnails[memoryKey(workspaceID, id)] = thumbnail
	return nil
}
func (s *MemoryStore) DeleteThumbnail(_ context.Context, workspaceID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.thumbnailItemLocked(workspaceID, id); err != nil {
		return err
	}
	delete(s.thumbnails, memoryKey(workspaceID, id))
	return nil
}
