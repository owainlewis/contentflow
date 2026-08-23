// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, emptyContent, serializeReplacement, type ContentDetail, type MutationResult, type YouTubeContent } from "../apps/web/src/api";
import { AutosaveManager, mergeServerState, rebaseConflictState } from "../apps/web/src/autosave";

const original = (): ContentDetail => ({
  id: "01KTESTAUTOSAVE00000000000",
  type: "x",
  status: "draft",
  working_title: "Original",
  revision: 1,
  created_at: "2026-08-15T10:00:00Z",
  updated_at: "2026-08-15T10:00:00Z",
  expires_at: "2026-10-10T10:00:00Z",
  content: emptyContent("x"),
});

afterEach(() => vi.useRealTimers());

describe("AutosaveManager", () => {
  it("rebases a debounced draft onto an external schedule update", async () => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    const documents: ContentDetail[] = [];
    const scheduled = { ...original(), revision: 2, updated_at: "2026-08-16T10:00:00Z", scheduled_at: "2026-08-18T08:00:00Z" };
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        return { operation_id: "op", item_ids: [scheduled.id], revisions: [3], expires_at: [scheduled.expires_at], status: "updated" };
      },
      resolve: async () => ({ ...scheduled, working_title: "Local edit", revision: 3 }),
      onDocument: (document) => documents.push(document),
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Local edit" });
    expect(manager.isBusy(original().id)).toBe(true);
    expect(manager.reconcileExternal(scheduled)).toBe(true);
    expect(manager.getDraft(original().id)).toEqual(expect.objectContaining({
      working_title: "Local edit",
      revision: 2,
      scheduled_at: scheduled.scheduled_at,
    }));
    await vi.advanceTimersByTimeAsync(750);

    expect(JSON.parse(bodies[0])).toEqual(expect.objectContaining({
      working_title: "Local edit",
      revision: 2,
      scheduled_at: scheduled.scheduled_at,
    }));
    expect(documents.at(-1)).toEqual(expect.objectContaining({ scheduled_at: scheduled.scheduled_at }));
    manager.dispose();
  });

  it("waits 750 ms, retries frozen bytes and operation ID, then queues later edits under a new ID", async () => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    let attempts = 0;
    let saved = original();
    const result = (): MutationResult => ({ operation_id: "op", item_ids: [saved.id], revisions: [saved.revision], expires_at: [saved.expires_at], status: "updated" });
    const manager = new AutosaveManager({
      delay: 750,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        attempts += 1;
        if (attempts === 1) throw new TypeError("network failed");
        const request = JSON.parse(body) as { working_title: string };
        saved = { ...saved, working_title: request.working_title, revision: saved.revision + 1 };
        return result();
      },
      resolve: async () => saved,
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Frozen edit" });
    await vi.advanceTimersByTimeAsync(749);
    expect(bodies).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(bodies).toHaveLength(1);
    manager.enqueue({ ...original(), working_title: "Later edit" });
    await vi.advanceTimersByTimeAsync(1000);
    await vi.runOnlyPendingTimersAsync();

    expect(bodies.length).toBeGreaterThanOrEqual(3);
    expect(bodies[0]).toBe(bodies[1]);
    const first = JSON.parse(bodies[0]) as { operation_id: string; working_title: string };
    const later = JSON.parse(bodies[2]) as { operation_id: string; working_title: string };
    expect(first.working_title).toBe("Frozen edit");
    expect(later.working_title).toBe("Later edit");
    expect(later.operation_id).not.toBe(first.operation_id);
    manager.dispose();
  });

  it("aborts a stalled attempt and retries the same frozen request", async () => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    const aborted: boolean[] = [];
    const manager = new AutosaveManager({
      delay: 750,
      requestTimeout: 500,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async (_id, body, signal) => {
        bodies.push(body);
        signal?.addEventListener("abort", () => aborted.push(true));
        return new Promise<MutationResult>(() => undefined);
      },
      resolve: async () => original(),
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Frozen timeout" });
    await vi.advanceTimersByTimeAsync(750);
    await vi.advanceTimersByTimeAsync(500);
    expect(aborted).toEqual([true]);
    await vi.advanceTimersByTimeAsync(1000);

    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]);
    manager.dispose();
  });

  it("retries when resolving the saved detail stalls", async () => {
    vi.useFakeTimers();
    let sends = 0;
    let resolves = 0;
    const resolveAborts: boolean[] = [];
    const manager = new AutosaveManager({
      delay: 750,
      requestTimeout: 500,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async () => {
        sends += 1;
        return { operation_id: "op", item_ids: [original().id], revisions: [2], expires_at: [original().expires_at], status: "updated" };
      },
      resolve: async (_id, signal) => {
        resolves += 1;
        signal?.addEventListener("abort", () => resolveAborts.push(true));
        return new Promise<ContentDetail>(() => undefined);
      },
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Resolve timeout" });
    await vi.advanceTimersByTimeAsync(750);
    await vi.advanceTimersByTimeAsync(500);
    expect(resolveAborts).toEqual([true]);
    await vi.advanceTimersByTimeAsync(1000);

    expect(sends).toBe(1);
    expect(resolves).toBe(2);
    manager.dispose();
  });

  it("retries only the detail read when it lags an acknowledged save", async () => {
    vi.useFakeTimers();
    let sends = 0;
    let resolves = 0;
    const persisted = { ...original(), working_title: "Persisted", revision: 2 };
    const manager = new AutosaveManager({
      delay: 750,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async () => {
        sends += 1;
        return { operation_id: "op", item_ids: [persisted.id], revisions: [2], expires_at: [persisted.expires_at], status: "updated" };
      },
      resolve: async () => {
        resolves += 1;
        return resolves === 1 ? original() : persisted;
      },
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Persisted" });
    await vi.advanceTimersByTimeAsync(750);
    await vi.advanceTimersByTimeAsync(1000);

    expect(sends).toBe(1);
    expect(resolves).toBe(2);
    manager.dispose();
  });

  it("retries a rate-limited frozen save without changing its bytes", async () => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    let saved = original();
    const manager = new AutosaveManager({
      delay: 750,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        if (bodies.length === 1) throw new ApiError(429, "rate_limited");
        const request = JSON.parse(body) as { working_title: string };
        saved = { ...saved, working_title: request.working_title, revision: 2 };
        return { operation_id: "op", item_ids: [saved.id], revisions: [2], expires_at: [saved.expires_at], status: "updated" };
      },
      resolve: async () => saved,
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Rate-limited edit" });
    await vi.advanceTimersByTimeAsync(750);
    await vi.advanceTimersByTimeAsync(1000);

    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]);
    manager.dispose();
  });

  it("retries only a rate-limited detail read after the save is acknowledged", async () => {
    vi.useFakeTimers();
    let sends = 0;
    let resolves = 0;
    const persisted = { ...original(), working_title: "Persisted after throttle", revision: 2 };
    const manager = new AutosaveManager({
      delay: 750,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async () => {
        sends += 1;
        return { operation_id: "op", item_ids: [persisted.id], revisions: [2], expires_at: [persisted.expires_at], status: "updated" };
      },
      resolve: async () => {
        resolves += 1;
        if (resolves === 1) throw new ApiError(429, "rate_limited");
        return persisted;
      },
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Persisted after throttle" });
    await vi.advanceTimersByTimeAsync(750);
    await vi.advanceTimersByTimeAsync(1000);

    expect(sends).toBe(1);
    expect(resolves).toBe(2);
    manager.dispose();
  });

  it("flushes a debounced later edit after the frozen request fails with a terminal error", async () => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    let releaseFirst: () => void = () => undefined;
    const firstGate = new Promise<void>((resolve) => { releaseFirst = resolve; });
    let signalSecond: () => void = () => undefined;
    const secondSent = new Promise<void>((resolve) => { signalSecond = resolve; });
    let saved = original();
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        if (bodies.length === 1) {
          await firstGate;
          throw new ApiError(400, "working_title_required");
        }
        signalSecond();
        const request = JSON.parse(body) as { working_title: string };
        saved = { ...saved, working_title: request.working_title, revision: saved.revision + 1 };
        return { operation_id: "op", item_ids: [saved.id], revisions: [saved.revision], expires_at: [saved.expires_at], status: "updated" };
      },
      resolve: async () => saved,
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "" });
    await vi.advanceTimersByTimeAsync(750);
    manager.enqueue({ ...original(), working_title: "Corrected title" });
    await vi.advanceTimersByTimeAsync(750);
    releaseFirst();
    await secondSent;

    expect(bodies).toHaveLength(2);
    expect(JSON.parse(bodies[1])).toEqual(expect.objectContaining({ working_title: "Corrected title" }));
    expect(JSON.parse(bodies[1]).operation_id).not.toBe(JSON.parse(bodies[0]).operation_id);
    manager.dispose();
  });

  it("accepts a newer resolved server revision after a successful save", async () => {
    vi.useFakeTimers();
    const states: string[] = [];
    let sends = 0;
    const server = { ...original(), working_title: "Newer server state", revision: 3 };
    const manager = new AutosaveManager({
      delay: 750,
      retryDelays: [1000],
      serialize: serializeReplacement,
      send: async () => {
        sends += 1;
        return { operation_id: "op", item_ids: [server.id], revisions: [2], expires_at: [server.expires_at], status: "updated" };
      },
      resolve: async () => server,
      onDocument: () => undefined,
      onState: (_id, state) => states.push(state),
      onConflict: () => undefined,
    });

    manager.enqueue({ ...original(), working_title: "Saved locally" });
    await vi.advanceTimersByTimeAsync(750);
    await vi.runAllTicks();

    expect(sends).toBe(1);
    expect(states.at(-1)).toBe("saved");
    manager.dispose();
  });

  it("surfaces a newer server revision when later local edits are queued", async () => {
    vi.useFakeTimers();
    let sends = 0;
    let releaseSend: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => { releaseSend = resolve; });
    let signalConflict: () => void = () => undefined;
    const conflictReady = new Promise<void>((resolve) => { signalConflict = resolve; });
    const server = { ...original(), working_title: "Newer server state", revision: 3 };
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async () => {
        sends += 1;
        await gate;
        return { operation_id: "op", item_ids: [server.id], revisions: [2], expires_at: [server.expires_at], status: "updated" };
      },
      resolve: async () => server,
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: (_id, conflict) => { if (conflict) signalConflict(); },
    });

    manager.enqueue({ ...original(), working_title: "Frozen edit" });
    await vi.advanceTimersByTimeAsync(750);
    manager.enqueue({ ...original(), working_title: "Queued edit" });
    releaseSend();
    await conflictReady;

    expect(manager.getConflict(server.id)).toEqual({
      server,
      local: expect.objectContaining({ working_title: "Queued edit" }),
    });
    manager.resolveConflict(server.id, "server");
    await vi.advanceTimersByTimeAsync(750);
    expect(sends).toBe(1);
    manager.dispose();
  });

  it.each([
    [401, "session_expired"],
    [403, "csrf_check_failed"],
  ])("pauses a %i %s frozen save and resumes it unchanged after reauthentication", async (status, code) => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    const states: string[] = [];
    let unauthorized = 0;
    let signalSaved: () => void = () => undefined;
    const savedReady = new Promise<void>((resolve) => { signalSaved = resolve; });
    const saved = { ...original(), working_title: "Queued through sign-in", revision: 2 };
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        if (bodies.length === 1) throw new ApiError(status, code);
        return { operation_id: "op", item_ids: [saved.id], revisions: [2], expires_at: [saved.expires_at], status: "updated" };
      },
      resolve: async () => saved,
      onDocument: () => undefined,
      onState: (_id, state) => states.push(state),
      onConflict: () => undefined,
      onUnauthorized: () => { unauthorized += 1; },
      onSaved: signalSaved,
    });

    manager.enqueue({ ...original(), working_title: "Queued through sign-in" });
    await vi.advanceTimersByTimeAsync(750);
    expect(unauthorized).toBe(1);
    expect(states.at(-1)).toBe("reauthenticating");

    manager.resumeUnauthorized();
    await savedReady;

    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]);
    expect(states.at(-1)).toBe("saved");
    manager.dispose();
  });

  it("retries a delayed old-session auth failure after another request renews the session", async () => {
    vi.useFakeTimers();
    const bodies: string[] = [];
    let sessionGeneration = 0;
    let releaseSend: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => { releaseSend = resolve; });
    const saved = { ...original(), working_title: "Saved after shared recovery", revision: 2 };
    let signalSaved: () => void = () => undefined;
    const savedReady = new Promise<void>((resolve) => { signalSaved = resolve; });
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        if (bodies.length === 1) {
          await gate;
          throw new ApiError(401, "session_expired");
        }
        return { operation_id: "op", item_ids: [saved.id], revisions: [2], expires_at: [saved.expires_at], status: "updated" };
      },
      resolve: async () => saved,
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: () => undefined,
      onUnauthorized: () => { throw new Error("stale auth failure reopened recovery"); },
      onSaved: signalSaved,
      getSessionGeneration: () => sessionGeneration,
    });

    manager.enqueue({ ...original(), working_title: "Saved after shared recovery" });
    await vi.advanceTimersByTimeAsync(750);
    sessionGeneration = 1;
    releaseSend();
    await savedReady;

    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]);
    manager.dispose();
  });

  it("cancels a later debounce when a frozen save enters conflict", async () => {
    vi.useFakeTimers();
    let releaseSend: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => { releaseSend = resolve; });
    let signalConflict: () => void = () => undefined;
    const conflictReady = new Promise<void>((resolve) => { signalConflict = resolve; });
    const server = { ...original(), working_title: "Server wins", revision: 2 };
    let sends = 0;
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async () => {
        sends += 1;
        await gate;
        throw new ApiError(409, "revision_conflict", server);
      },
      resolve: async () => server,
      onDocument: () => undefined,
      onState: () => undefined,
      onConflict: (_id, nextConflict) => { if (nextConflict) signalConflict(); },
    });

    manager.enqueue({ ...original(), working_title: "Frozen edit" });
    await vi.advanceTimersByTimeAsync(750);
    manager.enqueue({ ...original(), working_title: "Debouncing later edit" });
    releaseSend();
    await conflictReady;
    manager.resolveConflict(server.id, "server");
    await vi.advanceTimersByTimeAsync(750);

    expect(sends).toBe(1);
    manager.dispose();
  });

  it("keeps YouTube section identity by ID when rebasing a conflict", () => {
    const youtube = (sections: YouTubeContent["sections"], revision: number): ContentDetail => ({
      ...original(),
      type: "youtube",
      revision,
      content: { ...(emptyContent("youtube") as YouTubeContent), sections },
    });
    const local = youtube([
      { clientKey: "new", position: 0, title: "New local", body: "new" },
      { id: "section-a", clientKey: "section-a", position: 1, title: "Existing", body: "mine" },
      { id: "removed-server-side", clientKey: "old", position: 2, title: "Restore", body: "mine" },
    ], 2);
    const server = youtube([
      { id: "section-a", clientKey: "section-a", position: 0, title: "Existing", body: "server" },
      { id: "section-b", clientKey: "section-b", position: 1, title: "Server only", body: "server" },
    ], 3);

    const rebased = rebaseConflictState(local, server);
    const sections = (rebased.content as YouTubeContent).sections;
    expect(sections.map((section) => section.id)).toEqual([undefined, "section-a", undefined]);
    expect(rebased.revision).toBe(3);
  });

  it("matches returned YouTube section IDs by frozen content after server reordering", () => {
    const youtube = (sections: YouTubeContent["sections"], revision: number): ContentDetail => ({
      ...original(),
      type: "youtube",
      revision,
      content: { ...(emptyContent("youtube") as YouTubeContent), sections },
    });
    const frozen = youtube([
      { clientKey: "local-a", position: 0, title: "Alpha", body: "First" },
      { clientKey: "local-b", position: 1, title: "Beta", body: "Second" },
    ], 1);
    const latest = youtube([
      { clientKey: "local-b", position: 0, title: "Beta edited later", body: "Second" },
      { clientKey: "local-a", position: 1, title: "Alpha", body: "First edited later" },
    ], 1);
    const server = youtube([
      { id: "server-b", clientKey: "server-b", position: 0, title: "Beta", body: "Second" },
      { id: "server-a", clientKey: "server-a", position: 1, title: "Alpha", body: "First" },
    ], 2);

    const merged = mergeServerState(latest, server, frozen);
    expect((merged?.content as YouTubeContent).sections.map((section) => [section.clientKey, section.id])).toEqual([
      ["local-b", "server-b"],
      ["local-a", "server-a"],
    ]);
  });

  it("preserves queued YouTube section identity through a reordered save result", async () => {
    vi.useFakeTimers();
    const youtube = (sections: YouTubeContent["sections"], revision: number): ContentDetail => ({
      ...original(),
      type: "youtube",
      revision,
      content: { ...(emptyContent("youtube") as YouTubeContent), sections },
    });
    const frozen = youtube([
      { clientKey: "local-a", position: 0, title: "Alpha", body: "First" },
      { clientKey: "local-b", position: 1, title: "Beta", body: "Second" },
    ], 1);
    const later = youtube([
      { clientKey: "local-b", position: 0, title: "Beta later", body: "Second" },
      { clientKey: "local-a", position: 1, title: "Alpha", body: "First later" },
    ], 1);
    const server = youtube([
      { id: "server-b", clientKey: "server-b", position: 0, title: "Beta", body: "Second" },
      { id: "server-a", clientKey: "server-a", position: 1, title: "Alpha", body: "First" },
    ], 2);
    let releaseSend: () => void = () => undefined;
    const sendGate = new Promise<void>((resolve) => { releaseSend = resolve; });
    let publishDocument: (document: ContentDetail) => void = () => undefined;
    const documented = new Promise<ContentDetail>((resolve) => { publishDocument = resolve; });
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async () => {
        await sendGate;
        return { operation_id: "op", item_ids: [server.id], revisions: [2], expires_at: [server.expires_at], status: "updated" };
      },
      resolve: async () => server,
      onDocument: publishDocument,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue(frozen);
    await vi.advanceTimersByTimeAsync(750);
    manager.enqueue(later);
    releaseSend();
    const merged = await documented;

    expect((merged.content as YouTubeContent).sections.map((section) => [section.clientKey, section.id])).toEqual([
      ["local-b", "server-b"],
      ["local-a", "server-a"],
    ]);
    manager.dispose();
  });

  it("reconciles queued duplicate new YouTube sections by their frozen positions", async () => {
    vi.useFakeTimers();
    const youtube = (sections: YouTubeContent["sections"], revision: number): ContentDetail => ({
      ...original(),
      type: "youtube",
      revision,
      content: { ...(emptyContent("youtube") as YouTubeContent), sections },
    });
    const frozen = youtube([
      { clientKey: "duplicate-a", position: 0, title: "Beat", body: "Pause" },
      { clientKey: "duplicate-b", position: 1, title: "Beat", body: "Pause" },
    ], 1);
    const latest = youtube([
      { clientKey: "duplicate-b", position: 0, title: "Beat later", body: "Pause" },
      { clientKey: "duplicate-a", position: 1, title: "Beat", body: "Pause later" },
    ], 1);
    const server = youtube([
      { id: "server-b", clientKey: "server-b", position: 1, title: "Beat", body: "Pause" },
      { id: "server-a", clientKey: "server-a", position: 0, title: "Beat", body: "Pause" },
    ], 2);
    const savedLater = youtube([
      { id: "server-b", clientKey: "server-b", position: 0, title: "Beat later", body: "Pause" },
      { id: "server-a", clientKey: "server-a", position: 1, title: "Beat", body: "Pause later" },
    ], 3);

    let releaseSend: () => void = () => undefined;
    const sendGate = new Promise<void>((resolve) => { releaseSend = resolve; });
    const bodies: string[] = [];
    let sends = 0;
    let publishDocument: (document: ContentDetail) => void = () => undefined;
    const documented = new Promise<ContentDetail>((resolve) => { publishDocument = resolve; });
    const manager = new AutosaveManager({
      delay: 750,
      serialize: serializeReplacement,
      send: async (_id, body) => {
        bodies.push(body);
        sends += 1;
        if (sends === 1) await sendGate;
        return { operation_id: "op", item_ids: [server.id], revisions: [sends + 1], expires_at: [server.expires_at], status: "updated" };
      },
      resolve: async () => sends === 1 ? server : savedLater,
      onDocument: publishDocument,
      onState: () => undefined,
      onConflict: () => undefined,
    });

    manager.enqueue(frozen);
    await vi.advanceTimersByTimeAsync(750);
    manager.enqueue(latest);
    releaseSend();
    const merged = await documented;

    expect((merged.content as YouTubeContent).sections.map((section) => [section.clientKey, section.id])).toEqual([
      ["duplicate-b", "server-b"],
      ["duplicate-a", "server-a"],
    ]);
    await vi.advanceTimersByTimeAsync(750);
    const secondRequest = JSON.parse(bodies[1]) as { content: YouTubeContent };
    expect(secondRequest.content.sections.map((section) => [section.id, section.title, section.body])).toEqual([
      ["server-b", "Beat later", "Pause"],
      ["server-a", "Beat", "Pause later"],
    ]);
    expect(sends).toBe(2);
    expect(manager.getConflict(server.id)).toBeUndefined();
    manager.dispose();
  });

  it("registers lifecycle conflicts so both resolution choices work", () => {
    const documents: ContentDetail[] = [];
    const conflicts: Array<ContentDetail | undefined> = [];
    const manager = new AutosaveManager({
      serialize: serializeReplacement,
      send: async () => { throw new Error("not used"); },
      resolve: async () => original(),
      onDocument: (document) => documents.push(document),
      onState: () => undefined,
      onConflict: (_id, conflict) => conflicts.push(conflict?.server),
    });
    const local = { ...original(), working_title: "Local" };
    const server = { ...original(), working_title: "Server", revision: 2 };

    manager.beginConflict(local, server);
    expect(manager.getConflict(local.id)).toEqual({ local, server });
    manager.resolveConflict(local.id, "server");
    expect(documents.at(-1)).toEqual(server);
    expect(conflicts.at(-1)).toBeUndefined();
    manager.beginConflict(local, server);
    manager.resolveConflict(local.id, "local");
    expect(manager.getDraft(local.id)).toEqual(expect.objectContaining({ working_title: "Local", revision: 2 }));
    manager.dispose();
  });
});
