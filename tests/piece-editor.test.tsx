import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import PieceEditor from "../apps/web/src/PieceEditor";
import ThumbnailUpload from "../apps/web/src/ThumbnailUpload";
import { emptyContent, type ContentDetail, type ContentType } from "../apps/web/src/api";

const image = () => new File(["image bytes"], "thumbnail.png", { type: "image/png" });
const jsonError = (status: number, error: string) => new Response(JSON.stringify({ error }), { status, headers: { "Content-Type": "application/json" } });
function piece(type: ContentType): ContentDetail {
  return { id: "piece-1", type, working_title: "Topic title", status: "draft", revision: 1, created_at: "2026-09-20T09:00:00Z", updated_at: "2026-09-20T09:00:00Z", expires_at: "", content: emptyContent(type) };
}

beforeEach(() => {
  let index = 0;
  vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:thumbnail-${++index}`);
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("focused piece editor", () => {
  it("leads with writing and reveals metadata only when requested", async () => {
    const user = userEvent.setup();
    const change = vi.fn();
    const { container } = render(<PieceEditor detail={piece("linkedin")} topics={[]} onChange={change} />);
    const editor = container.querySelector(".piece-editor")!;
    expect(editor.firstElementChild?.textContent).toBe("LinkedIn post");
    expect(container.querySelector(".piece-settings")?.hasAttribute("open")).toBe(false);
    await user.type(screen.getByLabelText("LinkedIn post"), "A");
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ content: { body: "A" } }));
    await user.click(screen.getByText(/Unscheduled/));
    await user.selectOptions(screen.getByLabelText("Content status"), "ready");
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ status: "ready" }));
    fireEvent.change(screen.getByLabelText("Publish date"), { target: { value: "2026-09-25" } });
    expect(change.mock.lastCall?.[0].scheduled_at.startsWith("2026-09-25")).toBe(true);
  });

  it("keeps document actions compact and topic notes available on demand", async () => {
    const user = userEvent.setup();
    const detail = { ...piece("topic"), content: { source: "Core idea", source_url: "https://docs.google.com/document/d/example" } };
    const change = vi.fn();
    const { container } = render(<PieceEditor detail={detail} topics={[]} onChange={change} />);
    expect(screen.getByRole("link", { name: "Open Source document" }).getAttribute("href")).toBe(detail.content.source_url);
    expect(container.querySelector(".piece-details")?.hasAttribute("open")).toBe(false);
    await user.click(screen.getByText("Topic notes · saved"));
    fireEvent.change(screen.getByLabelText("Topic notes"), { target: { value: "Updated idea" } });
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ content: { ...detail.content, source: "Updated idea" } }));
  });

  it("pairs the YouTube title and thumbnail before document links and collapsed description", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonError(404, "thumbnail_not_found")));
    const { container } = render(<PieceEditor detail={piece("youtube")} topics={[]} onChange={vi.fn()} csrfToken="csrf" />);
    await waitFor(() => expect(screen.queryByText("Loading thumbnail…")).toBeNull());
    const packaging = container.querySelector(".piece-youtube-package")!;
    expect(packaging.contains(screen.getByLabelText("YouTube title"))).toBe(true);
    expect(packaging.contains(screen.getByRole("button", { name: /Upload thumbnail/ }))).toBe(true);
    expect(screen.getByLabelText("YouTube description").closest("details")?.open).toBe(false);
  });
});

describe("persistent thumbnail upload", () => {
  it("uploads, reloads the saved image, replaces and removes it with CSRF protection", async () => {
    const user = userEvent.setup();
    let saved = false;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
      if (init.method === "PUT") { saved = true; return new Response(JSON.stringify({ url: "/thumbnail" })); }
      if (init.method === "DELETE") { saved = false; return new Response(null, { status: 204 }); }
      return saved ? new Response("saved image", { headers: { "Content-Type": "image/png" } }) : jsonError(404, "thumbnail_not_found");
    });
    vi.stubGlobal("fetch", fetcher);
    const first = render(<ThumbnailUpload id="video-1" csrfToken="csrf-secret" />);
    await waitFor(() => expect(screen.queryByText("Loading thumbnail…")).toBeNull());
    const file = image();
    await user.upload(screen.getByLabelText("Upload YouTube thumbnail"), file);
    await screen.findByRole("img", { name: "YouTube thumbnail" });
    expect(fetcher).toHaveBeenCalledWith("/api/v1/content/video-1/thumbnail", expect.objectContaining({ method: "PUT", body: file, headers: { "Content-Type": "image/png", "X-CSRF-Token": "csrf-secret" } }));
    first.unmount();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:thumbnail-1");
    render(<ThumbnailUpload id="video-1" csrfToken="csrf-secret" />);
    await screen.findByRole("img", { name: "YouTube thumbnail" });
    await user.upload(screen.getByLabelText("Upload YouTube thumbnail"), image());
    await waitFor(() => expect((screen.getByRole("img") as HTMLImageElement).src).toBe("blob:thumbnail-3"));
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:thumbnail-2");
    await user.click(screen.getByRole("button", { name: "Remove thumbnail" }));
    await waitFor(() => expect(screen.queryByRole("img")).toBeNull());
    expect(saved).toBe(false);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:thumbnail-3");
  });

  it("retains the current preview on upload failure and explains invalid server images", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => init.method === "PUT" ? jsonError(400, "invalid_thumbnail") : new Response("existing image")));
    render(<ThumbnailUpload id="video-1" csrfToken="csrf" />);
    await screen.findByRole("img");
    await user.upload(screen.getByLabelText("Upload YouTube thumbnail"), image());
    expect((await screen.findByRole("alert")).textContent).toContain("could not be read as a JPEG or PNG");
    expect((screen.getByRole("img") as HTMLImageElement).src).toBe("blob:thumbnail-1");
  });

  it("rejects oversized uploads before sending a request", async () => {
    const user = userEvent.setup();
    const fetcher = vi.fn().mockResolvedValue(jsonError(404, "thumbnail_not_found"));
    vi.stubGlobal("fetch", fetcher);
    render(<ThumbnailUpload id="video-1" csrfToken="csrf" />);
    await waitFor(() => expect(screen.queryByText("Loading thumbnail…")).toBeNull());
    const file = image();
    Object.defineProperty(file, "size", { value: 5 * 1024 * 1024 + 1 });
    await user.upload(screen.getByLabelText("Upload YouTube thumbnail"), file);
    expect(screen.getByRole("alert").textContent).toContain("up to 5 MB");
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each(["GET", "PUT", "DELETE"])("recovers controls after a stalled %s request times out", async (method) => {
    const user = userEvent.setup();
    const nativeTimeout = window.setTimeout.bind(window);
    vi.spyOn(window, "setTimeout").mockImplementation(((handler: TimerHandler, timeout?: number, ...args: unknown[]) => nativeTimeout(handler, timeout === 10_000 ? 0 : timeout, ...args)) as typeof window.setTimeout);
    let stalledSignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
      if ((init.method ?? "GET") === method) {
        stalledSignal = init.signal ?? undefined;
        return new Promise<Response>(() => {});
      }
      return method === "DELETE" ? new Response("saved image") : jsonError(404, "thumbnail_not_found");
    }));
    render(<ThumbnailUpload id="video-1" csrfToken="csrf" />);
    if (method !== "GET") {
      await waitFor(() => expect(screen.queryByText("Loading thumbnail…")).toBeNull());
      if (method === "PUT") await user.upload(screen.getByLabelText("Upload YouTube thumbnail"), image());
      else await user.click(screen.getByRole("button", { name: "Remove thumbnail" }));
    }
    await screen.findByRole("alert");
    expect(stalledSignal?.aborted).toBe(true);
    expect((screen.getByRole("button", { name: "Reload thumbnail" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByLabelText("Upload YouTube thumbnail") as HTMLInputElement).disabled).toBe(false);
    if (method === "DELETE") expect(screen.getByRole("img")).toBeDefined();
  });

  it("recovers an expired session on reads and mutations", async () => {
    const user = userEvent.setup();
    const recover = vi.fn();
    const fetcher = vi.fn().mockResolvedValueOnce(jsonError(401, "session_expired")).mockResolvedValueOnce(jsonError(404, "thumbnail_not_found")).mockResolvedValueOnce(jsonError(403, "csrf_check_failed"));
    vi.stubGlobal("fetch", fetcher);
    render(<ThumbnailUpload id="video-1" csrfToken="expired" onSessionExpired={recover} />);
    await screen.findByRole("alert");
    expect(recover).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Reload thumbnail" }));
    await waitFor(() => expect(screen.queryByText("Loading thumbnail…")).toBeNull());
    await user.upload(screen.getByLabelText("Upload YouTube thumbnail"), image());
    await waitFor(() => expect(recover).toHaveBeenCalledTimes(2));
  });
});
