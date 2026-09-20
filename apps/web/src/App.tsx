import { Button, Input, Select } from "./ui";
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  ChevronDown,
  LoaderCircle,
  Moon,
  Plus,
  Search,
  SquarePen,
  Sun,
  Trash2,
  X,
  Zap,
} from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ApiError,
  contentStatuses,
  contentTypes,
  createContent,
  deleteContent,
  getContent,
  isSessionRecoveryError,
  listContent,
  loadSession,
  newOperationId,
  replaceContent,
  serializeReplacement,
  type ContentDetail,
  type ContentStatus,
  type ContentSummary,
  type ContentType,
} from "./api";
import PieceEditor from "./PieceEditor";
import TopicPieces from "./TopicPieces";
import Calendar, { dayKey } from "./Calendar";
import Settings from "./Settings";
import WeeklyMatrix from "./WeeklyMatrix";
import { TypeIcon, displayTitle, statusLabels, typeMeta } from "./content-meta";
import { AutosaveManager, type ConflictView, type SaveState } from "./autosave";
import { normalizeUnicode15Title } from "./unicode-normalization";

type Theme = "light" | "dark";
type View = "topics" | "library" | "workspace" | "weekly" | "calendar" | "settings";

// The server serves index.html for any extensionless path, so views are real
// URLs: deep links and the browser back button both work.
function viewFromPath(pathname: string): View {
  if (pathname.startsWith("/weekly")) return "weekly";
  if (pathname.startsWith("/calendar")) return "calendar";
  if (pathname.startsWith("/settings")) return "settings";
  if (pathname.startsWith("/content")) return "workspace";
  if (pathname.startsWith("/library")) return "library";
  return "topics";
}

const viewPaths: Record<View, string> = { topics: "/", library: "/library", workspace: "/content", weekly: "/weekly", calendar: "/calendar", settings: "/settings" };
type LifecycleAction = "delete";
type CreatePlan = { day: string; title: string; attemptId: string };

function scheduledAtFor(day: string) {
  return new Date(`${day}T09:00:00`).toISOString();
}

type PendingLifecycle = { id: string; action: LifecycleAction };
type LibraryFilters = { query: string; type: ContentType | "all"; status: ContentStatus | "all" };

class ScheduleLock {
  private readonly ids = new Set<string>();

  has(id: string) { return this.ids.has(id); }
  add(id: string) { this.ids.add(id); }
  remove(id: string) { this.ids.delete(id); }
  snapshot(): ReadonlySet<string> { return new Set(this.ids); }
}

const enabledTypesKey = "contentflow-enabled-types";

// Preferences live in localStorage beside the theme. They are per-browser, not
// per-account, because the API has no settings resource to store them in.
function readEnabledTypes(): ContentType[] {
  try {
    const stored = window.localStorage.getItem(enabledTypesKey);
    if (!stored) return [...contentTypes];
    const parsed: unknown = JSON.parse(stored);
    if (!Array.isArray(parsed)) return [...contentTypes];
    const kept = contentTypes.filter((type) => parsed.includes(type));
    // Never leave the workspace with nothing to create.
    return kept.length ? kept : [...contentTypes];
  } catch {
    return [...contentTypes];
  }
}

function normalizeSearchTitle(value: string) {
  return normalizeUnicode15Title(value);
}

function formatRelativeTime(value: string) {
  const elapsed = Date.now() - new Date(value).getTime();
  if (elapsed < 60_000) return "just now";
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)} min ago`;
  if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)} hr ago`;
  const days = Math.floor(elapsed / 86_400_000);
  return days === 1 ? "yesterday" : `${days} days ago`;
}

function editableSnapshot(detail: ContentDetail) {
  return { working_title: detail.working_title, status: detail.status, topic_id: detail.topic_id, format: detail.format, document_url: detail.document_url, video_url: detail.video_url, scheduled_at: detail.scheduled_at, content: detail.content };
}

function saveLabel(state: SaveState) {
  switch (state) {
    case "saving": return "Saving";
    case "retrying": return "Offline, retrying";
    case "reauthenticating": return "Sign in to continue saving";
    case "unsaved": return "Unsaved changes";
    case "conflict": return "Save conflict";
    case "error": return "Could not save";
    default: return "Saved";
  }
}

