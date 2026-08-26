export const contentTypes = ["youtube", "linkedin", "x", "instagram", "tiktok", "email", "substack"] as const;
export const contentStatuses = ["idea", "draft", "ready", "published"] as const;

export type ContentType = (typeof contentTypes)[number];
export type ContentStatus = (typeof contentStatuses)[number];

export type Section = {
  id?: string;
  clientKey: string;
  position: number;
  title: string;
  body: string;
};

export type YouTubeContent = {
  topic: string;
  icp: string;
  angle: string;
  cta: string;
  publishing_title: string;
  description: string;
  transcript: string;
  sections: Section[];
};

export type ContentPayload =
  | YouTubeContent
  | { body: string }
  | { script: string }
  | { subject: string; body: string }
  | { headline: string; subheadline: string; body: string };

export type ContentSummary = {
  id: string;
  type: ContentType;
  status: ContentStatus;
  working_title: string;
  revision: number;
  created_at: string;
  updated_at: string;
  expires_at: string;
  scheduled_at?: string;
  asset_counts: Record<string, number>;
};

export type ContentDetail = Omit<ContentSummary, "asset_counts"> & {
  content: ContentPayload;
};

export type MutationResult = {
  operation_id: string;
  item_ids: string[];
  revisions: number[];
  expires_at: string[];
  status: string;
};

type Session = {
  csrf_token: string;
  workspace_id?: string;
};

export class ApiError extends Error {
  status: number;
  code: string;
  current?: ContentDetail;

  constructor(status: number, code: string, current?: ContentDetail) {
    super(code);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.current = current ? normalizeDetail(current) : undefined;
  }
}

export function isSessionRecoveryError(error: unknown) {
  return error instanceof ApiError && (error.status === 401 || (error.status === 403 && error.code === "csrf_check_failed"));
}

function normalizeDetail(detail: ContentDetail): ContentDetail {
  if (detail.type !== "youtube") return detail;
  const content = detail.content as YouTubeContent;
  return {
    ...detail,
    content: {
      ...content,
      sections: content.sections.map((section, position) => ({
        ...section,
        clientKey: section.id ?? section.clientKey ?? newClientKey(),
        position,
      })),
    },
  };
}

async function parseResponse<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({})) as { error?: string; current?: ContentDetail } & T;
  if (!response.ok) {
    throw new ApiError(response.status, body.error ?? `request_failed_${response.status}`, body.current);
  }
  return body;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers: {
      Accept: "application/json",
      ...init?.headers,
    },
  });
  return parseResponse<T>(response);
}

async function withRequestTimeout<T>(request: (signal: AbortSignal) => Promise<T>, timeoutMs: number) {
  const controller = new AbortController();
  let timeout: number | undefined;
  const timedOut = new Promise<never>((_, reject) => {
    timeout = window.setTimeout(() => {
      controller.abort();
      reject(new TypeError("request timed out"));
    }, timeoutMs);
  });
  try {
    return await Promise.race([request(controller.signal), timedOut]);
  } finally {
    if (timeout) window.clearTimeout(timeout);
  }
}

export async function loadSession(requestTimeout = 10_000): Promise<Session> {
  return withRequestTimeout((signal) => request<Session>("/api/v1/session", { signal }), requestTimeout);
}

export async function listContent(filters: { q?: string; type?: ContentType; status?: ContentStatus } = {}, requestTimeout = 10_000): Promise<ContentSummary[]> {
  const query = new URLSearchParams();
  if (filters.q?.trim()) query.set("q", filters.q.trim());
  if (filters.type) query.set("type", filters.type);
  if (filters.status) query.set("status", filters.status);
  const serializedQuery = query.toString();
  const suffix = serializedQuery ? `?${serializedQuery}` : "";
  const response = await withRequestTimeout((signal) => request<{ items: ContentSummary[] }>(`/api/v1/content${suffix}`, { signal }), requestTimeout);
  return response.items;
}

export async function getContent(id: string, signal?: AbortSignal, requestTimeout = 10_000): Promise<ContentDetail> {
  const load = (requestSignal: AbortSignal) => request<ContentDetail>(`/api/v1/content/${encodeURIComponent(id)}`, { signal: requestSignal });
  return normalizeDetail(await (signal ? load(signal) : withRequestTimeout(load, requestTimeout)));
}

