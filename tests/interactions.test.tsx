// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import Home from "../apps/web/src/App";
import { emptyContent, listContent, loadSession, type ContentDetail, type ContentStatus, type ContentType, type YouTubeContent } from "../apps/web/src/api";

const now = new Date("2026-08-15T10:00:00Z");
const expires = new Date(now.getTime() + 56 * 86_400_000).toISOString();

function summary(item: ContentDetail) {
  const copy = { ...item } as Partial<ContentDetail> & { asset_counts?: Record<string, number> };
  delete copy.content;
  copy.asset_counts = {};
  return copy;
}

function detail(type: ContentType, id = `01K${type.toUpperCase().padEnd(23, "0")}`): ContentDetail {
  const label = type === "youtube" ? "YouTube" : type === "linkedin" ? "LinkedIn" : type === "x" ? "X" : type[0].toUpperCase() + type.slice(1);
  return {
    id,
    type,
    status: "draft",
    working_title: `${label} one`,
    revision: 1,
    created_at: now.toISOString(),
    updated_at: now.toISOString(),
    expires_at: expires,
    content: emptyContent(type),
  };
}

class FakeAPI {
  items = new Map<string, ContentDetail>();
  requests: Array<{ method: string; path: string; body: string; csrfToken: string | null }> = [];
  replaceBodies: string[] = [];
  conflictNext = false;
  lifecycleConflictNext = false;
  lifecycleGate?: Promise<void>;
  createGate?: Promise<void>;
  detailGate?: Promise<void>;
  failGatedDetail = false;
  expireGatedDetail = false;
  detailFailures = new Set<string>();
  failNextList = false;
  expireNextList = false;
  failListAfterLifecycle = false;
  listGate?: Promise<void>;
  listGateStarted = 0;
  failGatedList = false;
  expireGatedList = false;
  createResponseLostOnce = false;
  deleteResponseLostOnce = false;
  lifecycleResponseLostOnce = false;
  replaceAuthFailure?: { status: number; code: string };
  createAuthFailure?: { status: number; code: string };
  gatedCreateFailure?: { status: number; code: string };
  lifecycleAuthFailure?: { status: number; code: string };
  gatedLifecycleFailure?: { status: number; code: string };
  expireNextDetail = false;
  excludeExpired = false;
  sessionCounter = 0;
  createReceipts = new Map<string, { result: object; status: number }>();
  deleteReceipts = new Map<string, object>();
  lifecycleReceipts = new Map<string, { signature: string; result: object }>();

  constructor(items: ContentDetail[] = [detail("youtube"), detail("linkedin")]) {
    for (const item of items) this.items.set(item.id, item);
  }

  fetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(typeof input === "string" ? input : input.toString(), "http://localhost");
    const method = init.method ?? "GET";
    const body = String(init.body ?? "");
    this.requests.push({ method, path: `${url.pathname}${url.search}`, body, csrfToken: new Headers(init.headers).get("X-CSRF-Token") });
    if (url.pathname === "/api/v1/session") {
      this.sessionCounter += 1;
      return json({ csrf_token: `csrf-${this.sessionCounter}` });
    }
    if (url.pathname === "/api/v1/content" && method === "GET") {
      if (this.expireNextList) {
        this.expireNextList = false;
        return json({ error: "session_expired" }, 401);
      }
      if (this.listGate) {
        const gate = this.listGate;
        this.listGate = undefined;
        this.listGateStarted += 1;
        await gate;
        if (this.expireGatedList) {
          this.expireGatedList = false;
          return json({ error: "session_expired" }, 401);
        }
        if (this.failGatedList) {
          this.failGatedList = false;
          return json({ error: "unavailable" }, 503);
        }
      }
      if (this.failNextList) {
        this.failNextList = false;
        return json({ error: "unavailable" }, 503);
      }
      let items = [...this.items.values()];
      if (this.excludeExpired) items = items.filter((item) => new Date(item.expires_at).getTime() > Date.now());
      const q = url.searchParams.get("q")?.toLowerCase();
      const type = url.searchParams.get("type");
      const status = url.searchParams.get("status");
      if (q) items = items.filter((item) => item.working_title.toLowerCase().startsWith(q));
      if (type) items = items.filter((item) => item.type === type);
      if (status) items = items.filter((item) => item.status === status);
      return json({ items: items.map(summary) });
    }
    if (url.pathname === "/api/v1/content" && method === "POST") {
      if (this.createAuthFailure) {
        const failure = this.createAuthFailure;
        this.createAuthFailure = undefined;
        return json({ error: failure.code }, failure.status);
      }
      if (this.createGate) {
        const gate = this.createGate;
        this.createGate = undefined;
        await gate;
      }
      if (this.gatedCreateFailure) {
        const failure = this.gatedCreateFailure;
        this.gatedCreateFailure = undefined;
        return json({ error: failure.code }, failure.status);
      }
      const request = JSON.parse(body) as { type: ContentType; working_title: string; status: ContentStatus; operation_id: string; content: ContentDetail["content"] };
      const receipt = this.createReceipts.get(request.operation_id);
      if (receipt) return json(receipt.result, receipt.status);
      const id = `01KCREATED${String(this.items.size).padStart(16, "0")}`;
      const created = { ...detail(request.type, id), working_title: request.working_title, status: request.status, content: request.content };
      this.items.set(id, created);
      const result = { operation_id: request.operation_id, item_ids: [id], revisions: [1], expires_at: [expires], status: "created" };
      this.createReceipts.set(request.operation_id, { result, status: 201 });
      if (this.createResponseLostOnce) {
        this.createResponseLostOnce = false;
        throw new TypeError("response lost after commit");
      }
      return json(result, 201);
    }
    const match = url.pathname.match(/^\/api\/v1\/content\/([^/]+)(?:\/(archive|restore))?$/);
    if (!match) return json({ error: "not_found" }, 404);
    const id = decodeURIComponent(match[1]);
    if (method === "DELETE") {
      if (this.lifecycleAuthFailure) {
        const failure = this.lifecycleAuthFailure;
        this.lifecycleAuthFailure = undefined;
        return json({ error: failure.code }, failure.status);
      }
      const request = JSON.parse(body) as { operation_id: string };
      const receipt = this.deleteReceipts.get(request.operation_id);
      if (receipt) return json(receipt);
    }
    const item = this.items.get(id);
    if (!item) return json({ error: "content_not_found" }, 404);
    if (method === "GET") {
      if (this.expireNextDetail) {
        this.expireNextDetail = false;
        return json({ error: "session_expired" }, 401);
      }
      if (this.detailGate) {
        const gate = this.detailGate;
        this.detailGate = undefined;
        await gate;
        if (this.expireGatedDetail) {
          this.expireGatedDetail = false;
          return json({ error: "session_expired" }, 401);
        }
        if (this.failGatedDetail) {
          this.failGatedDetail = false;
          return json({ error: "unavailable" }, 503);
        }
      }
      if (this.detailFailures.delete(id)) return json({ error: "unavailable" }, 503);
      return json(item);
    }
    if (method === "PUT") {
      this.replaceBodies.push(body);
      if (this.replaceAuthFailure) {
        const failure = this.replaceAuthFailure;
        this.replaceAuthFailure = undefined;
        return json({ error: failure.code }, failure.status);
      }
      const request = JSON.parse(body) as { working_title: string; status: ContentStatus; revision: number; content: ContentDetail["content"] };
      if (this.conflictNext) {
        this.conflictNext = false;
        const current = { ...item, working_title: "Server changed title", revision: item.revision + 1 };
        this.items.set(id, current);
        return json({ error: "revision_conflict", current }, 409);
      }
      const saved = { ...item, working_title: request.working_title, status: request.status, content: request.content, revision: item.revision + 1, updated_at: new Date().toISOString() };
      if (saved.type === "youtube") {
        const content = saved.content as YouTubeContent;
        saved.content = { ...content, sections: content.sections.map((section, position) => ({ ...section, id: section.id ?? `01KSECTION${String(position).padStart(16, "0")}`, clientKey: section.id ?? section.clientKey, position })) };
      }
      this.items.set(id, saved);
      return json({ operation_id: "op", item_ids: [id], revisions: [saved.revision], expires_at: [expires], status: "updated" });
    }
    if (method === "POST" && match[2]) {
      if (this.lifecycleGate) {
        const gate = this.lifecycleGate;
        this.lifecycleGate = undefined;
        await gate;
      }
      if (this.gatedLifecycleFailure) {
        const failure = this.gatedLifecycleFailure;
        this.gatedLifecycleFailure = undefined;
        return json({ error: failure.code }, failure.status);
      }
      if (this.lifecycleConflictNext) {
        this.lifecycleConflictNext = false;
        const current = { ...item, working_title: "Server lifecycle title", revision: item.revision + 1 };
        this.items.set(id, current);
        return json({ error: "revision_conflict", current }, 409);
      }
      const archived = match[2] === "archive";
      const saved = { ...item, revision: item.revision + 1, archived_at: archived ? new Date().toISOString() : undefined };
      this.items.set(id, saved);
      if (this.failListAfterLifecycle) {
        this.failListAfterLifecycle = false;
        this.failNextList = true;
      }
      const request = JSON.parse(body) as { operation_id: string };
      const result = { operation_id: request.operation_id, item_ids: [id], revisions: [saved.revision], expires_at: [expires], status: archived ? "archived" : "restored" };
      this.lifecycleReceipts.set(request.operation_id, { signature: `${url.pathname}:${body}`, result });
      if (this.lifecycleResponseLostOnce) {
        this.lifecycleResponseLostOnce = false;
        throw new TypeError("response lost after commit");
      }
      return json(result);
    }
    if (method === "DELETE") {
      if (this.lifecycleGate) {
        const gate = this.lifecycleGate;
        this.lifecycleGate = undefined;
        await gate;
      }
      if (this.gatedLifecycleFailure) {
        const failure = this.gatedLifecycleFailure;
        this.gatedLifecycleFailure = undefined;
        return json({ error: failure.code }, failure.status);
      }
      if (this.lifecycleConflictNext) {
        this.lifecycleConflictNext = false;
        const current = { ...item, working_title: "Server lifecycle title", revision: item.revision + 1 };
        this.items.set(id, current);
        return json({ error: "revision_conflict", current }, 409);
      }
      const request = JSON.parse(body) as { operation_id: string };
      this.items.delete(id);
      if (this.failListAfterLifecycle) {
        this.failListAfterLifecycle = false;
        this.failNextList = true;
      }
      const result = { operation_id: request.operation_id, item_ids: [id], revisions: [item.revision + 1], expires_at: [expires], status: "deleted" };
      this.deleteReceipts.set(request.operation_id, result);
      if (this.deleteResponseLostOnce || this.lifecycleResponseLostOnce) {
        this.deleteResponseLostOnce = false;
        this.lifecycleResponseLostOnce = false;
        throw new TypeError("response lost after commit");
      }
      return json(result);
    }
    return json({ error: "not_found" }, 404);
  };
}

