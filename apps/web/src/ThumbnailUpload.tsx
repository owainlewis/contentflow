import { useCallback, useEffect, useRef, useState } from "react";
import { ImagePlus } from "lucide-react";
import { ApiError, deleteThumbnail, getThumbnail, isSessionRecoveryError, uploadThumbnail } from "./api";

export default function ThumbnailUpload({ id, csrfToken, onSessionExpired }: { id: string; csrfToken: string; onSessionExpired?: () => void }) {
  const [preview, setPreview] = useState("");
  const [busy, setBusy] = useState("Loading thumbnail…");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const request = useRef<AbortController | null>(null);
  const recover = useRef(onSessionExpired);
  useEffect(() => { recover.current = onSessionExpired; }, [onSessionExpired]);
  const previewURL = useRef("");
  const updatePreview = useCallback((blob: Blob | null) => {
    if (previewURL.current) URL.revokeObjectURL(previewURL.current);
    previewURL.current = blob ? URL.createObjectURL(blob) : "";
    setPreview(previewURL.current);
  }, []);
  useEffect(() => () => { if (previewURL.current) URL.revokeObjectURL(previewURL.current); }, []);

  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    getThumbnail(id, controller.signal).then((image) => {
      if (!controller.signal.aborted) updatePreview(image);
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return;
      if (isSessionRecoveryError(cause)) recover.current?.();
      setError("Could not load the thumbnail. Try again.");
    }).finally(() => { if (!controller.signal.aborted) setBusy(""); });
    return () => { controller.abort(); request.current?.abort(); };
  }, [id, reload, updatePreview]);

  const mutate = async (file?: File) => {
    if (busy) return;
    if (file && (!["image/jpeg", "image/png"].includes(file.type) || file.size > 5 * 1024 * 1024 || file.size === 0)) {
      setError("Choose a JPEG or PNG image up to 5 MB."); return;
    }
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    setError(""); setBusy(file ? "Uploading thumbnail…" : "Removing thumbnail…");
    try {
      if (file) await uploadThumbnail(id, file, csrfToken, controller.signal);
      else await deleteThumbnail(id, csrfToken, controller.signal);
      if (!controller.signal.aborted) updatePreview(file ?? null);
    } catch (cause) {
      if (controller.signal.aborted) return;
      if (isSessionRecoveryError(cause)) recover.current?.();
      const uploadError = cause instanceof ApiError && cause.code === "thumbnail_dimensions_exceeded"
        ? "Choose an image no larger than 8192 pixels per side and 16 million pixels overall."
        : cause instanceof ApiError && cause.code === "invalid_thumbnail"
          ? "This file could not be read as a JPEG or PNG image. Choose another image."
          : "Could not upload the thumbnail. Try again or reload to check the saved image.";
      setError(file ? uploadError : "Could not remove the thumbnail. Try again or reload to check the saved image.");
    } finally { if (!controller.signal.aborted) setBusy(""); }
  };

  return <div className="thumbnail-upload" aria-busy={!!busy}>
    <span className="thumbnail-label">YouTube thumbnail</span>
    <input ref={input} type="file" accept="image/jpeg,image/png" aria-label="Upload YouTube thumbnail" hidden disabled={!!busy} onChange={(event) => {
      const file = event.target.files?.[0]; event.target.value = "";
      if (file) void mutate(file);
    }} />
    {preview ? <img className="thumbnail-preview" src={preview} alt="YouTube thumbnail" /> : <button className="thumbnail-placeholder" type="button" disabled={!!busy} onClick={() => input.current?.click()}><ImagePlus size={28} /><span>Upload thumbnail</span><small>JPEG or PNG · up to 5 MB</small></button>}
    <div className="thumbnail-actions">
      {preview && <><button type="button" disabled={!!busy} onClick={() => input.current?.click()}>Replace thumbnail</button><button type="button" disabled={!!busy} onClick={() => void mutate()}>Remove thumbnail</button></>}
      {busy && <span role="status">{busy}</span>}
      {error && <><span role="alert">{error}</span><button type="button" disabled={!!busy} onClick={() => { setBusy("Loading thumbnail…"); setError(""); setReload((value) => value + 1); }}>Reload thumbnail</button></>}
    </div>
  </div>;
}
