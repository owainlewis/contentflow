import { ApiError, isSessionRecoveryError, newOperationId, type ContentDetail, type MutationResult, type YouTubeContent } from "./api";

export type SaveState = "saved" | "unsaved" | "saving" | "retrying" | "reauthenticating" | "conflict" | "error";

export type ConflictView = {
  server: ContentDetail;
  local: ContentDetail;
};

type Queue = {
  draft: ContentDetail;
  version: number;
  persistedVersion: number;
  ready: boolean;
  timer?: number;
  retryTimer?: number;
  inFlight?: FrozenSave;
  unauthorizedRetry?: () => Promise<void>;
  conflict?: ConflictView;
};

type FrozenSave = {
  body: string;
  document: ContentDetail;
  operationId: string;
  sessionGeneration: number;
  version: number;
  retryIndex: number;
};

type AutosaveOptions = {
  delay?: number;
  retryDelays?: number[];
  requestTimeout?: number;
  serialize: (detail: ContentDetail, operationId: string) => string;
  send: (id: string, body: string, signal?: AbortSignal) => Promise<MutationResult>;
  resolve: (id: string, signal?: AbortSignal) => Promise<ContentDetail>;
  onDocument: (detail: ContentDetail) => void;
  onState: (id: string, state: SaveState) => void;
  onConflict: (id: string, conflict?: ConflictView) => void;
  onUnauthorized?: (id: string) => void;
  onSaved?: (id: string) => void;
  getSessionGeneration?: () => number;
};

export class AutosaveManager {
  private queues = new Map<string, Queue>();
  private externalVersions = new Map<string, number>();
  private delay: number;
  private retryDelays: number[];
  private requestTimeout: number;
  private options: AutosaveOptions;

  constructor(options: AutosaveOptions) {
    this.options = options;
    this.delay = options.delay ?? 750;
    this.retryDelays = options.retryDelays ?? [1000, 2000, 4000, 8000, 16000, 30000];
    this.requestTimeout = options.requestTimeout ?? 10_000;
  }

  enqueue(detail: ContentDetail) {
    const existing = this.queues.get(detail.id);
    const queue: Queue = existing ?? { draft: detail, version: 0, persistedVersion: 0, ready: false };
    queue.draft = detail;
    queue.version += 1;
    if (queue.conflict) {
      queue.conflict = { ...queue.conflict, local: detail };
      this.options.onConflict(detail.id, queue.conflict);
      this.options.onState(detail.id, "conflict");
      this.queues.set(detail.id, queue);
      return;
    }
    queue.ready = false;
    if (queue.timer) window.clearTimeout(queue.timer);
    queue.timer = window.setTimeout(() => {
      queue.ready = true;
      queue.timer = undefined;
      void this.flush(detail.id);
    }, this.delay);
    this.queues.set(detail.id, queue);
    this.options.onState(detail.id, "unsaved");
  }

  getDraft(id: string) {
    const queue = this.queues.get(id);
    return queue && queue.version > queue.persistedVersion ? queue.draft : undefined;
  }

  getConflict(id: string) {
    return this.queues.get(id)?.conflict;
  }

  getVersionStamp(id: string) {
    const queue = this.queues.get(id);
    return `${queue?.version ?? 0}:${queue?.persistedVersion ?? 0}:${this.externalVersions.get(id) ?? 0}`;
  }

  isBusy(id: string) {
    const queue = this.queues.get(id);
    return Boolean(queue && (queue.version > queue.persistedVersion || queue.inFlight || queue.conflict || queue.unauthorizedRetry));
  }

  // Scheduling is saved outside the editor queue. Rebase an idle or debounced
  // draft onto that server result so the next edit cannot restore stale
  // metadata or submit an old revision.
  reconcileExternal(server: ContentDetail) {
    this.externalVersions.set(server.id, (this.externalVersions.get(server.id) ?? 0) + 1);
    const queue = this.queues.get(server.id);
    if (!queue) {
      this.options.onDocument(server);
      return true;
    }
    if (queue.inFlight || queue.conflict || queue.unauthorizedRetry) return false;
    if (queue.version > queue.persistedVersion) {
      queue.draft = rebaseConflictState(queue.draft, server);
      this.options.onDocument(queue.draft);
      return true;
    }
    queue.draft = server;
    this.options.onDocument(server);
    this.options.onState(server.id, "saved");
    return true;
  }

  beginConflict(local: ContentDetail, server: ContentDetail) {
    const queue = this.queues.get(local.id) ?? { draft: local, version: 0, persistedVersion: 0, ready: false };
    if (queue.timer) window.clearTimeout(queue.timer);
    if (queue.retryTimer) window.clearTimeout(queue.retryTimer);
    queue.timer = undefined;
    queue.retryTimer = undefined;
    queue.inFlight = undefined;
    queue.unauthorizedRetry = undefined;
    queue.ready = false;
    queue.draft = local;
    queue.conflict = { server, local };
    this.queues.set(local.id, queue);
    this.options.onConflict(local.id, queue.conflict);
    this.options.onState(local.id, "conflict");
  }