export default function Home() {
  const [summaries, setSummaries] = useState<ContentSummary[]>([]);
  const [allSummaries, setAllSummaries] = useState<ContentSummary[]>([]);
  const [selectedId, setSelectedId] = useState(() => new URLSearchParams(window.location.search).get("id") ?? "");
  const [selected, setSelected] = useState<ContentDetail>();
  const [documents, setDocuments] = useState<Record<string, ContentDetail>>({});
  const [createTopicId, setCreateTopicId] = useState("");
  const [typeFilter, setTypeFilter] = useState<ContentType | "all">("all");
  const [statusFilter, setStatusFilter] = useState<ContentStatus | "all">("all");
  const [query, setQuery] = useState("");
  const [csrfToken, setCsrfToken] = useState<string>();
  const [authState, setAuthState] = useState<"loading" | "ready" | "signed-out" | "error">("loading");
  const [sessionExpired, setSessionExpired] = useState(false);
  const [reauthChecking, setReauthChecking] = useState(false);
  const [reauthError, setReauthError] = useState("");
  const [signInEmail, setSignInEmail] = useState("");
  const [signInPassword, setSignInPassword] = useState("");
  const [signInError, setSignInError] = useState("");
  const [signInPending, setSignInPending] = useState(false);
  const [pendingSessionCreate, setPendingSessionCreate] = useState<{ type: ContentType; plan?: CreatePlan; topicId?: string; format?: string }>();
  const [pendingSessionLifecycle, setPendingSessionLifecycle] = useState<{ document: ContentDetail; action: LifecycleAction }>();
  const [loadError, setLoadError] = useState("");
  const [detailError, setDetailError] = useState("");
  const [detailReload, setDetailReload] = useState(0);
  const [detailLoading, setDetailLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [view, setView] = useState<View>(() => viewFromPath(window.location.pathname));
  const [calendarError, setCalendarError] = useState("");
  const [weeklyCreateError, setWeeklyCreateError] = useState("");
  const [completedWeeklyAttemptId, setCompletedWeeklyAttemptId] = useState("");
  const [frozenWeeklyPlan, setFrozenWeeklyPlan] = useState<(CreatePlan & { type: ContentType })>();
  const [schedulePendingIds, setSchedulePendingIds] = useState<ReadonlySet<string>>(() => new Set());
  const [scheduleUncertainIds, setScheduleUncertainIds] = useState<ReadonlySet<string>>(() => new Set());
  const [workspaceId, setWorkspaceId] = useState<string>();
  const [enabledTypes, setEnabledTypes] = useState<ContentType[]>(readEnabledTypes);
  const [createPending, setCreatePending] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [theme, setTheme] = useState<Theme>("dark");
  const [saveStates, setSaveStates] = useState<Record<string, SaveState>>({});
  const [autosaveManager, setAutosaveManager] = useState<AutosaveManager>();
  const [conflict, setConflict] = useState<ConflictView>();
  const [pendingLifecycle, setPendingLifecycle] = useState<PendingLifecycle>();
  const [actionError, setActionError] = useState("");
  const [filterError, setFilterError] = useState("");
  const [searchFocusRequest, setSearchFocusRequest] = useState(0);
  const createModalRef = useRef<HTMLElement>(null);
  const deleteModalRef = useRef<HTMLElement>(null);
  const searchFocusRequestedRef = useRef(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const selectedIdRef = useRef("");
  const allSummariesRef = useRef<ContentSummary[]>([]);
  const summariesRef = useRef<ContentSummary[]>([]);
  const autosaveRef = useRef<AutosaveManager | undefined>(undefined);
  const csrfTokenRef = useRef("");
  const createPendingRef = useRef(false);
  const createOperationsRef = useRef(new Map<string, { operationId: string; plan?: CreatePlan; topicId?: string; format?: string }>());
  const lifecycleOperationIdsRef = useRef(new Map<string, string>());
  const lifecycleSynchronizationRef = useRef(0);
  const pendingLifecycleRef = useRef<PendingLifecycle | undefined>(undefined);
  const refreshLibraryRef = useRef<(filters?: LibraryFilters) => Promise<void>>(async () => undefined);
  const requestSequence = useRef(0);
  const sessionGenerationRef = useRef(0);
  const activeFiltersRef = useRef<LibraryFilters>({ query: "", type: "all", status: "all" });
  const [scheduleLock] = useState(() => new ScheduleLock());

  const refreshLibrary = useCallback(async (filtersOverride?: LibraryFilters) => {
    const sequence = ++requestSequence.current;
    const currentFilters = filtersOverride ?? { query, type: typeFilter, status: statusFilter };
    const trimmedQuery = currentFilters.query.trim();
    const filtered = { q: trimmedQuery || undefined, type: currentFilters.type === "all" ? undefined : currentFilters.type, status: currentFilters.status === "all" ? undefined : currentFilters.status };
    const hasFilters = Boolean(filtered.q || filtered.type || filtered.status);
    const [all, visible] = await Promise.all([listContent(), hasFilters ? listContent(filtered) : Promise.resolve(undefined)]);
    if (sequence !== requestSequence.current) return;
    const manager = autosaveRef.current;
    const withDraft = (item: ContentSummary) => {
      const draft = manager?.getDraft(item.id);
      return draft ? { ...item, working_title: draft.working_title, status: draft.status, topic_id: draft.topic_id, format: draft.format, scheduled_at: draft.scheduled_at } : item;
    };
    const mergedAll = all.map(withDraft);
    const visibleIds = new Set((visible ?? all).map((item) => item.id));
    const queryPrefix = filtered.q ? normalizeSearchTitle(filtered.q) : undefined;
    const mergedVisible = hasFilters ? mergedAll.filter((item) => {
      const draft = manager?.getDraft(item.id);
      if (!draft) return visibleIds.has(item.id);
      const titleMatches = !queryPrefix || normalizeSearchTitle(item.working_title).startsWith(queryPrefix);
      return titleMatches
        && (!filtered.type || item.type === filtered.type)
        && (!filtered.status || item.status === filtered.status);
    }) : mergedAll;
    allSummariesRef.current = mergedAll;
    summariesRef.current = mergedVisible;
    setAllSummaries(mergedAll);
    setSummaries(mergedVisible);
    const currentSelectedId = selectedIdRef.current;
    if (currentSelectedId && !mergedAll.some((item) => item.id === currentSelectedId)) {
      manager?.discard(currentSelectedId);
      const replacementId = "";
      selectedIdRef.current = replacementId;
      setSelectedId(replacementId);
      setSelected(undefined);
      setConflict(undefined);
      setSaveStates((states) => {
        const next = { ...states };
        delete next[currentSelectedId];
        return next;
      });
      setActionError("");
    }
    setFilterError("");
  }, [query, statusFilter, typeFilter]);

  useEffect(() => {
    selectedIdRef.current = selectedId;
  }, [selectedId]);
  useLayoutEffect(() => {
    activeFiltersRef.current = { query, type: typeFilter, status: statusFilter };
    refreshLibraryRef.current = refreshLibrary;
  }, [query, refreshLibrary, statusFilter, typeFilter]);

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const session = await loadSession();
        const items = await listContent();
        if (!active) return;
        csrfTokenRef.current = session.csrf_token ?? "";
        setCsrfToken(csrfTokenRef.current);
        setWorkspaceId(session.workspace_id);
        allSummariesRef.current = items;
        summariesRef.current = items;
        setAllSummaries(items);
        setSummaries(items);
        setAuthState("ready");
      } catch (error) {
        if (!active) return;
        if (error instanceof ApiError && error.status === 401) setAuthState("signed-out");
        else {
          setLoadError("The workspace could not be loaded. Try again in a moment.");
          setAuthState("error");
        }
      }
    })();
    return () => { active = false; };
  }, []);

  useEffect(() => {
    const manager = new AutosaveManager({
      serialize: serializeReplacement,
      send: (id, body, signal) => replaceContent(id, body, csrfTokenRef.current, signal),
      resolve: (id, signal) => getContent(id, signal),
      onDocument: (detail) => {
        setDocuments((current) => ({ ...current, [detail.id]: detail }));
        if (selectedIdRef.current === detail.id) setSelected(detail);
        setAllSummaries((items) => items.map((item) => item.id === detail.id ? { ...item, ...detail, asset_counts: item.asset_counts } : item));
        setSummaries((items) => items.map((item) => item.id === detail.id ? { ...item, ...detail, asset_counts: item.asset_counts } : item));
      },
      onState: (id, state) => {
        if (state === "unsaved") lifecycleSynchronizationRef.current += 1;
        setSaveStates((states) => ({ ...states, [id]: state }));
      },
      onConflict: (id, nextConflict) => { if (selectedIdRef.current === id) setConflict(nextConflict); },
      onUnauthorized: () => setSessionExpired(true),
      getSessionGeneration: () => sessionGenerationRef.current,
      onSaved: (id) => {
        const refresh = refreshLibraryRef.current();
        const sequence = requestSequence.current;
        void refresh.then(() => {
          if (sequence === requestSequence.current) setActionError((current) => current === "The library could not be refreshed after saving." ? "" : current);
        }).catch((error) => {
          if (sequence !== requestSequence.current) return;
          if (isSessionRecoveryError(error)) {
            setSessionExpired(true);
          } else if (selectedIdRef.current === id) {
            setActionError("The library could not be refreshed after saving.");
          }
        });
      },
    });
    autosaveRef.current = manager;
    setAutosaveManager(manager);
    return () => {
      manager.dispose();
      if (autosaveRef.current === manager) autosaveRef.current = undefined;
    };
  }, []);

  useEffect(() => {
    if (authState !== "ready") return;
    const timer = window.setTimeout(() => {
      const refresh = refreshLibrary();
      const sequence = requestSequence.current;
      void refresh.then(() => {
        if (sequence !== requestSequence.current) return;
        setFilterError("");
      }).catch((error) => {
        if (sequence !== requestSequence.current) return;
        if (isSessionRecoveryError(error)) {
          setSessionExpired(true);
        } else {
          setFilterError("The library filters could not be refreshed.");
        }
      });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [authState, refreshLibrary]);

  useEffect(() => {
    if (!selectedId) return;
    let active = true;
    const sessionGeneration = sessionGenerationRef.current;
    void Promise.resolve().then(async () => {
      setDetailError("");
      const manager = autosaveRef.current;
      const queueVersion = manager?.getVersionStamp(selectedId) ?? "0:0";
      const draft = manager?.getDraft(selectedId);
      if (!active) return;
      setSelected(draft);
      setConflict(autosaveRef.current?.getConflict(selectedId));
      setDetailLoading(!draft);
      try {
        const detail = await getContent(selectedId);
        if (!active || (autosaveRef.current?.getVersionStamp(selectedId) ?? "0:0") !== queueVersion || autosaveRef.current?.getDraft(selectedId)) return;
        setSelected(detail);
        setDetailError("");
        setSaveStates((states) => ({ ...states, [selectedId]: "saved" }));
      } catch (error) {
        if (!active || sessionGeneration !== sessionGenerationRef.current) return;
        if (error instanceof ApiError && error.status === 401) {
          setSessionExpired(true);
        } else {
          setDetailError("The selected item could not be loaded.");
        }
      } finally {
        if (active) setDetailLoading(false);
      }
    });
    return () => { active = false; };
  }, [detailReload, selectedId]);

  useEffect(() => {
    const sync = () => {
      const next = viewFromPath(window.location.pathname);
      if (next === "topics" || next === "library") { setTypeFilter("all"); setStatusFilter("all"); setQuery(""); }
      setView(next);
      setSelectedId(new URLSearchParams(window.location.search).get("id") ?? "");
    };
    window.addEventListener("popstate", sync);
    return () => window.removeEventListener("popstate", sync);
  }, []);

  useEffect(() => {
    if (!searchFocusRequestedRef.current || view !== "library") return;
    searchRef.current?.focus();
    searchFocusRequestedRef.current = false;
  }, [searchFocusRequest, view]);

  useEffect(() => {
    const timer = window.setTimeout(() => setTheme(document.documentElement.dataset.theme === "light" ? "light" : "dark"), 0);
    return () => window.clearTimeout(timer);
  }, []);

  useEffect(() => {
    const modal = createOpen ? createModalRef.current : deleteOpen ? deleteModalRef.current : null;
    if (!modal) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusable = Array.from(modal.querySelectorAll<HTMLElement>('button:not([disabled]), input, textarea, select, [tabindex]:not([tabindex="-1"])'));
    focusable[0]?.focus();
    function handleKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        setCreateOpen(false);
        setDeleteOpen(false);
      }
      if (event.key !== "Tab" || !focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
    document.addEventListener("keydown", handleKey);
    return () => { document.removeEventListener("keydown", handleKey); previous?.focus(); };
  }, [createOpen, deleteOpen]);

  useEffect(() => {
    function shortcut(event: KeyboardEvent) {
      if (createOpen || deleteOpen || (event.target instanceof Element && event.target.closest('[role="dialog"], [role="alertdialog"]'))) return;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        searchFocusRequestedRef.current = true;
        setSearchFocusRequest((request) => request + 1);
        if (view !== "library") {
          window.history.pushState({}, "", viewPaths.library);
          setTypeFilter("all");
          setStatusFilter("all");
          setQuery("");
          setView("library");
        }
      } else if (!event.metaKey && !event.ctrlKey && !event.altKey && event.key.toLowerCase() === "n" && !(event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement)) {
        event.preventDefault();
        // Keep creation available from every page.
        setCreateOpen(true);
      }
    }
    document.addEventListener("keydown", shortcut);
    return () => document.removeEventListener("keydown", shortcut);
  }, [createOpen, deleteOpen, view]);

  const counts = useMemo(() => allSummaries.reduce<Record<ContentType, number>>((result, item) => {
    result[item.type] += 1;
    return result;
  }, { topic: 0, youtube: 0, linkedin: 0, x: 0, instagram: 0, tiktok: 0, email: 0, substack: 0 }), [allSummaries]);

  const scheduleBlockedIds = useMemo(() => {
    const blocked = new Set(schedulePendingIds);
    for (const [id, state] of Object.entries(saveStates)) if (state !== "saved") blocked.add(id);
    return blocked;
  }, [saveStates, schedulePendingIds]);
  const scheduleError = scheduleUncertainIds.size
    ? "A schedule update could not be confirmed. Reload before editing or moving the locked item."
    : calendarError;

  function navigate(next: View) {
    if (next === "topics" || next === "library") { setTypeFilter("all"); setStatusFilter("all"); setQuery(""); }
    if (window.location.pathname !== viewPaths[next]) window.history.pushState({}, "", viewPaths[next]);
    setView(next);
  }

  function openContent(id: string) {
    setActionError("");
    setSelectedId(id);
    const path = `/content?id=${encodeURIComponent(id)}`;
    if (window.location.pathname + window.location.search !== path) window.history.pushState({}, "", path);
    setView("workspace");
  }

  async function submitPasswordSignIn(event: React.FormEvent) {
    event.preventDefault();
    if (signInPending) return;
    setSignInPending(true);
    setSignInError("");
    try {
      const response = await fetch("/api/v1/auth/password", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: signInEmail, password: signInPassword }),
      });
      if (response.ok) {
        window.location.reload();
        return;
      }
      const problem = await response.json().catch(() => ({})) as { error?: string };
      setSignInError(problem.error === "rate_limit_exceeded"
        ? "Too many attempts. Wait a moment and try again."
        : "That email and password did not match.");
    } catch {
      setSignInError("Could not reach the server. Try again.");
    } finally {
      setSignInPending(false);
    }
  }

  function persistEnabledTypes(next: ContentType[]) {
    setEnabledTypes(next);
    try {
      window.localStorage.setItem(enabledTypesKey, JSON.stringify(next));
    } catch {
      // A browser with storage disabled still gets the change for this session.
    }
  }

  function toggleType(type: ContentType) {
    const isOn = enabledTypes.includes(type);
    // The last enabled type cannot be turned off; an empty menu has no way back.
    if (isOn && enabledTypes.length === 1) return;
    persistEnabledTypes(contentTypes.filter((candidate) => isOn ? candidate !== type && enabledTypes.includes(candidate) : candidate === type || enabledTypes.includes(candidate)));
    // Hiding the type currently being filtered would leave the library filtered
    // by something no longer reachable from the menu.
    if (isOn && typeFilter === type) setTypeFilter("all");
  }

  function setThemeChoice(next: Theme) {
    document.documentElement.dataset.theme = next;
    try {
      window.localStorage.setItem("contentflow-theme", next);
    } catch {
      // Theme still applies for this session.
    }
    setTheme(next);
  }

  function toggleTheme() {
    const current = document.documentElement.dataset.theme === "light" ? "light" : "dark";
    const next = current === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try {
      window.localStorage.setItem("contentflow-theme", next);
    } catch {
      // Theme still applies for this session.
    }
    setTheme(next);
  }

  async function resumeExpiredSession() {
    setReauthChecking(true);
    setReauthError("");
    try {
      const session = await loadSession();
      requestSequence.current += 1;
      if (!pendingLifecycleRef.current) lifecycleSynchronizationRef.current += 1;
      sessionGenerationRef.current += 1;
      csrfTokenRef.current = session.csrf_token ?? "";
      setCsrfToken(csrfTokenRef.current);
      setSessionExpired(false);
      autosaveRef.current?.resumeUnauthorized();
      setPendingSessionCreate(undefined);
      setPendingSessionLifecycle(undefined);
      if (pendingSessionCreate) void createItem(pendingSessionCreate.type, pendingSessionCreate.plan, pendingSessionCreate.topicId, pendingSessionCreate.format);
      if (pendingSessionLifecycle) void performLifecycle(pendingSessionLifecycle.document, pendingSessionLifecycle.action);
      setDetailReload((current) => current + 1);
      const refresh = refreshLibraryRef.current();
      const sequence = requestSequence.current;
      void refresh.catch((error) => {
        if (sequence !== requestSequence.current) return;
        if (isSessionRecoveryError(error)) {
          setSessionExpired(true);
        } else {
          setFilterError("The library filters could not be refreshed.");
        }
      });
    } catch {
      setReauthError("Your session is still expired. Finish signing in, then try again.");
    } finally {
      setReauthChecking(false);
    }
  }

  const receiveDocument = useCallback((detail: ContentDetail) => {
    setDocuments((current) => { const existing = current[detail.id]; return existing && existing.revision > detail.revision ? current : { ...current, [detail.id]: detail }; });
  }, []);
  const expireSession = useCallback(() => setSessionExpired(true), []);

  function updateDocument(detail: ContentDetail) {
    if (scheduleLock.has(detail.id) || pendingLifecycle?.id === detail.id) return;
    setDocuments((current) => ({ ...current, [detail.id]: detail }));
    setAllSummaries((items) => items.map((item) => item.id === detail.id ? { ...item, ...detail, asset_counts: item.asset_counts } : item));
    setSummaries((items) => items.map((item) => item.id === detail.id ? { ...item, ...detail, asset_counts: item.asset_counts } : item));
    autosaveManager?.enqueue(detail);
  }

  function updateSelected(change: (current: ContentDetail) => ContentDetail) {
    if (!selected || pendingLifecycle?.id === selected.id || scheduleLock.has(selected.id)) return;
    const next = change(selected);
    setSelected(next);
    setAllSummaries((items) => items.map((item) => item.id === next.id ? { ...item, working_title: next.working_title, status: next.status, topic_id: next.topic_id, format: next.format, scheduled_at: next.scheduled_at, updated_at: new Date().toISOString() } : item));
    setSummaries((items) => items.map((item) => item.id === next.id ? { ...item, working_title: next.working_title, status: next.status, topic_id: next.topic_id, format: next.format, scheduled_at: next.scheduled_at, updated_at: new Date().toISOString() } : item));
    autosaveManager?.enqueue(next);
  }

  async function createItem(type: ContentType, plan?: CreatePlan, topicId = "", format = ""): Promise<boolean> {
    if (csrfToken === undefined || createPendingRef.current) return false;
    lifecycleSynchronizationRef.current += 1;
    createPendingRef.current = true;
    setCreatePending(true);
    const setCreateError = plan ? setWeeklyCreateError : setActionError;
    setCreateError("");
    const operationKey = plan ? `weekly:${plan.attemptId}` : `${type}:${topicId}:${format}`;
    const existingOperation = createOperationsRef.current.get(operationKey);
    const operation = existingOperation ?? { operationId: newOperationId(), plan, topicId, format };
    createOperationsRef.current.set(operationKey, operation);
    const mutationSessionGeneration = sessionGenerationRef.current;
    let retryAfterStaleSession = false;
    let created = false;
    try {
      const result = await createContent(type, csrfTokenRef.current, operation.operationId, operation.plan ? { workingTitle: operation.plan.title, scheduledAt: scheduledAtFor(operation.plan.day) } : { topicId: operation.topicId, format: operation.format });
      created = true;
      if (plan) {
        setCompletedWeeklyAttemptId(plan.attemptId);
        setFrozenWeeklyPlan(undefined);
      }
      requestSequence.current += 1;
      setCreateError("");
      createOperationsRef.current.delete(operationKey);
      // Stay in the section the item was created from; clearing to "all" would
      // bounce the writer out of the type they deliberately filtered to.
      const retainedType = typeFilter === type ? type : "all";
      const clearedFilters: LibraryFilters = plan ? activeFiltersRef.current : { query: "", type: retainedType, status: "all" };
      if (!plan) {
        activeFiltersRef.current = clearedFilters;
        setQuery("");
        setTypeFilter(retainedType);
        setStatusFilter("all");
        openContent(topicId || result.item_ids[0]);
        setCreateOpen(false);
          }
      const refresh = refreshLibraryRef.current(clearedFilters);
      const refreshSequence = requestSequence.current;
      try {
        await refresh;
      } catch (error) {
        if (refreshSequence !== requestSequence.current) return created;
        if (isSessionRecoveryError(error)) {
          setSessionExpired(true);
        } else {
          setCreateError("The item was created, but the library could not be refreshed.");
        }
      }
    } catch (error) {
      if (isSessionRecoveryError(error)) {
        if (mutationSessionGeneration !== sessionGenerationRef.current) retryAfterStaleSession = true;
        else {
          setPendingSessionCreate({ type, plan, topicId, format });
          setSessionExpired(true);
        }
      }
      if (error instanceof ApiError && error.status < 500 && !isSessionRecoveryError(error)) {
        createOperationsRef.current.delete(operationKey);
        if (plan) setFrozenWeeklyPlan(undefined);
      }
      if (!isSessionRecoveryError(error)) {
        const uncertain = !(error instanceof ApiError) || error.status >= 500;
        if (plan && uncertain) setFrozenWeeklyPlan({ ...plan, type });
        setCreateError(plan && uncertain ? "The new item could not be confirmed. Retry with the same title." : "The new item could not be created.");
      }
    } finally {
      createPendingRef.current = false;
      setCreatePending(false);
    }
    if (retryAfterStaleSession) return createItem(type, plan, topicId, format);
    return created;
  }

  // A type section is already an answer to "what are you creating?", so skip the
  // picker there and only ask when the writer is in All content.
  // Scheduling is a plain replacement of the whole item. It waits for the
  // editor queue to become idle, then rebases that queue onto the saved result.
  async function rescheduleItem(id: string, day: string | undefined) {
    if (csrfToken === undefined) return;
    if (scheduleLock.has(id)) return;
    if (autosaveRef.current?.isBusy(id)) {
      setCalendarError("Wait for this item's edits to finish saving before rescheduling it.");
      return;
    }
    scheduleLock.add(id);
    setSchedulePendingIds(scheduleLock.snapshot());
    setCalendarError("");
    let releaseScheduleLock = true;
    try {
      let detail: ContentDetail | undefined;
      let scheduled: string | undefined;
      try {
        detail = await getContent(id);
        scheduled = day ? new Date(`${day}T09:00:00`).toISOString() : undefined;
        const body = serializeReplacement({ ...detail, scheduled_at: scheduled }, newOperationId());
        let result;
        try {
          result = await replaceContent(id, body, csrfTokenRef.current);
        } catch (error) {
          if (error instanceof ApiError) throw error;
          // The first request may have committed before its response was lost.
          // Replaying identical bytes is safe because the operation ID is retained.
          result = await replaceContent(id, body, csrfTokenRef.current);
        }
        const optimistic = {
          ...detail,
          scheduled_at: scheduled,
          revision: result.revisions[0] ?? detail.revision + 1,
          updated_at: new Date().toISOString(),
        };
        if (!(autosaveRef.current?.reconcileExternal(optimistic)) && selectedIdRef.current === id) setSelected(optimistic);
      } catch (error) {
        if (isSessionRecoveryError(error)) setSessionExpired(true);
        else if (error instanceof ApiError) setCalendarError("That item could not be rescheduled. Try again.");
        else {
          try {
            const current = await getContent(id);
            if (!(autosaveRef.current?.reconcileExternal(current)) && selectedIdRef.current === id) setSelected(current);
            const requestedDay = day || undefined;
            const currentDay = current.scheduled_at ? dayKey(new Date(current.scheduled_at)) : undefined;
            setCalendarError(detail && currentDay === requestedDay && current.revision > detail.revision
              ? "The item was moved, but the response was lost. Its latest details are now loaded."
              : "That item could not be rescheduled. Try again.");
          } catch (confirmationError) {
            if (isSessionRecoveryError(confirmationError)) setSessionExpired(true);
            releaseScheduleLock = false;
            setScheduleUncertainIds((ids) => new Set(ids).add(id));
          }
        }
        return;
      }

      let refreshFailed = false;
      try {
        const saved = await getContent(id);
        if (!(autosaveRef.current?.reconcileExternal(saved)) && selectedIdRef.current === id) setSelected(saved);
      } catch (error) {
        refreshFailed = true;
        if (isSessionRecoveryError(error)) setSessionExpired(true);
      }
      try {
        requestSequence.current += 1;
        await refreshLibraryRef.current();
      } catch (error) {
        refreshFailed = true;
        if (isSessionRecoveryError(error)) setSessionExpired(true);
      }
      if (refreshFailed) setCalendarError("The item was moved, but its latest details could not be refreshed. Reload to confirm.");
    } finally {
      if (releaseScheduleLock) scheduleLock.remove(id);
      setSchedulePendingIds(scheduleLock.snapshot());
    }
  }

  function startCreate() {
    if (view === "topics") { void createItem("topic"); return; }
    if (typeFilter !== "all") void createItem(typeFilter);
    else setCreateOpen(true);
  }

  async function confirmDelete() {
    if (!selected || csrfToken === undefined) return;
    setDeleteOpen(false);
    await performLifecycle(selected, "delete");
  }

  async function performLifecycle(document: ContentDetail, action: LifecycleAction) {
    if (pendingLifecycleRef.current) return;
    const pending = { id: document.id, action };
    const operationPrefix = `${document.id}:${action}:`;
    const operationKey = `${operationPrefix}${document.revision}`;
    for (const key of lifecycleOperationIdsRef.current.keys()) {
      if (key.startsWith(operationPrefix) && key !== operationKey) lifecycleOperationIdsRef.current.delete(key);
    }
    const operationId = lifecycleOperationIdsRef.current.get(operationKey) ?? newOperationId();
    lifecycleOperationIdsRef.current.set(operationKey, operationId);
    const synchronizationGeneration = lifecycleSynchronizationRef.current + 1;
    lifecycleSynchronizationRef.current = synchronizationGeneration;
    const mutationSessionGeneration = sessionGenerationRef.current;
    let retryAfterStaleSession = false;
    pendingLifecycleRef.current = pending;
    setPendingLifecycle(pending);
    setActionError("");
    try {
      if (action === "delete") {
        await deleteContent(document.id, document.revision, csrfTokenRef.current, operationId);
        requestSequence.current += 1;
        setActionError("");
        lifecycleOperationIdsRef.current.delete(operationKey);
        autosaveRef.current?.discard(document.id);
        const remaining = allSummariesRef.current.filter((item) => item.id !== document.id);
        const visibleRemaining = summariesRef.current.filter((item) => item.id !== document.id);
        allSummariesRef.current = remaining;
        summariesRef.current = visibleRemaining;
        setAllSummaries((items) => items.filter((item) => item.id !== document.id));
        setSummaries((items) => items.filter((item) => item.id !== document.id));
        const deletedSelection = selectedIdRef.current === document.id;
        if (deletedSelection) {
          selectedIdRef.current = "";
          setSelectedId("");
          setSelected(undefined);
          setConflict(undefined);
          if (viewFromPath(window.location.pathname) === "workspace") navigate(document.type === "topic" ? "topics" : "library");
        }
        pendingLifecycleRef.current = undefined;
        setPendingLifecycle(undefined);
        const refresh = refreshLibraryRef.current();
        const refreshSequence = requestSequence.current;
        try {
          await refresh;
          if (refreshSequence !== requestSequence.current || lifecycleSynchronizationRef.current !== synchronizationGeneration) return;
        } catch (error) {
          if (refreshSequence !== requestSequence.current || lifecycleSynchronizationRef.current !== synchronizationGeneration) return;
          if (isSessionRecoveryError(error)) {
            setSessionExpired(true);
          } else {
            setActionError("The item was deleted, but the library could not be refreshed.");
          }
        }
        return;
      }
    } catch (error) {
      if (error instanceof ApiError && error.status === 409 && error.current) {
        lifecycleOperationIdsRef.current.delete(operationKey);
        pendingLifecycleRef.current = { id: document.id, action };
        autosaveRef.current?.beginConflict(document, error.current);
      } else if (isSessionRecoveryError(error)) {
        pendingLifecycleRef.current = undefined;
        setPendingLifecycle(undefined);
        if (mutationSessionGeneration !== sessionGenerationRef.current) retryAfterStaleSession = true;
        else {
          setPendingSessionLifecycle({ document, action });
          setSessionExpired(true);
        }
      } else {
        if (error instanceof ApiError && error.status < 500) lifecycleOperationIdsRef.current.delete(operationKey);
        pendingLifecycleRef.current = undefined;
        setPendingLifecycle(undefined);
        if (selectedIdRef.current === document.id && lifecycleSynchronizationRef.current === synchronizationGeneration) setActionError(error instanceof ApiError && error.code === "topic_not_empty" ? "Move the related pieces to standalone content or delete them before deleting this topic group." : "The item could not be deleted.");
      }
    }
    if (retryAfterStaleSession) void performLifecycle(document, action);
  }

  function resolveSelectedConflict(choice: "server" | "local") {
    if (!selected) return;
    autosaveRef.current?.resolveConflict(selected.id, choice);
  }

  function resolveLifecycleConflict(retry: boolean) {
    if (!selected) return;
    const manager = autosaveRef.current;
    const currentConflict = manager?.getConflict(selected.id);
    const pending = pendingLifecycleRef.current;
    manager?.resolveConflict(selected.id, "server");
    pendingLifecycleRef.current = undefined;
    setPendingLifecycle(undefined);
    if (retry && pending?.id === selected.id && currentConflict) void performLifecycle(currentConflict.server, pending.action);
  }

  if (authState === "loading") return <main className="centered-state"><LoaderCircle className="spin" /><h1>Opening ContentFlow</h1><p>Loading your workspace…</p></main>;
  if (authState === "signed-out") return <main className="centered-state sign-in-state">
    <div className="brand-mark"><Zap size={20} fill="currentColor" /></div>
    <h1>ContentFlow</h1>
    <p>Sign in to open your content workspace.</p>
    <form className="sign-in-form" onSubmit={(event) => void submitPasswordSignIn(event)}>
      <label>Email<Input type="email" autoComplete="username" required value={signInEmail} onChange={(event) => setSignInEmail(event.target.value)} /></label>
      <label>Password<Input type="password" autoComplete="current-password" required value={signInPassword} onChange={(event) => setSignInPassword(event.target.value)} /></label>
      {signInError && <p className="inline-error" role="alert">{signInError}</p>}
      <Button className="primary-button" type="submit" disabled={signInPending}>{signInPending ? "Signing in…" : "Sign in"}</Button>
    </form>
    <div className="sign-in-divider"><span>or</span></div>
    <a className="secondary-button" href="/api/v1/auth/login">Sign in with Google</a>
  </main>;
  if (authState === "error") return <main className="centered-state"><AlertTriangle /><h1>ContentFlow is unavailable</h1><p>{loadError}</p><Button className="primary-button" onClick={() => window.location.reload()}>Try again</Button></main>;
  if (sessionExpired) return <main className="centered-state"><AlertTriangle /><h1>Your session expired</h1><p>{pendingSessionLifecycle ? `Your ${pendingSessionLifecycle.action} action for “${displayTitle(pendingSessionLifecycle.document)}” is waiting. Sign in in a new tab, then return here to retry it.` : "Your unsaved changes are still queued. Sign in in a new tab, then return here to continue saving."}</p><a className="primary-button" href="/api/v1/auth/login" target="_blank" rel="noreferrer">Open sign in</a><Button className="secondary-button" disabled={reauthChecking} onClick={() => void resumeExpiredSession()}>{reauthChecking ? "Checking…" : "I’ve signed in"}</Button>{reauthError && <p className="inline-error" role="alert">{reauthError}</p>}</main>;

  const createLabel = typeFilter === "all" ? "New content" : `New ${typeMeta[typeFilter].label}`;
  const currentSaveState = selected ? saveStates[selected.id] ?? "saved" : "saved";
  const selectedPendingLifecycle = selected && pendingLifecycle?.id === selected.id ? pendingLifecycle : undefined;
  const foreignPendingLifecycle = selected && pendingLifecycle && pendingLifecycle.id !== selected.id && autosaveManager?.getConflict(pendingLifecycle.id) ? pendingLifecycle : undefined;
  const foreignPendingLifecycleItem = foreignPendingLifecycle ? allSummaries.find((item) => item.id === foreignPendingLifecycle.id) : undefined;
  const foreignPendingLifecycleTitle = foreignPendingLifecycleItem ? displayTitle(foreignPendingLifecycleItem) : undefined;
  const selectedSchedulePending = Boolean(selected && schedulePendingIds.has(selected.id));
  const editorLocked = selectedSchedulePending || Boolean(selectedPendingLifecycle);
  const lifecycleDisabled = currentSaveState !== "saved" || Boolean(pendingLifecycle) || selectedSchedulePending;

  const visibleItems = summaries.filter((item) => view === "topics" ? item.type === "topic" : item.type !== "topic");
  const listTitle = view === "topics" ? "Topics" : "Library";
  return <main className="app-shell app-redesign">
    <header className="app-topbar">
      <Button className="app-brand" onClick={() => navigate("topics")} aria-label="ContentFlow home"><div className="brand-mark"><Zap size={17} fill="currentColor" /></div><span className="brand-name">ContentFlow</span></Button>
      <nav className="app-nav" aria-label="Main navigation">{([["weekly", "Week"], ["topics", "Topics"], ["library", "Library"], ["settings", "Settings"]] as const).map(([page, label]) => <Button key={page} className={view === page ? "active" : ""} aria-current={view === page ? "page" : undefined} onClick={() => navigate(page)}>{label}</Button>)}</nav>
      <div className="app-topbar-actions"><Button className="icon-button" onClick={toggleTheme} aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}>{theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}</Button><Button className="primary-button" onClick={() => { setCreateTopicId(""); setCreateOpen(true); }}><Plus size={16} />New content</Button></div>
    </header>

    {(view === "topics" || view === "library") && <section className="content-list-page" aria-label={view === "topics" ? "Topic groups" : "Content library"}>
      <header className="content-list-heading"><div><p className="eyebrow">Your workspace</p><h1>{listTitle}</h1><p>{view === "topics" ? "One idea. Every version, together." : "Find your posts, scripts, and videos."}</p></div><Button className="primary-button" disabled={createPending} onClick={startCreate}><Plus size={16} />{view === "topics" ? "New topic" : "New piece"}</Button></header>
      <div className="content-list-filters">{view === "library" && <label className="search-box"><Search size={17} /><Input ref={searchRef} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search content" aria-label="Search content titles" /><span className="key-hint">⌘ K</span></label>}
      {view === "library" && <Select aria-label="Filter by platform" value={typeFilter} onChange={(event) => setTypeFilter(event.target.value as ContentType | "all")}><option value="all">All platforms</option>{enabledTypes.map((type) => <option value={type} key={type}>{typeMeta[type].label}</option>)}</Select>}
      <Select aria-label="Filter by status" value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as ContentStatus | "all")}><option value="all">All statuses</option>{contentStatuses.map((status) => <option value={status} key={status}>{statusLabels[status]}</option>)}</Select></div>
      {filterError && <p className="inline-error" role="alert">{filterError}</p>}
      {actionError && <p className="inline-error" role="alert">{actionError}</p>}
      <div className="library-summary"><span>{visibleItems.length} {view === "topics" ? "topics" : "pieces"}</span><span>Last edited</span></div>
      <div className="content-list-grid">{visibleItems.map((item) => <Button className="content-card" key={item.id} onClick={() => openContent(item.id)}><span className="card-icon" style={{ color: typeMeta[item.type].color }}><TypeIcon type={item.type} size={20} /></span><span className="card-copy"><strong>{displayTitle(item)}</strong><span className="card-meta"><span className={`status-dot ${item.status}`} />{statusLabels[item.status]} · {formatRelativeTime(item.updated_at)}</span>{item.type === "topic" ? <span className="topic-piece-count">{allSummaries.filter((piece) => piece.topic_id === item.id).length} related pieces</span> : <span>{typeMeta[item.type].label}{item.format ? ` · ${item.format}` : ""}{item.topic_id ? ` · ${displayTitle(allSummaries.find((topic) => topic.id === item.topic_id) ?? item)}` : " · Standalone"}</span>}</span><span className="card-arrow">›</span></Button>)}</div>
      {!visibleItems.length && <div className="empty-state"><SquarePen size={28} /><h2>{query || statusFilter !== "all" || typeFilter !== "all" ? "No matches" : view === "topics" ? "Start with a topic" : "Your library is empty"}</h2><p>{view === "topics" ? "Bring an idea and its platform versions into one place." : "Create a standalone piece or add one to a topic."}</p><Button onClick={startCreate}>{view === "topics" ? "Create a topic" : "Create a piece"}</Button></div>}
    </section>}

    {view === "workspace" && <section className="editor-panel focus-workspace" aria-label="Content editor">
      <div className="workspace-back"><Button className="text-button" onClick={() => selected?.topic_id ? openContent(selected.topic_id) : navigate(selected?.type === "topic" ? "topics" : "library")}><ArrowLeft size={16} />{selected?.topic_id ? "Back to topic" : selected?.type === "topic" ? "Back to topics" : "Back to library"}</Button></div>
      {selected ? <>
        <div className="editor-toolbar"><div className="editor-context"><span className="type-pill" style={{ color: typeMeta[selected.type].color }}><TypeIcon type={selected.type} />{typeMeta[selected.type].label}</span><span className="toolbar-divider" /><label className="status-select"><span className={`status-dot ${selected.status}`} /><Select aria-label="Content status" value={selected.status} disabled={editorLocked} onChange={(event) => updateSelected((current) => ({ ...current, status: event.target.value as ContentStatus }))}>{contentStatuses.map((status) => <option value={status} key={status}>{statusLabels[status]}</option>)}</Select><ChevronDown size={14} /></label></div><div className="editor-actions">{selectedSchedulePending ? <span className="saved-state saving" role="status"><LoaderCircle className="spin" size={14} />Updating schedule…</span> : <span className={`saved-state ${currentSaveState}`} aria-live="polite">{currentSaveState === "saving" || currentSaveState === "retrying" ? <LoaderCircle className="spin" size={14} /> : currentSaveState === "conflict" || currentSaveState === "error" ? <AlertTriangle size={14} /> : <Check size={14} />}{saveLabel(currentSaveState)}</span>}</div></div>
        {foreignPendingLifecycle && <div className="inline-error" role="alert">Review the {foreignPendingLifecycle.action} conflict for “{foreignPendingLifecycleTitle ?? "another item"}” before continuing. <Button onClick={() => { setActionError(""); openContent(foreignPendingLifecycle.id); }}>Review item</Button></div>}
        {actionError && <div className="inline-error" role="alert">{actionError}</div>}
        {currentSaveState === "error" && <div className="inline-error" role="alert">Check the fields below. Your edits have not been saved. <Button onClick={() => autosaveManager?.enqueue(selected)}>Retry save</Button></div>}
        <div className="editor-scroll"><article className={`editor-document ${selected.type === "topic" ? "topic-document" : ""}`}>
          <div className="document-heading" inert={editorLocked}>{selected.topic_id && <Button className="topic-back" onClick={() => openContent(selected.topic_id!)}><ArrowLeft size={14} />{allSummaries.find((item) => item.id === selected.topic_id)?.working_title || "Open topic group"}</Button>}<label className="document-field document-field-large"><span>{selected.type === "topic" ? "Topic" : "Working title"}</span><Input aria-label="Working title" value={selected.working_title} placeholder={selected.type === "topic" ? "What is the idea?" : "Name this piece"} onChange={(event) => updateSelected((current) => ({ ...current, working_title: event.target.value }))} /></label></div>
          {conflict && <section className="conflict-panel" aria-labelledby="conflict-title"><div className="conflict-title"><AlertTriangle size={18} /><div><h2 id="conflict-title">This item changed elsewhere</h2><p>{selectedPendingLifecycle ? `Review the current server version before you retry or cancel ${selectedPendingLifecycle.action}.` : "Compare the saved server version with your unsaved local work. Nothing was overwritten."}</p></div></div><div className="conflict-columns"><div><h3>Server version</h3><pre>{JSON.stringify(editableSnapshot(conflict.server), null, 2)}</pre></div><div><h3>{selectedPendingLifecycle ? "Previous version" : "Your unsaved version"}</h3><pre>{JSON.stringify(editableSnapshot(conflict.local), null, 2)}</pre></div></div><div className="conflict-actions">{selectedPendingLifecycle ? <><Button onClick={() => resolveLifecycleConflict(false)}>Cancel action</Button><Button className="primary-button" onClick={() => resolveLifecycleConflict(true)}>Retry {selectedPendingLifecycle.action}</Button></> : <><Button onClick={() => resolveSelectedConflict("server")}>Use server version</Button><Button className="primary-button" onClick={() => resolveSelectedConflict("local")}>Save my version</Button></>}</div></section>}
          <div className="editor-content" inert={editorLocked}><PieceEditor disabled={editorLocked} key={selected.id} csrfToken={csrfToken ?? ""} onSessionExpired={expireSession} detail={selected} topics={allSummaries.filter((item) => item.type === "topic")} onChange={(detail) => updateSelected(() => detail)} showMetadata={false} /></div>
          {selected.type === "topic" && <TopicPieces csrfToken={csrfToken ?? ""} key={selected.id} topicId={selected.id} items={allSummaries.filter((item) => item.topic_id === selected.id).sort((a, b) => a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id))} topics={allSummaries.filter((item) => item.type === "topic")} enabledTypes={enabledTypes} documents={documents} states={saveStates} manager={autosaveManager} blockedIds={new Set([...schedulePendingIds, ...(pendingLifecycle ? [pendingLifecycle.id] : [])])} onChange={updateDocument} onLoaded={receiveDocument} onSessionExpired={expireSession} onOpen={openContent} onCreate={(type, format, topicId) => createItem(type, undefined, topicId, format)} createPending={createPending} />}
        </article></div>
        <footer className="editor-footer"><span>{typeMeta[selected.type].description}</span><Button className="delete-button" disabled={lifecycleDisabled} onClick={() => setDeleteOpen(true)}><Trash2 size={15} /> Delete</Button></footer>
      </> : <div className="editor-empty">{detailLoading ? <><LoaderCircle className="spin" /><p>Loading selected content…</p></> : detailError ? <><AlertTriangle /><h1>Could not open this item</h1><p role="alert">{detailError}</p><Button className="primary-button" onClick={() => setDetailReload((value) => value + 1)}>Retry loading item</Button></> : <><SquarePen size={28} /><h1>{allSummaries.length ? "Choose an item" : "Start writing"}</h1><p>{allSummaries.length ? "Select content from your library." : "Pick a format to create your first piece."}</p>{actionError && <p className="inline-error" role="alert">{actionError}</p>}<Button className="primary-button" onClick={startCreate}><Plus size={16} /> {createLabel}</Button></>}</div>}
    </section>}

    {view === "calendar" && <Calendar items={allSummaries.filter((item) => item.type !== "topic")} onOpen={openContent} onSchedule={(id, day) => void rescheduleItem(id, day)} blockedIds={scheduleBlockedIds} pendingIds={schedulePendingIds} error={scheduleError} />}

    {view === "weekly" && <WeeklyMatrix items={allSummaries.filter((item) => item.type !== "topic")} topics={allSummaries.filter((item) => item.type === "topic")} enabledTypes={enabledTypes} onOpen={openContent} onSchedule={(id, day) => void rescheduleItem(id, day)} onCreate={(type, day, title, attemptId) => createItem(type, { day, title, attemptId })} createPending={createPending} createError={weeklyCreateError} completedAttemptId={completedWeeklyAttemptId} frozenPlan={frozenWeeklyPlan} blockedIds={scheduleBlockedIds} pendingIds={schedulePendingIds} error={scheduleError} />}

    {view === "settings" && <Settings theme={theme} onThemeChange={setThemeChoice} enabledTypes={enabledTypes} onToggleType={toggleType} counts={counts} workspaceId={workspaceId} />}

    {createOpen && <div className="modal-backdrop"><section ref={createModalRef} className="modal-card" role="dialog" aria-modal="true" aria-labelledby="create-title"><div className="modal-header"><div><p className="eyebrow">New content</p><h2 id="create-title">What are you creating?</h2><p>Choose a format. You can change its status as the work develops.</p></div><Button className="icon-button" onClick={() => setCreateOpen(false)} aria-label="Close create dialog"><X size={19} /></Button></div><label className="resource-field"><span>Topic group</span><Select aria-label="Create in topic group" value={createTopicId} onChange={(event) => setCreateTopicId(event.target.value)}><option value="">Standalone piece</option>{allSummaries.filter((item) => item.type === "topic").map((topic) => <option key={topic.id} value={topic.id}>{displayTitle(topic)}</option>)}</Select></label><Button className="secondary-button" disabled={createPending} onClick={() => void createItem("topic")}>New topic group</Button><div className="create-grid" aria-busy={createPending}>{enabledTypes.map((type) => <Button key={type} disabled={createPending} onClick={() => void createItem(type, undefined, createTopicId)}><span className="create-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} size={19} /></span><span><strong>{typeMeta[type].label}</strong><small>{typeMeta[type].description}</small></span><span className="create-arrow">›</span></Button>)}</div>{createPending && <p aria-live="polite">Creating…</p>}{actionError && <p className="inline-error" role="alert">{actionError}</p>}</section></div>}
    {deleteOpen && selected && <div className="modal-backdrop"><section ref={deleteModalRef} className="modal-card confirm-card" role="alertdialog" aria-modal="true" aria-labelledby="delete-title" aria-describedby="delete-description"><div className="modal-header"><div><p className="eyebrow">Permanent deletion</p><h2 id="delete-title">Delete “{displayTitle(selected)}”?</h2><p id="delete-description">This removes the item immediately and cannot be undone.</p></div></div><div className="confirm-actions"><Button onClick={() => setDeleteOpen(false)}>Cancel</Button><Button className="danger-button" onClick={() => void confirmDelete()}><Trash2 size={15} /> Delete permanently</Button></div></section></div>}
  </main>;
}
