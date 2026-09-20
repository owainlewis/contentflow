package content

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func thumbnailPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestThumbnailValidation(t *testing.T) {
	pngData := thumbnailPNG(t, 16, 9)
	oversizedPixels := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(oversizedPixels[16:20], 4001)
	binary.BigEndian.PutUint32(oversizedPixels[20:24], 4000)
	binary.BigEndian.PutUint32(oversizedPixels[29:33], crc32.ChecksumIEEE(oversizedPixels[12:29]))
	var jpegData bytes.Buffer
	_ = jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 16, 9)), nil)
	for _, test := range []struct {
		name, mime string
		data       []byte
		valid      bool
	}{
		{"pixel limit", "image/png", oversizedPixels, false},
		{"png", "image/png", pngData, true}, {"jpeg", "image/jpeg", jpegData.Bytes(), true},
		{"strips trailing payload", "image/png", append(bytes.Clone(pngData), []byte("<script>unsafe</script>")...), true},
		{"wrong MIME", "image/jpeg", pngData, false}, {"SVG", "image/svg+xml", []byte("<svg></svg>"), false},
		{"corrupt body", "image/png", pngData[:len(pngData)/2], false}, {"oversize", "image/png", make([]byte, MaxThumbnailBytes+1), false},
		{"wide", "image/png", thumbnailPNG(t, MaxThumbnailDimension+1, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := validateThumbnail(test.data, test.mime)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
			if err == nil && bytes.Contains(result.Data, []byte("<script>")) {
				t.Fatal("unsafe payload preserved")
			}
		})
	}
}
func testThumbnailLifecycle(t *testing.T, store Store) {
	t.Helper()
	service := NewService(store)
	ctx := t.Context()
	created, err := service.Create(ctx, "thumb-workspace", CreateRequest{Type: TypeYouTube, Status: StatusDraft, OperationID: testOperationID(), Content: YouTubeContent{Transcript: ""}}, "thumbnail-content")
	if err != nil {
		t.Fatal(err)
	}
	id := created.ItemIDs[0]
	if _, err = store.GetThumbnail(ctx, "thumb-workspace", id); err == nil {
		t.Fatal("missing image exists")
	}
	image1, _ := validateThumbnail(thumbnailPNG(t, 16, 9), "image/png")
	if err = store.PutThumbnail(ctx, "thumb-workspace", id, image1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetThumbnail(ctx, "different-workspace", id); err == nil {
		t.Fatal("cross-workspace read")
	}
	if err = store.PutThumbnail(ctx, "different-workspace", id, image1); err == nil {
		t.Fatal("cross-workspace write")
	}
	if err = store.DeleteThumbnail(ctx, "different-workspace", id); err == nil {
		t.Fatal("cross-workspace delete")
	}
	stored, err := store.GetThumbnail(ctx, "thumb-workspace", id)
	if err != nil || !bytes.Equal(stored.Data, image1.Data) {
		t.Fatalf("roundtrip: %v", err)
	}
	stored.Data[0] = 0
	stored, err = store.GetThumbnail(ctx, "thumb-workspace", id)
	if err != nil || !bytes.Equal(stored.Data, image1.Data) {
		t.Fatal("image storage aliased")
	}
	image2, _ := validateThumbnail(thumbnailPNG(t, 32, 18), "image/png")
	if err = store.PutThumbnail(ctx, "thumb-workspace", id, image2); err != nil {
		t.Fatal(err)
	}
	stored, err = store.GetThumbnail(ctx, "thumb-workspace", id)
	if err != nil || !bytes.Equal(stored.Data, image2.Data) {
		t.Fatal("replacement failed")
	}
	item, err := service.Get(ctx, "thumb-workspace", id)
	if err != nil || item.Revision != 1 {
		t.Fatal("thumbnail changed revision")
	}
	if err = store.DeleteThumbnail(ctx, "thumb-workspace", id); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteThumbnail(ctx, "thumb-workspace", id); err != nil {
		t.Fatal("remove not idempotent")
	}
	if _, err = store.GetThumbnail(ctx, "thumb-workspace", id); err == nil {
		t.Fatal("image survived removal")
	}
	if err = store.PutThumbnail(ctx, "thumb-workspace", id, image1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Delete(ctx, "thumb-workspace", id, RevisionRequest{OperationID: testOperationID(), Revision: 1}, "delete-thumbnail-parent"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetThumbnail(ctx, "thumb-workspace", id); err == nil {
		t.Fatal("image survived parent deletion")
	}
	other, err := service.Create(ctx, "thumb-workspace", CreateRequest{Type: TypeInstagram, Status: StatusDraft, OperationID: testOperationID(), Content: InstagramContent{}}, "non-youtube")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutThumbnail(ctx, "thumb-workspace", other.ItemIDs[0], image1); err == nil {
		t.Fatal("non-youtube accepted thumbnail")
	}
}
func TestMemoryThumbnailLifecycle(t *testing.T) { testThumbnailLifecycle(t, NewMemoryStore()) }
func TestPostgresThumbnailLifecycle(t *testing.T) {
	store, pool := newPostgresStore(t)
	testThumbnailLifecycle(t, store)
	var n int
	if err := pool.QueryRow(t.Context(), "select count(*) from content_thumbnails").Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade left rows %d %v", n, err)
	}
}