export type CreateOptions = { workingTitle?: string; scheduledAt?: string; requestTimeout?: number };

export async function createContent(type: ContentType, csrfToken: string, operationId: string, options: CreateOptions = {}): Promise<MutationResult> {
  const body = JSON.stringify({
    type,
    working_title: options.workingTitle ?? "",
    status: "idea",
    operation_id: operationId,
    ...(options.scheduledAt ? { scheduled_at: options.scheduledAt } : {}),
    content: wireContent(type, emptyContent(type)),
  });
  return withRequestTimeout((signal) => request<MutationResult>("/api/v1/content", { ...mutationInit("POST", body, csrfToken), signal }), options.requestTimeout ?? 10_000);
}

export async function replaceContent(id: string, frozenBody: string, csrfToken: string, signal?: AbortSignal, requestTimeout = 10_000): Promise<MutationResult> {
  const save = (requestSignal: AbortSignal) => request<MutationResult>(`/api/v1/content/${encodeURIComponent(id)}`, { ...mutationInit("PUT", frozenBody, csrfToken), signal: requestSignal });
  return signal ? save(signal) : withRequestTimeout(save, requestTimeout);
}

export async function deleteContent(id: string, revision: number, csrfToken: string, operationId: string, requestTimeout = 10_000): Promise<MutationResult> {
  const body = JSON.stringify({ operation_id: operationId, revision });
  return withRequestTimeout((signal) => request<MutationResult>(`/api/v1/content/${encodeURIComponent(id)}`, { ...mutationInit("DELETE", body, csrfToken), signal }), requestTimeout);
}

function mutationInit(method: string, body: string, csrfToken: string): RequestInit {
  return {
    method,
    body,
    headers: {
      "Content-Type": "application/json",
      ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}),
    },
  };
}

export function serializeReplacement(detail: ContentDetail, operationId: string): string {
  return JSON.stringify({
    type: detail.type,
    working_title: detail.working_title,
    status: detail.status,
    operation_id: operationId,
    revision: detail.revision,
    ...(detail.scheduled_at ? { scheduled_at: detail.scheduled_at } : {}),
    content: wireContent(detail.type, detail.content),
  });
}

function wireContent(type: ContentType, content: ContentPayload): unknown {
  if (type !== "youtube") return content;
  return {
    ...(content as YouTubeContent),
    sections: (content as YouTubeContent).sections.map((source, position) => {
      const { clientKey, ...section } = source;
      void clientKey;
      return { ...section, position };
    }),
  };
}

export function emptyContent(type: ContentType): ContentPayload {
  switch (type) {
    case "youtube":
      return {
        topic: "",
        icp: "",
        angle: "",
        cta: "",
        publishing_title: "",
        description: "",
        transcript: "",
        sections: ["Intro", "Main section", "Outro"].map((title, position) => ({
          clientKey: newClientKey(),
          position,
          title,
          body: "",
        })),
      };
    case "instagram":
    case "tiktok":
      return { script: "" };
    case "email":
      return { subject: "", body: "" };
    case "substack":
      return { headline: "", subheadline: "", body: "" };
    default:
      return { body: "" };
  }
}

export function typeLabel(type: ContentType): string {
  if (type === "x") return "X";
  return type.charAt(0).toUpperCase() + type.slice(1);
}

export function newClientKey(): string {
  return crypto.randomUUID();
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

export function newOperationId(now = Date.now()): string {
  let timestamp = now;
  let encodedTime = "";
  for (let index = 0; index < 10; index += 1) {
    encodedTime = crockford[timestamp % 32] + encodedTime;
    timestamp = Math.floor(timestamp / 32);
  }
  const random = new Uint8Array(10);
  crypto.getRandomValues(random);
  let bits = 0;
  let bitCount = 0;
  let encodedRandom = "";
  for (const byte of random) {
    bits = (bits << 8) | byte;
    bitCount += 8;
    while (bitCount >= 5) {
      bitCount -= 5;
      encodedRandom += crockford[(bits >> bitCount) & 31];
    }
  }
  return encodedTime + encodedRandom.slice(0, 16);
}