// Delete is the only lifecycle action, and it is confirmation-gated.
async function runDelete(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "Delete" }));
  await user.click(screen.getByRole("button", { name: "Delete permanently" }));
}

function deleteRequests(api: FakeAPI) {
  return api.requests.filter((request) => request.method === "DELETE");
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function expireRequestTimersImmediately() {
  const nativeSetTimeout = window.setTimeout.bind(window);
  vi.spyOn(window, "setTimeout").mockImplementation(((...arguments_: Parameters<typeof window.setTimeout>) => {
    const [handler, timeout, ...rest] = arguments_;
    return nativeSetTimeout(handler, timeout === 10_000 ? 0 : timeout, ...rest);
  }) as typeof window.setTimeout);
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
  // Views are real URLs now, so the path has to be reset between tests.
  window.history.pushState({}, "", "/");
});

describe("persistent ContentFlow workspace", () => {
  it.each([
    ["session", () => loadSession()],
    ["library", () => listContent()],
  ])("times out a stalled %s request", async (_label, request) => {
    expireRequestTimersImmediately();
    vi.stubGlobal("fetch", () => new Promise<Response>(() => undefined));

    await expect(request()).rejects.toThrow("request timed out");
  });

  it("keeps filters when URLSearchParams.size is unavailable", async () => {
    const api = new FakeAPI();
    vi.stubGlobal("fetch", api.fetch);
    const descriptor = Object.getOwnPropertyDescriptor(URLSearchParams.prototype, "size");
    Object.defineProperty(URLSearchParams.prototype, "size", { configurable: true, get: () => undefined });
    try {
      await listContent({ q: "You", type: "youtube", status: "draft" });
    } finally {
      if (descriptor) Object.defineProperty(URLSearchParams.prototype, "size", descriptor);
      else delete (URLSearchParams.prototype as { size?: number }).size;
    }

    expect(api.requests.at(-1)?.path).toBe("/api/v1/content?q=You&type=youtube&status=draft");
  });

  it("loads summary traffic and selected detail separately", async () => {
    const api = new FakeAPI();
    vi.stubGlobal("fetch", api.fetch);
    render(<Home />);

    expect(await screen.findByDisplayValue("YouTube one")).toBeTruthy();
    expect(api.requests.some((request) => request.path === "/api/v1/content")).toBe(true);
    expect(api.requests.some((request) => request.path.includes("/api/v1/content/01K"))).toBe(true);
    const list = [...api.items.values()].map(summary);
    expect(JSON.stringify(list)).not.toContain("transcript");
    expect(JSON.stringify(list)).not.toContain("sections");
  });

  it("ignores a stale detail response that started before autosave completed", async () => {
    const api = new FakeAPI([detail("youtube"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const title = await screen.findByDisplayValue("YouTube one");

    await user.type(title, " Z");
    await user.click(screen.getByRole("button", { name: /^X one/ }));
    await screen.findByRole("heading", { name: "X one" });
    let releaseDetail: () => void = () => undefined;
    api.detailGate = new Promise<void>((resolve) => { releaseDetail = resolve; });
    await user.click(screen.getByRole("button", { name: /^YouTube one/ }));
    expect(await screen.findByDisplayValue("YouTube one Z")).toBeTruthy();
    await waitFor(() => expect(api.replaceBodies).toHaveLength(1), { timeout: 2500 });
    releaseDetail();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.getByDisplayValue("YouTube one Z")).toBeTruthy();
    expect(api.items.get(detail("youtube").id)?.working_title).toBe("YouTube one Z");
  });

  it.each([
    [401, "session_expired"],
    [403, "csrf_check_failed"],
  ])("preserves and resumes an autosave after a %i %s response", async (status, code) => {
    const api = new FakeAPI([detail("linkedin")]);
    api.replaceAuthFailure = { status, code };
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("LinkedIn post");

    await user.clear(body);
    await user.type(body, "Queued through reauthentication");
    expect(await screen.findByRole("heading", { name: "Your session expired" }, { timeout: 2500 })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open sign in" }).getAttribute("target")).toBe("_blank");

    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));
    expect(await screen.findByDisplayValue("Queued through reauthentication")).toBeTruthy();
    await waitFor(() => expect((api.items.values().next().value?.content as { body: string })?.body).toBe("Queued through reauthentication"), { timeout: 2500 });
    expect(api.replaceBodies).toHaveLength(2);
    expect(api.replaceBodies[1]).toBe(api.replaceBodies[0]);
    expect(api.requests.filter((request) => request.method === "PUT").map((request) => request.csrfToken)).toEqual(["csrf-1", "csrf-2"]);
    expect(api.sessionCounter).toBe(2);
  });

  it("reauthenticates after a successful autosave library refresh expires", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("LinkedIn post");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.expireNextList = true;

    await user.type(body, "persisted");
    expect(await screen.findByRole("heading", { name: "Your session expired" }, { timeout: 2500 })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    expect(await screen.findByDisplayValue("persisted")).toBeTruthy();
    expect(api.requests.filter((request) => request.method === "PUT")).toHaveLength(1);
    expect(api.sessionCounter).toBe(2);
  });

  it("keeps an in-flight lifecycle failure visible after unrelated reauthentication", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    api.gatedLifecycleFailure = { status: 503, code: "unavailable" };

    await runDelete(user);
    api.expireNextList = true;
    await user.type(screen.getByLabelText("Search content titles"), "Linked");
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull());

    releaseLifecycle();

    expect(await screen.findByText("The item could not be deleted.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete" })).not.toHaveProperty("disabled", true);
  });

  it("retries a delayed old-session lifecycle auth failure after another request renews the session", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    api.gatedLifecycleFailure = { status: 403, code: "csrf_check_failed" };

    await runDelete(user);
    api.expireNextList = true;
    await user.type(screen.getByLabelText("Search content titles"), "Linked");
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull());

    releaseLifecycle();

    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
    const archives = deleteRequests(api);
    expect(archives).toHaveLength(2);
    expect(JSON.parse(archives[1].body).operation_id).toBe(JSON.parse(archives[0].body).operation_id);
  });

  it("sends title search, type, and status filters to the list API", async () => {
    const api = new FakeAPI();
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByDisplayValue("YouTube one");

    await user.type(screen.getByLabelText("Search content titles"), "You");
    await user.click(screen.getByRole("button", { name: /^YouTube1$/ }));
    await user.click(screen.getByRole("button", { name: "Draft" }));
    await waitFor(() => expect(api.requests.some((request) => request.path.includes("q=You") && request.path.includes("type=youtube") && request.path.includes("status=draft"))).toBe(true));
  });

  it("shows filter refresh failures in the ready workspace", async () => {
    const api = new FakeAPI();
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByDisplayValue("YouTube one");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.failNextList = true;

    await user.type(screen.getByLabelText("Search content titles"), "You");

    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "The library filters could not be refreshed.");
    await user.clear(screen.getByLabelText("Search content titles"));
    await user.type(screen.getByLabelText("Search content titles"), "Linked");
    expect(await screen.findByText("LinkedIn one")).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });

  it("reauthenticates and retries filters after a 401", async () => {
    const api = new FakeAPI();
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByDisplayValue("YouTube one");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.expireNextList = true;

    await user.type(screen.getByLabelText("Search content titles"), "You");
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    expect(await screen.findByText("YouTube one")).toBeTruthy();
    await waitFor(() => expect(api.requests.filter((request) => request.path === "/api/v1/content?q=You").length).toBeGreaterThanOrEqual(2));
    expect(api.sessionCounter).toBe(2);
  });

  it("ignores an old detail 401 after the session has been renewed", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseDetail: () => void = () => undefined;
    api.detailGate = new Promise<void>((resolve) => { releaseDetail = resolve; });
    api.expireGatedDetail = true;

    await user.click(screen.getByRole("button", { name: /^X one/ }));
    api.expireNextList = true;
    await user.type(screen.getByLabelText("Search content titles"), "X");
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull());
    expect(await screen.findByRole("heading", { name: "X one" })).toBeTruthy();

    releaseDetail();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
    expect(api.sessionCounter).toBe(2);
  });

  it("replays an in-flight filter request invalidated by session recovery", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseList: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseList = resolve; });

    await user.type(screen.getByLabelText("Search content titles"), "X");
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    api.expireNextDetail = true;
    await user.click(screen.getByRole("button", { name: /^X one/ }));
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    expect(await screen.findByRole("button", { name: /^X one/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^LinkedIn one/ })).toBeNull();
    releaseList();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.getByLabelText("Search content titles")).toHaveProperty("value", "X");
    expect(screen.getByRole("button", { name: /^X one/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^LinkedIn one/ })).toBeNull();
    expect(api.requests.filter((request) => request.path === "/api/v1/content?q=X").length).toBeGreaterThanOrEqual(2);
  });

  it("ignores a failed filter request after a newer filter succeeds", async () => {
    const api = new FakeAPI([detail("youtube"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByDisplayValue("YouTube one");
    let releaseList: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseList = resolve; });
    api.failGatedList = true;

    await user.type(screen.getByLabelText("Search content titles"), "You");
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.clear(screen.getByLabelText("Search content titles"));
    await user.type(screen.getByLabelText("Search content titles"), "X");
    expect(await screen.findByText("X one")).toBeTruthy();
    releaseList();

    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(await screen.findByText("X one")).toBeTruthy();
  });

  // YouTube is the only type with an author-editable title, so title-specific
  // behaviour is exercised there.
  it("keeps queued title changes in an older library response", async () => {
    const api = new FakeAPI([detail("youtube"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByDisplayValue("YouTube one");
    let releaseList: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseList = resolve; });

    await user.type(screen.getByLabelText("Search content titles"), " strass ");
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.clear(screen.getByLabelText("Working title"));
    await user.type(screen.getByLabelText("Working title"), "Ｓｔｒａße local");
    await user.selectOptions(screen.getByLabelText("Content status"), "ready");
    releaseList();

    const card = await screen.findByRole("button", { name: /^Ｓｔｒａße local/ });
    expect(card).toHaveProperty("textContent", expect.stringContaining("Ready"));
    expect((screen.getByLabelText("Working title") as HTMLInputElement).value).toBe("Ｓｔｒａße local");
  });

  it("keeps queued drafts visible for an all-whitespace title query", async () => {
    const api = new FakeAPI([detail("youtube")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const title = await screen.findByDisplayValue("YouTube one");

    await user.type(title, " queued");
    await user.type(screen.getByLabelText("Search content titles"), "   ");

    expect(await screen.findByRole("button", { name: /^YouTube one queued/ })).toBeTruthy();
    expect(api.requests.some((request) => request.path.includes("q="))).toBe(false);
  });

  it("clears incompatible filters before selecting a created item", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.click(screen.getByRole("button", { name: "Published" }));
    expect(await screen.findByText("No content found")).toBeTruthy();
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Email/ }));

    expect(await screen.findByRole("heading", { name: /^Email · / })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Email · / })).toBeTruthy();
    expect((screen.getByLabelText("Search content titles") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("button", { name: "All" })).toHaveProperty("className", expect.stringContaining("active"));
  });

  it("shows a library refresh failure after a successful autosave", async () => {
    const api = new FakeAPI();
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const title = await screen.findByDisplayValue("YouTube one");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.failNextList = true;

    await user.type(title, " saved");

    expect(await screen.findByRole("alert", {}, { timeout: 2500 })).toHaveProperty("textContent", "The library could not be refreshed after saving.");
    await user.type(title, " recovered");
    await waitFor(() => expect(screen.queryByText("The library could not be refreshed after saving.")).toBeNull(), { timeout: 2500 });
  });

  it("does not let an older background refresh overwrite newer filters", async () => {
    const api = new FakeAPI([detail("youtube"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const title = await screen.findByDisplayValue("YouTube one");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseList: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseList = resolve; });
    await user.type(title, " refreshed");
    await waitFor(() => expect(api.listGateStarted).toBe(1), { timeout: 2500 });

    await user.type(screen.getByLabelText("Search content titles"), "X");
    await waitFor(() => expect(api.requests.some((request) => request.path === "/api/v1/content?q=X")).toBe(true));
    expect(await screen.findByText("X one")).toBeTruthy();
    releaseList();

    expect(await screen.findByText("X one")).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole("button", { name: /^YouTube one refreshed/ })).toBeNull());
  });

  it("keeps the unfiltered summary cache current when filters overtake a create refresh", async () => {
    const api = new FakeAPI([]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseCreateRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseCreateRefresh = resolve; });

    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^LinkedIn/ }));
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.type(screen.getByLabelText("Search content titles"), "No match");
    await waitFor(() => expect(api.requests.some((request) => request.path === "/api/v1/content?q=No+match")).toBe(true));

    expect(screen.getByRole("button", { name: /^LinkedIn1$/ })).toBeTruthy();
    releaseCreateRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.getByRole("button", { name: /^LinkedIn1$/ })).toBeTruthy();
  });

  it.each([
    ["YouTube", "YouTube transcript: what was actually said"],
    ["LinkedIn", "LinkedIn post"],
    ["X", "X post"],
    ["Instagram", "Instagram script"],
    ["TikTok", "TikTok script"],
    ["Email", "Email subject"],
    ["Substack", "Substack headline"],
  ])("creates and opens the %s editor", async (label, editorLabel) => {
    const api = new FakeAPI([]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: new RegExp(`^${label}`) }));
    expect(await screen.findByLabelText(editorLabel)).toBeTruthy();
  });

  it("serializes create requests while the first item is pending", async () => {
    const api = new FakeAPI([]);
    let release: () => void = () => undefined;
    api.createGate = new Promise<void>((resolve) => { release = resolve; });
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    const choice = within(screen.getByRole("dialog")).getByRole("button", { name: /^LinkedIn/ });

    await user.dblClick(choice);
    expect((choice as HTMLButtonElement).disabled).toBe(true);
    expect(api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content")).toHaveLength(1);
    release();

    expect(await screen.findByLabelText("LinkedIn post")).toBeTruthy();
    expect(api.items).toHaveProperty("size", 1);
  });

  it("times out a stalled create and retries with the same operation ID", async () => {
    const api = new FakeAPI([]);
    api.createGate = new Promise<void>(() => undefined);
    vi.stubGlobal("fetch", api.fetch);
    expireRequestTimersImmediately();
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    const choice = within(screen.getByRole("dialog")).getByRole("button", { name: /^LinkedIn/ });

    await user.click(choice);
    expect(await within(screen.getByRole("dialog")).findByRole("alert")).toHaveProperty("textContent", "The new item could not be created.");
    expect((choice as HTMLButtonElement).disabled).toBe(false);
    await user.click(choice);
    expect(await screen.findByLabelText("LinkedIn post")).toBeTruthy();

    const creates = api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content").map((request) => JSON.parse(request.body).operation_id);
    expect(creates).toEqual([creates[0], creates[0]]);
    expect(api.items).toHaveProperty("size", 1);
  });

  it.each([
    [401, "session_expired"],
    [403, "csrf_check_failed"],
  ])("reauthenticates a create after a %i %s response and retries with the same operation ID", async (status, code) => {
    const api = new FakeAPI([]);
    api.createAuthFailure = { status, code };
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^LinkedIn/ }));
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));
    expect(await screen.findByLabelText("LinkedIn post")).toBeTruthy();

    const creates = api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content").map((request) => JSON.parse(request.body).operation_id);
    expect(creates).toEqual([creates[0], creates[0]]);
    expect(api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content").map((request) => request.csrfToken)).toEqual(["csrf-1", "csrf-2"]);
    expect(api.items).toHaveProperty("size", 1);
  });

  it("reauthenticates after a successful create library refresh expires without replaying create", async () => {
    const api = new FakeAPI([]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.expireNextList = true;

    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^LinkedIn/ }));
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    expect(await screen.findByLabelText("LinkedIn post")).toBeTruthy();
    expect(api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content")).toHaveLength(1);
    expect(api.sessionCounter).toBe(2);
  });

  it("retries a delayed old-session create auth failure after another request renews the session", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseCreate: () => void = () => undefined;
    api.createGate = new Promise<void>((resolve) => { releaseCreate = resolve; });
    api.gatedCreateFailure = { status: 401, code: "session_expired" };

    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    api.expireNextList = true;
    await user.type(screen.getByLabelText("Search content titles"), "Linked");
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull());

    releaseCreate();

    expect(await screen.findByLabelText("X post")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
    const creates = api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content");
    expect(creates).toHaveLength(2);
    expect(JSON.parse(creates[1].body).operation_id).toBe(JSON.parse(creates[0].body).operation_id);
  });

  it("reuses the create operation ID after a committed response is lost", async () => {
    const api = new FakeAPI([]);
    api.createResponseLostOnce = true;
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    const choice = within(screen.getByRole("dialog")).getByRole("button", { name: /^LinkedIn/ });

    await user.click(choice);
    expect(await within(screen.getByRole("dialog")).findByRole("alert")).toHaveProperty("textContent", "The new item could not be created.");
    expect(api.items).toHaveProperty("size", 1);
    await user.click(choice);

    expect(await screen.findByLabelText("LinkedIn post")).toBeTruthy();
    expect(api.items).toHaveProperty("size", 1);
    const creates = api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content").map((request) => JSON.parse(request.body).operation_id);
    expect(creates).toEqual([creates[0], creates[0]]);
  });

  it("acknowledges a successful create when only the library refresh fails", async () => {
    const api = new FakeAPI([]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByText("Your workspace is empty");
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    api.failNextList = true;
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Email/ }));

    expect(await screen.findByLabelText("Email subject")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "The item was created, but the library could not be refreshed.");
    expect(api.requests.filter((request) => request.method === "POST" && request.path === "/api/v1/content")).toHaveLength(1);
  });

  it("keeps the spoken transcript independent from planned script and persists after reload", async () => {
    const item = detail("youtube");
    (item.content as YouTubeContent).sections = [{ clientKey: "intro", position: 0, title: "Intro", body: "Planned opening" }];
    const api = new FakeAPI([item]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    const first = render(<Home />);
    const transcript = await screen.findByLabelText("YouTube transcript: what was actually said");
    await user.type(transcript, "Words actually spoken");
    expect((screen.getByLabelText("Intro script") as HTMLTextAreaElement).value).toBe("Planned opening");
    await waitFor(() => expect(api.replaceBodies.length).toBe(1), { timeout: 2500 });
    first.unmount();

    render(<Home />);
    expect(await screen.findByDisplayValue("Words actually spoken")).toBeTruthy();
    expect((screen.getByLabelText("Intro script") as HTMLTextAreaElement).value).toBe("Planned opening");
  });

  it("permanently deletes through the API", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await runDelete(user);

    expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
    expect(deleteRequests(api)).toHaveLength(1);
    expect(api.items.size).toBe(0);
  });

  it("clears the editor when deleting the final visible filtered item", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.type(screen.getByLabelText("Search content titles"), "Linked");
    await waitFor(() => expect(screen.queryByRole("button", { name: /^X one/ })).toBeNull());
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));

    expect(await screen.findByText("No content found")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Choose an item" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "X one" })).toBeNull();
  });

  it("does not select a hidden replacement while a filter request is pending", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseList: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseList = resolve; });

    await user.type(screen.getByLabelText("Search content titles"), "Nobody");
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));

    expect(await screen.findByText("No content found")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Choose an item" })).toBeTruthy();
    releaseList();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.queryByRole("heading", { name: "X one" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Choose an item" })).toBeTruthy();
  });

  it("uses filters changed after delete starts before choosing a replacement", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseDelete: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseDelete = resolve; });

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await user.type(screen.getByLabelText("Search content titles"), "Nobody");
    releaseDelete();

    expect(await screen.findByText("No content found")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Choose an item" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "X one" })).toBeNull();
  });

  it("lets a newer filter refresh choose a replacement for a deleted selection", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseDeleteRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseDeleteRefresh = resolve; });

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.type(screen.getByLabelText("Search content titles"), "X");

    expect(await screen.findByRole("heading", { name: "X one" })).toBeTruthy();
    releaseDeleteRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.getByRole("heading", { name: "X one" })).toBeTruthy();
  });

  it.each([
    [401, "session_expired"],
    [403, "csrf_check_failed"],
  ])("reauthenticates a lifecycle action after a %i %s response and retries with the same operation ID", async (status, code) => {
    const api = new FakeAPI([detail("linkedin")]);
    api.lifecycleAuthFailure = { status, code };
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await runDelete(user);
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    const archives = deleteRequests(api).map((request) => JSON.parse(request.body).operation_id);
    expect(archives).toEqual([archives[0], archives[0]]);
    expect(deleteRequests(api).map((request) => request.csrfToken)).toEqual(["csrf-1", "csrf-2"]);
    expect(api.sessionCounter).toBe(2);
  });

  it("times out a stalled delete and retries with the same operation ID", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    api.lifecycleGate = new Promise<void>(() => undefined);
    vi.stubGlobal("fetch", api.fetch);
    expireRequestTimersImmediately();
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "The item could not be deleted.");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));

    expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
    const deletes = api.requests.filter((request) => request.method === "DELETE").map((request) => JSON.parse(request.body).operation_id);
    expect(deletes).toEqual([deletes[0], deletes[0]]);
  });

  it("keeps a successful delete when the library refresh fails", async () => {
    const item = detail("linkedin");
    const api = new FakeAPI([item]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    api.failListAfterLifecycle = true;

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));

    expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "The item was deleted, but the library could not be refreshed.");
    expect(api.items.has(item.id)).toBe(false);
    expect(screen.queryByLabelText("Content status")).toBeNull();
  });

  it("reauthenticates after a successful delete library refresh expires without replaying delete", async () => {
    const item = detail("linkedin");
    const api = new FakeAPI([item]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.expireNextList = true;

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
    expect(api.requests.filter((request) => request.method === "DELETE")).toHaveLength(1);
    expect(api.sessionCounter).toBe(2);
  });

  it("reuses the delete operation ID after a committed response is lost", async () => {
    const item = detail("linkedin");
    const api = new FakeAPI([item]);
    api.deleteResponseLostOnce = true;
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "The item could not be deleted.");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));

    expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
    expect(screen.queryByLabelText("Content status")).toBeNull();
    const deletes = api.requests.filter((request) => request.method === "DELETE").map((request) => JSON.parse(request.body).operation_id);
    expect(deletes).toEqual([deletes[0], deletes[0]]);
  });

  it("preserves a newer selection after a delayed delete completes", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x"), detail("email")]);
    let releaseDelete: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseDelete = resolve; });
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await user.click(screen.getByRole("button", { name: /X one/ }));
    await screen.findByRole("heading", { name: "X one" });
    releaseDelete();

    await waitFor(() => expect(screen.queryByRole("button", { name: /LinkedIn one/ })).toBeNull());
    expect(screen.getByRole("heading", { name: "X one" })).toBeTruthy();
  });

  it("preserves an item created while the previous final item is being deleted", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    let releaseDelete: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseDelete = resolve; });
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    expect(await screen.findByLabelText("X post")).toBeTruthy();
    releaseDelete();

    await waitFor(() => expect(screen.queryByRole("button", { name: /LinkedIn one/ })).toBeNull());
    expect(screen.getByRole("heading", { name: /^X · / })).toBeTruthy();
    expect(screen.getByLabelText("X post")).toBeTruthy();
  });

  it("refreshes a clean cached item when it is reopened", async () => {
    const linkedin = detail("linkedin");
    const api = new FakeAPI([linkedin, detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("LinkedIn post");
    await user.type(body, "saved");
    await waitFor(() => expect(api.replaceBodies).toHaveLength(1), { timeout: 2500 });
    await user.click(screen.getByRole("button", { name: /X one/ }));
    await screen.findByRole("heading", { name: "X one" });
    const current = api.items.get(linkedin.id)!;
    api.items.set(linkedin.id, { ...current, working_title: "Changed in another tab", revision: current.revision + 1 });

    await user.click(screen.getByRole("button", { name: /^LinkedIn one/ }));

    expect(await screen.findByRole("heading", { name: "Changed in another tab" })).toBeTruthy();
  });

  it("shows detail-load failures and retries the selected item", async () => {
    const item = detail("email");
    const api = new FakeAPI([item]);
    api.detailFailures.add(item.id);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);

    expect(await screen.findByRole("heading", { name: "Could not open this item" })).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("The selected item could not be loaded.");
    await user.click(screen.getByRole("button", { name: "Retry loading item" }));

    expect(await screen.findByRole("heading", { name: "Email one" })).toBeTruthy();
  });

  it("reauthenticates and retries an uncached detail request", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    api.expireNextDetail = true;

    await user.click(screen.getByRole("button", { name: /X one/ }));
    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "I’ve signed in" }));

    expect(await screen.findByRole("heading", { name: "X one" })).toBeTruthy();
    expect(api.sessionCounter).toBe(2);
  });

  it("times out a stalled detail request and exposes retry", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    expireRequestTimersImmediately();
    api.detailGate = new Promise<void>(() => undefined);

    await user.click(screen.getByRole("button", { name: /X one/ }));
    expect(await screen.findByRole("heading", { name: "Could not open this item" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Retry loading item" }));

    expect(await screen.findByRole("heading", { name: "X one" })).toBeTruthy();
  });

  it("locks edits while a delete request is in flight", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    const body = screen.getByLabelText("LinkedIn post") as HTMLTextAreaElement;

    let releaseDelete: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseDelete = resolve; });
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await waitFor(() => expect(document.querySelector(".document-heading")?.hasAttribute("inert")).toBe(true));
    await user.clear(body);
    await user.type(body, "Must remain blocked");
    expect(body.value).toBe("");
    expect(screen.getByRole("heading", { name: "LinkedIn one" })).toBeTruthy();
    releaseDelete();
    expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
  });

  it("ignores an overtaken lifecycle mutation failure after create selects another item", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    api.gatedLifecycleFailure = { status: 503, code: "unavailable" };

    await runDelete(user);
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    expect(await screen.findByLabelText("X post")).toBeTruthy();
    releaseLifecycle();
    await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).not.toHaveProperty("disabled", true));

    expect(screen.queryByText("The item could not be deleted.")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
    expect(screen.getByRole("heading", { name: /^X · / })).toBeTruthy();
  });

  it("ignores an overtaken lifecycle mutation failure after an existing item is selected", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    api.gatedLifecycleFailure = { status: 503, code: "unavailable" };

    await runDelete(user);
    await user.click(screen.getByRole("button", { name: /^X one/ }));
    await screen.findByRole("heading", { name: "X one" });
    releaseLifecycle();
    await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).not.toHaveProperty("disabled", true));

    expect(screen.queryByText("The item could not be deleted.")).toBeNull();
    expect(screen.getByRole("heading", { name: "X one" })).toBeTruthy();
  });

  it("keeps an overtaken lifecycle 401 retry explicitly owned by its item", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    api.gatedLifecycleFailure = { status: 401, code: "session_expired" };

    await runDelete(user);
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    expect(await screen.findByLabelText("X post")).toBeTruthy();
    releaseLifecycle();

    expect(await screen.findByRole("heading", { name: "Your session expired" })).toBeTruthy();
    expect(screen.getByText("Your delete action for “LinkedIn one” is waiting. Sign in in a new tab, then return here to retry it.")).toBeTruthy();
  });

  it("surfaces an overtaken lifecycle conflict and keeps both resolutions available", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    api.lifecycleConflictNext = true;

    await runDelete(user);
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    expect(await screen.findByLabelText("X post")).toBeTruthy();
    releaseLifecycle();

    expect(await screen.findByText("Review the delete conflict for “LinkedIn one” before continuing.", { exact: false })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Review item" }));
    await screen.findByRole("heading", { name: "This item changed elsewhere" });
    expect(screen.getByRole("button", { name: "Cancel action" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry delete" })).toBeTruthy();
  });

  it("does not label an in-flight lifecycle action as a conflict", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });

    await runDelete(user);
    await user.click(screen.getByRole("button", { name: /^X one/ }));
    await screen.findByRole("heading", { name: "X one" });

    expect(screen.queryByText(/Review the archive conflict/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Review item" })).toBeNull();
    releaseLifecycle();
    await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).not.toHaveProperty("disabled", true));
  });

  it.each([
    ["archive", "failure"],
    ["archive", "session"],
    ["delete", "failure"],
    ["delete", "session"],
  ])("ignores an older %s library %s after a newer filter succeeds", async (action, outcome) => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseRefresh = resolve; });
    if (outcome === "failure") api.failGatedList = true;
    else api.expireGatedList = true;

    if (action === "archive") {
      await runDelete(user);
    } else {
      await user.click(screen.getByRole("button", { name: "Delete" }));
      await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    }
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.type(screen.getByLabelText("Search content titles"), "X");
    await waitFor(() => expect(api.requests.some((request) => request.method === "GET" && request.path === "/api/v1/content?q=X")).toBe(true));
    releaseRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.queryByText(/but the library could not be refreshed/)).toBeNull();
    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
  });

  it("keeps an in-flight filter result when a newer lifecycle mutation fails", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseFilter: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseFilter = resolve; });

    await user.type(screen.getByLabelText("Search content titles"), "X");
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    api.lifecycleGate = Promise.resolve();
    api.gatedLifecycleFailure = { status: 503, code: "unavailable" };
    await runDelete(user);
    expect(await screen.findByText("The item could not be deleted.")).toBeTruthy();
    releaseFilter();

    await waitFor(() => expect(screen.getByRole("button", { name: /^X one/ })).toBeTruthy());
    expect(screen.queryByRole("button", { name: /^LinkedIn one/ })).toBeNull();
    expect((screen.getByLabelText("Search content titles") as HTMLInputElement).value).toBe("X");
  });

  it("keeps an in-flight filter result when a newer create fails", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseFilter: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseFilter = resolve; });

    await user.type(screen.getByLabelText("Search content titles"), "X");
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    api.createGate = Promise.resolve();
    api.gatedCreateFailure = { status: 503, code: "unavailable" };
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    expect(await within(screen.getByRole("dialog")).findByText("The new item could not be created.")).toBeTruthy();
    releaseFilter();

    await waitFor(() => expect(screen.getByRole("button", { name: /^X one/ })).toBeTruthy());
    expect(screen.queryByRole("button", { name: /^LinkedIn one/ })).toBeNull();
    expect((screen.getByLabelText("Search content titles") as HTMLInputElement).value).toBe("X");
  });

  it("clears an older filter error after a successful lifecycle replacement refresh", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    api.failNextList = true;

    await user.type(screen.getByLabelText("Search content titles"), "X");
    expect(await screen.findByText("The library filters could not be refreshed.")).toBeTruthy();
    await runDelete(user);
    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });

    await waitFor(() => expect(screen.queryByText("The library filters could not be refreshed.")).toBeNull());
    expect(screen.getByRole("button", { name: /^X one/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^LinkedIn one/ })).toBeNull();
  });

  it("ignores an older delete refresh failure once create starts", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseDeleteRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseDeleteRefresh = resolve; });
    api.failGatedList = true;

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^XPost/ }));
    expect(await screen.findByLabelText("X post")).toBeTruthy();
    releaseDeleteRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.queryByText("The item was deleted, but the library could not be refreshed.")).toBeNull();
    expect(screen.getByRole("heading", { name: /^X · / })).toBeTruthy();
  });

  it("ignores an older delete refresh failure once another item action starts", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseDeleteRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseDeleteRefresh = resolve; });
    api.failGatedList = true;

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await screen.findByRole("heading", { name: "X one" });
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    let releaseXArchive: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseXArchive = resolve; });
    await runDelete(user);
    releaseDeleteRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.queryByText("The item was deleted, but the library could not be refreshed.")).toBeNull();

    releaseXArchive();
    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("ignores an older autosave refresh failure once a lifecycle action starts", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("LinkedIn post");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseAutosaveRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseAutosaveRefresh = resolve; });
    api.failGatedList = true;

    await user.type(body, "saved");
    await waitFor(() => expect(api.listGateStarted).toBe(1), { timeout: 2500 });
    await user.click(screen.getByRole("button", { name: /^X one/ }));
    await screen.findByRole("heading", { name: "X one" });
    let releaseXArchive: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseXArchive = resolve; });
    await runDelete(user);
    releaseAutosaveRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.queryByText("The library could not be refreshed after saving.")).toBeNull();

    releaseXArchive();
    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    expect(screen.queryByText("The library could not be refreshed after saving.")).toBeNull();
  });

  it("ignores an older autosave refresh 401 after a newer lifecycle action succeeds", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("LinkedIn post");
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseAutosaveRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseAutosaveRefresh = resolve; });
    api.expireGatedList = true;

    await user.type(body, "saved");
    await waitFor(() => expect(api.listGateStarted).toBe(1), { timeout: 2500 });
    await user.click(screen.getByRole("button", { name: /^X one/ }));
    await screen.findByRole("heading", { name: "X one" });
    await runDelete(user);
    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    releaseAutosaveRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("ignores an older delete refresh 401 after a newer lifecycle action succeeds", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await waitFor(() => expect(api.requests.filter((request) => request.method === "GET" && request.path === "/api/v1/content").length).toBeGreaterThanOrEqual(2));
    let releaseDeleteRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseDeleteRefresh = resolve; });
    api.expireGatedList = true;

    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete permanently" }));
    await screen.findByRole("heading", { name: "X one" });
    await waitFor(() => expect(api.listGateStarted).toBe(1));
    await runDelete(user);
    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    releaseDeleteRefresh();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(screen.queryByRole("heading", { name: "Your session expired" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("uses the latest filters after a delayed lifecycle action", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    let releaseLifecycle: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { releaseLifecycle = resolve; });
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await runDelete(user);
    await user.type(screen.getByLabelText("Search content titles"), "X");
    expect(await screen.findByText("X one")).toBeTruthy();
    releaseLifecycle();
    await waitFor(() => expect(deleteRequests(api).length).toBeGreaterThanOrEqual(1), { timeout: 2500 });

    await waitFor(() => expect(screen.getByText("X one")).toBeTruthy());
    expect(screen.queryByRole("button", { name: /^LinkedIn one/ })).toBeNull();
  });

  it("serializes lifecycle requests across items", async () => {
    const api = new FakeAPI([detail("linkedin"), detail("x")]);
    let release: () => void = () => undefined;
    api.lifecycleGate = new Promise<void>((resolve) => { release = resolve; });
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await runDelete(user);
    await user.click(screen.getByRole("button", { name: /X one/ }));
    await screen.findByRole("heading", { name: "X one" });

    const secondArchive = screen.getByRole("button", { name: "Delete" });
    expect((secondArchive as HTMLButtonElement).disabled).toBe(true);
    await user.click(secondArchive);
    expect(deleteRequests(api)).toHaveLength(1);
    release();

    await waitFor(() => expect((secondArchive as HTMLButtonElement).disabled).toBe(false));
  });

  it.each([
    ["Cancel action", false],
    ["Retry delete", true],
  ])("resolves lifecycle conflicts with %s", async (resolution, removesItem) => {
    const api = new FakeAPI([detail("linkedin")]);
    api.lifecycleConflictNext = true;
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await runDelete(user);
    expect(await screen.findByRole("heading", { name: "This item changed elsewhere" })).toBeTruthy();
    const body = screen.getByLabelText("LinkedIn post") as HTMLTextAreaElement;
    await user.clear(body);
    await user.type(body, "Must not be lost");
    expect(body.value).toBe("");
    await user.click(screen.getByRole("button", { name: resolution }));

    if (removesItem) expect(await screen.findByText("Your workspace is empty")).toBeTruthy();
    else expect(await screen.findByRole("button", { name: "Delete" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "This item changed elsewhere" })).toBeNull();
  });

  it("shows server and unsaved local content after a conflict", async () => {
    const api = new FakeAPI([detail("x")]);
    api.conflictNext = true;
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("X post");
    await user.clear(body);
    await user.type(body, "Local changed title");

    const conflictHeading = await screen.findByRole("heading", { name: "This item changed elsewhere" }, { timeout: 2500 });
    const conflictPanel = conflictHeading.closest("section")!;
    expect(within(conflictPanel).getByText("Server version")).toBeTruthy();
    expect(within(conflictPanel).getByText("Your unsaved version")).toBeTruthy();
    expect(within(conflictPanel).getByText(/Server changed title/)).toBeTruthy();
    expect(within(conflictPanel).getByText(/Local changed title/)).toBeTruthy();
  });

  it("reapplies active filters after accepting the server conflict version", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    api.conflictNext = true;
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    const body = await screen.findByLabelText("LinkedIn post");
    await user.type(screen.getByLabelText("Search content titles"), "Linked");
    await waitFor(() => expect(api.requests.filter((request) => request.path === "/api/v1/content?q=Linked")).toHaveLength(1));
    await user.clear(body);
    await user.type(body, "Local changed title");

    await screen.findByRole("heading", { name: "This item changed elsewhere" }, { timeout: 2500 });
    await user.click(screen.getByRole("button", { name: "Use server version" }));

    await waitFor(() => expect(api.requests.filter((request) => request.path === "/api/v1/content?q=Linked").length).toBeGreaterThanOrEqual(2));
    expect(await screen.findByText("No content found")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Server changed title/ })).toBeNull();
    expect((screen.getByLabelText("Search content titles") as HTMLInputElement).value).toBe("Linked");
  });


  it("routes between the workspace, calendar, and settings views", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });

    await user.click(screen.getByRole("button", { name: /^Calendar/ }));
    expect(await screen.findByRole("heading", { name: "Calendar" })).toBeTruthy();
    expect(window.location.pathname).toBe("/calendar");

    await user.click(screen.getByRole("button", { name: /^Settings/ }));
    expect(await screen.findByRole("heading", { name: "Settings" })).toBeTruthy();
    expect(window.location.pathname).toBe("/settings");
    expect(screen.queryByRole("heading", { name: "Calendar" })).toBeNull();

    await user.click(screen.getByRole("button", { name: /^All content/ }));
    expect(await screen.findByRole("heading", { name: "LinkedIn one" })).toBeTruthy();
    expect(window.location.pathname).toBe("/");
  });

  it("opens the calendar directly from its URL", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    window.history.pushState({}, "", "/calendar");
    render(<Home />);

    expect(await screen.findByRole("heading", { name: "Calendar" })).toBeTruthy();
  });

  it("schedules an unscheduled item onto a day and writes the date", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await user.click(screen.getByRole("button", { name: /^Calendar/ }));
    await screen.findByRole("heading", { name: "Calendar" });

    const chip = screen.getByRole("button", { name: /LinkedIn one, unscheduled/ });
    const today = new Date();
    const cell = screen.getByLabelText(today.toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" }));
    // jsdom has no DataTransfer, so stand in for the one a real drag carries.
    const carried = new Map<string, string>();
    const dataTransfer = {
      setData: (format: string, value: string) => { carried.set(format, value); },
      getData: (format: string) => carried.get(format) ?? "",
      effectAllowed: "move",
      dropEffect: "move",
    };
    fireEvent.dragStart(chip, { dataTransfer });
    fireEvent.dragOver(cell, { dataTransfer });
    fireEvent.drop(cell, { dataTransfer });

    await waitFor(() => expect(api.replaceBodies.length).toBeGreaterThanOrEqual(1), { timeout: 2500 });
    const scheduled = (JSON.parse(api.replaceBodies[0]) as { scheduled_at?: string }).scheduled_at;
    expect(scheduled).toBeTruthy();
    expect(new Date(scheduled!).toDateString()).toBe(today.toDateString());
  });

  it("hides a content type from the sidebar through the settings page", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await user.click(screen.getByRole("button", { name: /^Settings/ }));
    await user.click(await screen.findByRole("button", { name: "Content types" }));

    await user.click(screen.getByLabelText("Show TikTok"));

    expect(screen.queryByRole("button", { name: /^TikTok/ })).toBeNull();
    expect(screen.getByRole("button", { name: /^Substack/ })).toBeTruthy();
  });

  it("persists theme selection and exposes the expiry date", async () => {
    const api = new FakeAPI([detail("email")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    document.documentElement.dataset.theme = "dark";
    render(<Home />);
    await screen.findByRole("heading", { name: "Email one" });
    await user.click(screen.getAllByRole("button", { name: "Switch to light mode" })[0]);
    expect(window.localStorage.getItem("contentflow-theme")).toBe("light");
    expect(screen.getAllByText(/Expires/).length).toBeGreaterThan(0);
  });

  it("uses clear singular and expired expiry wording", async () => {
    const soon = { ...detail("email"), working_title: "Soon item", expires_at: new Date(Date.now() + 3_600_000).toISOString() };
    const past = { ...detail("x"), working_title: "Past item", expires_at: new Date(Date.now() - 3_600_000).toISOString() };
    const api = new FakeAPI([soon, past]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);

    await screen.findByRole("heading", { name: "Soon item" });
    expect(screen.getByText("Expires in 1 day")).toBeTruthy();
    expect(screen.getByText(/· 1 day left/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /Past item/ }));

    expect(await screen.findByRole("heading", { name: "Past item" })).toBeTruthy();
    expect(screen.getByText("Expired")).toBeTruthy();
    expect(screen.getByText(/· expired/)).toBeTruthy();
  });

  it("refreshes and clears the editor when selected content expires while open", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    const expiring = { ...detail("email"), expires_at: new Date(now.getTime() + 1_000).toISOString() };
    const api = new FakeAPI([expiring]);
    api.excludeExpired = true;
    vi.stubGlobal("fetch", api.fetch);
    render(<Home />);

    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(screen.getByRole("heading", { name: "Email one" })).toBeTruthy();
    const initialListRequests = api.requests.filter((request) => request.path === "/api/v1/content").length;

    await act(async () => { await vi.advanceTimersByTimeAsync(1_050); });

    expect(screen.getByText("Your workspace is empty")).toBeTruthy();
    expect(screen.queryByLabelText("Content status")).toBeNull();
    expect(api.requests.filter((request) => request.path === "/api/v1/content").length).toBeGreaterThan(initialListRequests);
  });

  it("shows the final-seven-day warning when the deadline crosses while open", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    const expiring = { ...detail("email"), expires_at: new Date(now.getTime() + 7 * 86_400_000 + 1_000).toISOString() };
    const api = new FakeAPI([expiring]);
    api.excludeExpired = true;
    vi.stubGlobal("fetch", api.fetch);
    render(<Home />);

    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(screen.queryByText("Expires in 7 days")).toBeNull();

    await act(async () => { await vi.advanceTimersByTimeAsync(1_050); });

    expect(screen.getByText("Expires in 7 days")).toBeTruthy();
    expect(screen.getByText(/· 7 days left/)).toBeTruthy();

    for (let day = 0; day < 7; day += 1) {
      await act(async () => { await vi.advanceTimersByTimeAsync(86_400_000 + 50); });
    }

    expect(screen.getByText("Your workspace is empty")).toBeTruthy();
    expect(screen.queryByLabelText("Content status")).toBeNull();
  });

  it("lets a newer filter refresh reconcile a selection that expires", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    const expiring = { ...detail("email"), expires_at: new Date(now.getTime() + 1_000).toISOString() };
    const api = new FakeAPI([expiring, detail("x")]);
    api.excludeExpired = true;
    vi.stubGlobal("fetch", api.fetch);
    render(<Home />);

    await act(async () => { await vi.advanceTimersByTimeAsync(300); });
    expect(screen.getByRole("heading", { name: "Email one" })).toBeTruthy();
    let releaseExpiredRefresh: () => void = () => undefined;
    api.listGate = new Promise<void>((resolve) => { releaseExpiredRefresh = resolve; });

    await act(async () => { await vi.advanceTimersByTimeAsync(750); });
    expect(api.listGateStarted).toBe(1);
    fireEvent.change(screen.getByLabelText("Search content titles"), { target: { value: "X" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(250); });

    expect(screen.getByRole("heading", { name: "X one" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Email one/ })).toBeNull();
    releaseExpiredRefresh();
    await act(async () => { await Promise.resolve(); });

    expect(screen.getByRole("heading", { name: "X one" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Email one" })).toBeNull();
  });

  it("backs off expiry refreshes while the server clock still considers an item live", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    const expiring = { ...detail("email"), expires_at: new Date(now.getTime() + 1_000).toISOString() };
    const api = new FakeAPI([expiring]);
    vi.stubGlobal("fetch", api.fetch);
    render(<Home />);

    await act(async () => { await vi.advanceTimersByTimeAsync(300); });
    const requestsBeforeExpiry = api.requests.filter((request) => request.path === "/api/v1/content").length;
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    const requestsAfterExpiry = api.requests.filter((request) => request.path === "/api/v1/content").length;
    expect(requestsAfterExpiry).toBeGreaterThan(requestsBeforeExpiry);

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(api.requests.filter((request) => request.path === "/api/v1/content")).toHaveLength(requestsAfterExpiry);

    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(api.requests.filter((request) => request.path === "/api/v1/content").length).toBeGreaterThan(requestsAfterExpiry);
  });

  it("keeps global shortcuts inside open modal dialogs", async () => {
    const api = new FakeAPI([detail("linkedin")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "LinkedIn one" });
    await user.click(screen.getAllByRole("button", { name: /New content/ })[0]);
    const createDialog = screen.getByRole("dialog");

    await user.keyboard("{Control>}k{/Control}");
    expect(createDialog.contains(document.activeElement)).toBe(true);
    await user.keyboard("n");
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("button", { name: "Delete" }));
    const deleteDialog = screen.getByRole("alertdialog");
    await user.keyboard("{Control>}k{/Control}");
    expect(deleteDialog.contains(document.activeElement)).toBe(true);
    await user.keyboard("n");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("alertdialog")).toBe(deleteDialog);
  });

  it("moves and traps focus in the mobile library, then restores it", async () => {
    const api = new FakeAPI([detail("email")]);
    vi.stubGlobal("fetch", api.fetch);
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: true,
      media: "(max-width: 900px)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })));
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "Email one" });
    const open = screen.getByRole("button", { name: "Open content library" });

    await user.click(open);
    const close = screen.getByRole("button", { name: "Close library" });
    await waitFor(() => expect(document.activeElement).toBe(close));
    const editor = document.querySelector<HTMLElement>(".editor-panel");
    expect(editor?.hasAttribute("inert")).toBe(true);
    await user.keyboard("{Shift>}{Tab}{/Shift}");
    expect(screen.getByRole("region", { name: "Content library" }).contains(document.activeElement)).toBe(true);
    await user.keyboard("{Escape}");

    await waitFor(() => expect(document.activeElement).toBe(open));
    expect(editor?.hasAttribute("inert")).toBe(false);
  });

  it("collapses and restores the desktop content library", async () => {
    const api = new FakeAPI([detail("email")]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "Email one" });

    const library = screen.getByRole("region", { name: "Content library" });
    const collapse = screen.getByRole("button", { name: "Collapse content library" });
    collapse.focus();
    await user.keyboard("{Enter}");

    expect(document.querySelector(".app-shell")?.classList).toContain("library-is-collapsed");
    expect(library.getAttribute("aria-hidden")).toBe("true");
    expect(library.hasAttribute("inert")).toBe(true);
    expect(window.localStorage.getItem("contentflow-library-collapsed")).toBe("true");

    await user.click(screen.getByRole("button", { name: "Expand content library" }));

    expect(document.querySelector(".app-shell")?.classList).not.toContain("library-is-collapsed");
    expect(library.hasAttribute("aria-hidden")).toBe(false);
    expect(library.hasAttribute("inert")).toBe(false);
    expect(window.localStorage.getItem("contentflow-library-collapsed")).toBe("false");
  });

  it("keeps the mobile content library available when desktop collapse is remembered", async () => {
    window.localStorage.setItem("contentflow-library-collapsed", "true");
    const api = new FakeAPI([detail("email")]);
    vi.stubGlobal("fetch", api.fetch);
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: true,
      media: "(max-width: 900px)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })));
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "Email one" });

    await user.click(screen.getByRole("button", { name: "Open content library" }));

    expect(screen.getByRole("region", { name: "Content library" }).hasAttribute("inert")).toBe(false);
    expect(screen.queryByRole("button", { name: "Expand content library" })).toBeNull();
  });

  it("can restore a collapsed library from an empty workspace", async () => {
    window.localStorage.setItem("contentflow-library-collapsed", "true");
    const api = new FakeAPI([]);
    vi.stubGlobal("fetch", api.fetch);
    const user = userEvent.setup();
    render(<Home />);
    await screen.findByRole("heading", { name: "Start writing" });

    await user.click(screen.getByRole("button", { name: "Expand content library" }));

    expect(screen.getByRole("region", { name: "Content library" }).hasAttribute("inert")).toBe(false);
    expect(document.querySelector(".app-shell")?.classList).not.toContain("library-is-collapsed");
  });
});