  discard(id: string) {
    const queue = this.queues.get(id);
    if (queue?.timer) window.clearTimeout(queue.timer);
    if (queue?.retryTimer) window.clearTimeout(queue.retryTimer);
    this.queues.delete(id);
    this.externalVersions.delete(id);
  }

  resolveConflict(id: string, choice: "server" | "local") {
    const queue = this.queues.get(id);
    if (!queue?.conflict) return;
    const conflict = queue.conflict;
    queue.conflict = undefined;
    this.options.onConflict(id, undefined);
    if (choice === "server") {
      queue.draft = conflict.server;
      queue.ready = false;
      queue.persistedVersion = queue.version;
      this.options.onDocument(conflict.server);
      this.options.onState(id, "saved");
      this.options.onSaved?.(id);
      return;
    }
    const rebased = rebaseConflictState(conflict.local, conflict.server);
    queue.draft = rebased;
    this.options.onDocument(rebased);
    this.enqueue(rebased);
  }

  dispose() {
    for (const id of this.queues.keys()) this.discard(id);
    this.externalVersions.clear();
  }

  resumeUnauthorized() {
    for (const [id, queue] of this.queues) {
      const retry = queue.unauthorizedRetry;
      if (!retry) continue;
      queue.unauthorizedRetry = undefined;
      if (queue.inFlight) queue.inFlight.sessionGeneration = this.options.getSessionGeneration?.() ?? queue.inFlight.sessionGeneration;
      this.options.onState(id, "saving");
      void retry();
    }
  }

  private async flush(id: string) {
    const queue = this.queues.get(id);
    if (!queue || !queue.ready || queue.inFlight || queue.conflict) return;
    const operationId = newOperationId();
    const frozen: FrozenSave = {
      body: this.options.serialize(queue.draft, operationId),
      document: queue.draft,
      operationId,
      sessionGeneration: this.options.getSessionGeneration?.() ?? 0,
      version: queue.version,
      retryIndex: 0,
    };
    queue.inFlight = frozen;
    queue.ready = false;
    this.options.onState(id, "saving");
    await this.attempt(id, frozen);
  }

  private async attempt(id: string, frozen: FrozenSave) {
    const queue = this.queues.get(id);
    if (!queue || queue.inFlight !== frozen) return;
    let result: MutationResult;
    try {
      result = await this.withRequestTimeout((signal) => this.options.send(id, frozen.body, signal));
    } catch (error) {
      this.handleFailure(id, frozen, error, () => this.attempt(id, frozen));
      return;
    }
    await this.resolveAttempt(id, frozen, result);
  }

  private async resolveAttempt(id: string, frozen: FrozenSave, result: MutationResult) {
    const queue = this.queues.get(id);
    if (!queue || queue.inFlight !== frozen) return;
    let server: ContentDetail;
    try {
      server = await this.withRequestTimeout((signal) => this.options.resolve(id, signal));
    } catch (error) {
      this.handleFailure(id, frozen, error, () => this.resolveAttempt(id, frozen, result));
      return;
    }
    const savedRevision = result.revisions[0];
    if (savedRevision === undefined) {
      this.finishTerminal(id, frozen);
      return;
    }
    if (savedRevision > server.revision) {
      this.scheduleRetry(id, frozen, () => this.resolveAttempt(id, frozen, result));
      return;
    }
    if (savedRevision < server.revision && queue.version !== frozen.version) {
      this.enterConflict(id, queue, server);
      return;
    }
    const latest = queue.version === frozen.version ? server : mergeServerState(queue.draft, server, frozen.document);
    if (!latest) {
      this.enterConflict(id, queue, server);
      return;
    }
    queue.draft = latest;
    queue.inFlight = undefined;
    queue.persistedVersion = frozen.version;
    this.options.onDocument(latest);
    this.options.onSaved?.(id);
    if (queue.version === frozen.version) {
      this.options.onState(id, "saved");
    } else {
      this.options.onState(id, "unsaved");
      if (queue.ready) void this.flush(id);
    }
  }

  private handleFailure(id: string, frozen: FrozenSave, error: unknown, retry: () => Promise<void>) {
    const queue = this.queues.get(id);
    if (!queue || queue.inFlight !== frozen) return;
    if (error instanceof ApiError && error.status === 409 && error.current) {
      this.enterConflict(id, queue, error.current);
      return;
    }
    if (isSessionRecoveryError(error)) {
      const currentSessionGeneration = this.options.getSessionGeneration?.() ?? frozen.sessionGeneration;
      if (currentSessionGeneration !== frozen.sessionGeneration) {
        frozen.sessionGeneration = currentSessionGeneration;
        this.options.onState(id, "saving");
        void retry();
        return;
      }
      queue.unauthorizedRetry = retry;
      this.options.onUnauthorized?.(id);
      this.options.onState(id, "reauthenticating");
      return;
    }
    if (!(error instanceof ApiError) || error.status === 429 || error.status >= 500) {
      this.scheduleRetry(id, frozen, retry);
      return;
    }
    this.finishTerminal(id, frozen);
  }

