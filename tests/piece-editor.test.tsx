import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByLabelText("Publish date")).toBeNull();
    expect(container.querySelector("details")).toBeNull();
    await user.type(screen.getByLabelText("LinkedIn post"), "A");
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ content: { body: "A" } }));
    await user.click(screen.getByRole("button", { name: "Edit details" }));
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
    expect(screen.queryByLabelText("Topic notes")).toBeNull();
    expect(container.querySelector("details")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Topic notes" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Topic notes" }), { target: { value: "Updated idea" } });
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ content: { ...detail.content, source: "Updated idea" } }));
  });

  it("shows a single-line YouTube title, thumbnail, and description in the Video tab", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonError(404, "thumbnail_not_found")));
    const { container } = render(<PieceEditor detail={piece("youtube")} topics={[]} onChange={vi.fn()} csrfToken="csrf" />);
    await waitFor(() => expect(screen.queryByText("Loading thumbnail…")).toBeNull());
    const packaging = container.querySelector(".piece-youtube-package")!;
    expect(packaging.contains(screen.getByLabelText("YouTube title"))).toBe(true);
    expect(packaging.contains(screen.getByRole("button", { name: /Upload thumbnail/ }))).toBe(true);
    expect(screen.getByLabelText("YouTube title").tagName).toBe("INPUT");
    expect(screen.getByLabelText("YouTube title")).toHaveProperty("type", "text");
    expect(screen.getByRole("tab", { name: "Video" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByLabelText("YouTube description")).toBeTruthy();
    expect(screen.queryByLabelText("YouTube transcript: what was actually said")).toBeNull();
    expect(container.querySelector("details")).toBeNull();
  });
});

describe("editor dialogs and tabs", () => {
  it("traps keyboard focus in metadata and restores the trigger after Escape", async () => {
    const user = userEvent.setup();
    render(<PieceEditor detail={piece("linkedin")} topics={[]} onChange={vi.fn()} />);
    const trigger = screen.getByRole("button", { name: "Edit details" });
    trigger.focus();
    await user.keyboard("{Enter}");
    const dialog = screen.getByRole("dialog", { name: "Piece details" });
    const close = within(dialog).getByRole("button", { name: "Close Piece details" });
    await waitFor(() => expect(document.activeElement).toBe(close));
    await user.keyboard("{Shift>}{Tab}{/Shift}");
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Done" }));
    await user.keyboard("{Tab}");
    expect(document.activeElement).toBe(close);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByLabelText("Publish date")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("locks metadata fields already open in a portal when the piece becomes disabled", async () => {
    const user = userEvent.setup();
    const change = vi.fn();
    const detail = piece("linkedin");
    const { rerender } = render(<PieceEditor detail={detail} topics={[]} onChange={change} />);
    await user.click(screen.getByRole("button", { name: "Edit details" }));
    const dialog = screen.getByRole("dialog", { name: "Piece details" });
    const title = within(dialog).getByRole("textbox", { name: "Working title" });
    rerender(<PieceEditor detail={detail} topics={[]} onChange={change} disabled />);
    expect(title.closest("fieldset")).toHaveProperty("disabled", true);
    await user.type(title, " blocked");
    expect(change).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "Edit details" })).toHaveProperty("disabled", true);
  });

  it("opens external links for editing in a dialog without replacing the writing view", async () => {
    const user = userEvent.setup();
    const change = vi.fn();
    render(<PieceEditor detail={piece("linkedin")} topics={[]} onChange={change} />);
    expect(screen.queryByLabelText("External document")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Add External document" }));
    const dialog = screen.getByRole("dialog", { name: "External document" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "External document" }), { target: { value: "https://docs.google.com/document/d/script" } });
    expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ document_url: "https://docs.google.com/document/d/script" }));
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.getByLabelText("LinkedIn post")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("switches YouTube tabs with arrow keys and keeps inactive editors out of focus order", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonError(404, "thumbnail_not_found")));
    const user = userEvent.setup();
    render(<PieceEditor detail={piece("youtube")} topics={[]} onChange={vi.fn()} />);
    const video = screen.getByRole("tab", { name: "Video" });
    video.focus();
    await user.keyboard("{ArrowRight}");
    await waitFor(() => expect(screen.getByRole("tab", { name: "Transcript" }).getAttribute("aria-selected")).toBe("true"));
    expect(screen.getByLabelText("YouTube transcript: what was actually said")).toBeTruthy();
    expect(screen.queryByLabelText("YouTube title")).toBeNull();
    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("tab", { name: "Video" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByLabelText("YouTube title")).toBeTruthy();
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