  private scheduleRetry(id: string, frozen: FrozenSave, retry: () => Promise<void>) {
    const queue = this.queues.get(id);
    if (!queue || queue.inFlight !== frozen) return;
    const delay = this.retryDelays[Math.min(frozen.retryIndex, this.retryDelays.length - 1)];
    frozen.retryIndex += 1;
    this.options.onState(id, "retrying");
    queue.retryTimer = window.setTimeout(() => {
      queue.retryTimer = undefined;
      void retry();
    }, delay);
  }

  private enterConflict(id: string, queue: Queue, server: ContentDetail) {
    if (queue.timer) window.clearTimeout(queue.timer);
    if (queue.retryTimer) window.clearTimeout(queue.retryTimer);
    queue.timer = undefined;
    queue.retryTimer = undefined;
    queue.ready = false;
    queue.inFlight = undefined;
    queue.unauthorizedRetry = undefined;
    queue.conflict = { server, local: queue.draft };
    this.options.onConflict(id, queue.conflict);
    this.options.onState(id, "conflict");
  }

  private finishTerminal(id: string, frozen: FrozenSave) {
    const queue = this.queues.get(id);
    if (!queue || queue.inFlight !== frozen) return;
    queue.inFlight = undefined;
    if (queue.version !== frozen.version) {
      this.options.onState(id, "unsaved");
      if (queue.ready) void this.flush(id);
    } else {
      this.options.onState(id, "error");
    }
  }

  private async withRequestTimeout<T>(request: (signal: AbortSignal) => Promise<T>) {
    const controller = new AbortController();
    let timeout: number | undefined;
    const timedOut = new Promise<never>((_, reject) => {
      timeout = window.setTimeout(() => {
        controller.abort();
        reject(new TypeError("save request timed out"));
      }, this.requestTimeout);
    });
    try {
      return await Promise.race([request(controller.signal), timedOut]);
    } finally {
      if (timeout) window.clearTimeout(timeout);
    }
  }
}

export function mergeServerState(latest: ContentDetail, server: ContentDetail, frozen: ContentDetail): ContentDetail | undefined {
  if (latest.type !== "youtube" || server.type !== "youtube" || frozen.type !== "youtube") {
    return mergeMetadata(latest, server);
  }
  const serverContent = server.content as YouTubeContent;
  const frozenContent = frozen.content as YouTubeContent;
  const latestContent = latest.content as YouTubeContent;
  const assignedIds = new Map<string, string>();
  const frozenIDs = new Set(frozenContent.sections.flatMap((section) => section.id ? [section.id] : []));
  const serverIDs = new Set(serverContent.sections.flatMap((section) => section.id ? [section.id] : []));
  if ([...frozenIDs].some((id) => !serverIDs.has(id))) return undefined;
  const available = serverContent.sections.filter((section) => section.id && !frozenIDs.has(section.id));
  for (const section of frozenContent.sections) {
    if (section.id) continue;
    const matches = available.filter((candidate) => candidate.title === section.title && candidate.body === section.body);
    const positionalMatches = matches.filter((candidate) => candidate.position === section.position);
    const matched = matches.length === 1 ? matches[0] : positionalMatches.length === 1 ? positionalMatches[0] : undefined;
    if (!matched?.id) return undefined;
    assignedIds.set(section.clientKey, matched.id);
    available.splice(available.indexOf(matched), 1);
  }
  return {
    ...latest,
    revision: server.revision,
    updated_at: server.updated_at,
    expires_at: server.expires_at,
    scheduled_at: server.scheduled_at,
    content: {
      ...latestContent,
      sections: latestContent.sections.map((section, position) => ({
        ...section,
        id: section.id ?? assignedIds.get(section.clientKey),
        position,
      })),
    },
  };
}

export function rebaseConflictState(local: ContentDetail, server: ContentDetail): ContentDetail {
  const rebased = mergeMetadata(local, server);
  if (local.type !== "youtube" || server.type !== "youtube") return rebased;
  const serverIDs = new Set((server.content as YouTubeContent).sections.flatMap((section) => section.id ? [section.id] : []));
  return {
    ...rebased,
    content: {
      ...(local.content as YouTubeContent),
      sections: (local.content as YouTubeContent).sections.map((section, position) => ({
        ...section,
        id: section.id && serverIDs.has(section.id) ? section.id : undefined,
        position,
      })),
    },
  };
}

function mergeMetadata(local: ContentDetail, server: ContentDetail): ContentDetail {
  return {
    ...local,
    revision: server.revision,
    updated_at: server.updated_at,
    expires_at: server.expires_at,
    scheduled_at: server.scheduled_at,
  };
}
