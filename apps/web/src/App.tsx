import {
  AlertTriangle,
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  CalendarDays,
  Check,
  ChevronDown,
  Clock3,
  FileText,
  FileVideo,
  ImagePlus,
  Inbox,
  Lightbulb,
  LoaderCircle,
  Menu,
  MoreHorizontal,
  MousePointerClick,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Paperclip,
  Plus,
  Search,
  Settings as SettingsIcon,
  SquarePen,
  Sun,
  Target,
  Trash2,
  Upload,
  Users,
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
  newClientKey,
  newOperationId,
  replaceContent,
  serializeReplacement,
  type ContentDetail,
  type ContentPayload,
  type ContentStatus,
  type ContentSummary,
  type ContentType,
  type Section,
  type YouTubeContent,
} from "./api";
import AutoTextarea from "./AutoTextarea";
import Calendar from "./Calendar";
import Settings from "./Settings";
import { TypeIcon, displayTitle, statusLabels, typeMeta } from "./content-meta";
import { AutosaveManager, type ConflictView, type SaveState } from "./autosave";
import { normalizeUnicode15Title } from "./unicode-normalization";

type Theme = "light" | "dark";
type View = "workspace" | "calendar" | "settings";

// The server serves index.html for any extensionless path, so views are real
// URLs: deep links and the browser back button both work.
function viewFromPath(pathname: string): View {
  if (pathname.startsWith("/calendar")) return "calendar";
  if (pathname.startsWith("/settings")) return "settings";
  return "workspace";
}

const viewPaths: Record<View, string> = { workspace: "/", calendar: "/calendar", settings: "/settings" };
type LifecycleAction = "delete";
type PendingLifecycle = { id: string; action: LifecycleAction };
type LibraryFilters = { query: string; type: ContentType | "all"; status: ContentStatus | "all" };
type Attachment = { id: string; kind: "image" | "video" | "pdf"; name: string; size: number };
type ThumbnailPreview = { dataUrl?: string; requestId: string };

const enabledTypesKey = "contentflow-enabled-types";
const sidebarCollapsedKey = "contentflow-sidebar-collapsed";
const libraryCollapsedKey = "contentflow-library-collapsed";

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

function readSidebarCollapsed(): boolean {
  try {
    return window.localStorage.getItem(sidebarCollapsedKey) === "true";
  } catch {
    return false;
  }
}

function readLibraryCollapsed(): boolean {
  try {
    return window.localStorage.getItem(libraryCollapsedKey) === "true";
  } catch {
    return false;
  }
}

function AttachmentIcon({ kind }: { kind: Attachment["kind"] }) {
  if (kind === "image") return <ImagePlus size={16} />;
  if (kind === "pdf") return <FileText size={16} />;
  return <FileVideo size={16} />;
}

function wordCount(value: string) {
  return value.trim() ? value.trim().split(/\s+/).length : 0;
}

function normalizeSearchTitle(value: string) {
  return normalizeUnicode15Title(value);
}

function documentWordCount(detail: ContentDetail) {
  if (detail.type === "youtube") {
    const content = detail.content as YouTubeContent;
    return content.sections.reduce((total, section) => total + wordCount(section.body), 0) + wordCount(content.transcript);
  }
  if ("body" in detail.content) return wordCount(detail.content.body);
  if ("script" in detail.content) return wordCount(detail.content.script);
  return 0;
}

function formatRelativeTime(value: string) {
  const elapsed = Date.now() - new Date(value).getTime();
  if (elapsed < 60_000) return "just now";
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)} min ago`;
  if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)} hr ago`;
  const days = Math.floor(elapsed / 86_400_000);
  return days === 1 ? "yesterday" : `${days} days ago`;
}

function expiry(value: string) {
  const date = new Date(value);
  const remainingMilliseconds = date.getTime() - Date.now();
  const expired = remainingMilliseconds <= 0;
  const days = Math.max(0, Math.ceil(remainingMilliseconds / 86_400_000));
  return {
    label: date.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }),
    days,
    summary: expired ? "Expired" : `Expires in ${days} ${days === 1 ? "day" : "days"}`,
    detail: expired ? "expired" : `${days} ${days === 1 ? "day" : "days"} left`,
    expired,
    warning: days <= 7,
  };
}

function nextExpiryUpdateDelay(items: ContentSummary[]) {
  const now = Date.now();
  const maximumTimerDelay = 2_147_000_000;
  return Math.min(maximumTimerDelay, ...items.map((item) => {
    const remaining = new Date(item.expires_at).getTime() - now;
    if (remaining <= 0) return 0;
    const displayedDays = Math.ceil(remaining / 86_400_000);
    return remaining - Math.max(0, displayedDays - 1) * 86_400_000;
  }));
}

function formatFileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1024; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${unit}`;
}

function uniqueId(prefix: string) {
  return `${prefix}-${crypto.randomUUID()}`;
}

function editableSnapshot(detail: ContentDetail) {
  return { working_title: detail.working_title, status: detail.status, content: detail.content };
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
  const [selectedId, setSelectedId] = useState("");
  const [selected, setSelected] = useState<ContentDetail>();
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
  const [pendingSessionCreate, setPendingSessionCreate] = useState<ContentType>();
  const [pendingSessionLifecycle, setPendingSessionLifecycle] = useState<{ document: ContentDetail; action: LifecycleAction }>();
  const [loadError, setLoadError] = useState("");
  const [detailError, setDetailError] = useState("");
  const [detailReload, setDetailReload] = useState(0);
  const [detailLoading, setDetailLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [view, setView] = useState<View>(() => viewFromPath(window.location.pathname));
  const [calendarError, setCalendarError] = useState("");
  const [workspaceId, setWorkspaceId] = useState<string>();
  const [enabledTypes, setEnabledTypes] = useState<ContentType[]>(readEnabledTypes);
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(readSidebarCollapsed);
  const [libraryCollapsed, setLibraryCollapsed] = useState<boolean>(readLibraryCollapsed);
  const [createPending, setCreatePending] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [isCompact, setIsCompact] = useState(false);
  const [theme, setTheme] = useState<Theme>("dark");
  const [saveStates, setSaveStates] = useState<Record<string, SaveState>>({});
  const [autosaveManager, setAutosaveManager] = useState<AutosaveManager>();
  const [conflict, setConflict] = useState<ConflictView>();
  const [pendingLifecycle, setPendingLifecycle] = useState<PendingLifecycle>();
  const [actionError, setActionError] = useState("");
  const [filterError, setFilterError] = useState("");
  const [expiryClock, setExpiryClock] = useState(0);
  const [attachments, setAttachments] = useState<Record<string, Attachment[]>>({});
  const [thumbnailPreviews, setThumbnailPreviews] = useState<Record<string, ThumbnailPreview>>({});
  const createModalRef = useRef<HTMLElement>(null);
  const deleteModalRef = useRef<HTMLElement>(null);
  const libraryPanelRef = useRef<HTMLElement>(null);
  const libraryCollapseButtonRef = useRef<HTMLButtonElement>(null);
  const libraryExpandButtonRef = useRef<HTMLButtonElement>(null);
  const libraryToggleRequestedRef = useRef(false);
  const mobileLibraryButtonRef = useRef<HTMLButtonElement>(null);
  const libraryWasOpenRef = useRef(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const selectedIdRef = useRef("");
  const allSummariesRef = useRef<ContentSummary[]>([]);
  const summariesRef = useRef<ContentSummary[]>([]);
  const autosaveRef = useRef<AutosaveManager | undefined>(undefined);
  const csrfTokenRef = useRef("");
  const createPendingRef = useRef(false);
  const createOperationIdsRef = useRef(new Map<ContentType, string>());
  const lifecycleOperationIdsRef = useRef(new Map<string, string>());
  const lifecycleSynchronizationRef = useRef(0);
  const pendingLifecycleRef = useRef<PendingLifecycle | undefined>(undefined);
  const pendingDeletedSelectionRef = useRef(false);
  const expiryRefreshAfterRef = useRef(0);
  const refreshLibraryRef = useRef<(filters?: LibraryFilters) => Promise<void>>(async () => undefined);
  const requestSequence = useRef(0);
  const sessionGenerationRef = useRef(0);
  const activeFiltersRef = useRef<LibraryFilters>({ query: "", type: "all", status: "all" });

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
      return draft ? { ...item, working_title: draft.working_title, status: draft.status } : item;
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
      const replacementId = mergedVisible[0]?.id ?? "";
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
    if (pendingDeletedSelectionRef.current) {
      pendingDeletedSelectionRef.current = false;
      if (!selectedIdRef.current) {
        const replacementId = mergedVisible[0]?.id;
        if (replacementId) {
          selectedIdRef.current = replacementId;
          setSelectedId(replacementId);
        }
      }
    }
    setFilterError("");
  }, [query, statusFilter, typeFilter]);

  useEffect(() => {
    selectedIdRef.current = selectedId;
    if (selectedId) pendingDeletedSelectionRef.current = false;
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
        setSelectedId((current) => current || items[0]?.id || "");
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
    if (authState !== "ready" || !allSummaries.length) return;
    const timer = window.setTimeout(() => {
      const expiredItems = allSummariesRef.current.filter((item) => new Date(item.expires_at).getTime() <= Date.now());
      if (expiredItems.length) expiryRefreshAfterRef.current = Date.now() + 30_000;
      setExpiryClock((current) => current + 1);
      if (!expiredItems.length) return;

      const refresh = refreshLibraryRef.current();
      const sequence = requestSequence.current;
      void refresh.then(() => {
        if (sequence === requestSequence.current && !allSummariesRef.current.some((item) => new Date(item.expires_at).getTime() <= Date.now())) expiryRefreshAfterRef.current = 0;
      }).catch((error) => {
        if (sequence !== requestSequence.current) return;
        if (isSessionRecoveryError(error)) setSessionExpired(true);
        else setFilterError("The library could not be refreshed after content expired.");
      });
    }, Math.max(nextExpiryUpdateDelay(allSummaries) + 25, expiryRefreshAfterRef.current - Date.now()));
    return () => window.clearTimeout(timer);
  }, [allSummaries, authState, expiryClock]);

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
    const sync = () => setView(viewFromPath(window.location.pathname));
    window.addEventListener("popstate", sync);
    return () => window.removeEventListener("popstate", sync);
  }, []);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const media = window.matchMedia("(max-width: 900px)");
    const sync = () => setIsCompact(media.matches);
    sync();
    media.addEventListener("change", sync);
    return () => media.removeEventListener("change", sync);
  }, []);

  useEffect(() => {
    if (!isCompact) {
      libraryWasOpenRef.current = false;
      return;
    }
    if (!libraryOpen) {
      if (libraryWasOpenRef.current) mobileLibraryButtonRef.current?.focus();
      libraryWasOpenRef.current = false;
      return;
    }
    const panel = libraryPanelRef.current;
    if (!panel) return;
    libraryWasOpenRef.current = true;
    const focusable = Array.from(panel.querySelectorAll<HTMLElement>('button:not([disabled]), input, textarea, select, [tabindex]:not([tabindex="-1"])'));
    focusable[0]?.focus();
    function handleKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        setLibraryOpen(false);
        return;
      }
      if (event.key !== "Tab" || !focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [isCompact, libraryOpen]);

  useEffect(() => {
    if (!libraryToggleRequestedRef.current || isCompact) return;
    libraryToggleRequestedRef.current = false;
    (libraryCollapsed ? libraryExpandButtonRef : libraryCollapseButtonRef).current?.focus();
  }, [isCompact, libraryCollapsed]);

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
      if (createOpen || deleteOpen) return;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        searchRef.current?.focus();
      } else if (!event.metaKey && !event.ctrlKey && !event.altKey && event.key.toLowerCase() === "n" && !(event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement)) {
        event.preventDefault();
        // Mirrors the sidebar button this shortcut is labelled on: always ask.
        setCreateOpen(true);
      }
    }
    document.addEventListener("keydown", shortcut);
    return () => document.removeEventListener("keydown", shortcut);
  }, [createOpen, deleteOpen]);

  const counts = useMemo(() => allSummaries.reduce<Record<ContentType, number>>((result, item) => {
    result[item.type] += 1;
    return result;
  }, { youtube: 0, linkedin: 0, x: 0, instagram: 0, tiktok: 0, email: 0, substack: 0 }), [allSummaries]);

  function navigate(next: View) {
    if (window.location.pathname !== viewPaths[next]) window.history.pushState({}, "", viewPaths[next]);
    setView(next);
    setLibraryOpen(false);
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

  function toggleSidebar() {
    setSidebarCollapsed((current) => {
      const next = !current;
      try {
        window.localStorage.setItem(sidebarCollapsedKey, String(next));
      } catch {
        // Collapse still applies for this session.
      }
      return next;
    });
  }

  function toggleLibrary() {
    libraryToggleRequestedRef.current = true;
    setLibraryCollapsed((current) => {
      const next = !current;
      try {
        window.localStorage.setItem(libraryCollapsedKey, String(next));
      } catch {
        // Collapse still applies for this session.
      }
      return next;
    });
  }

  function setThemeChoice(next: Theme) {
    document.documentElement.dataset.theme = next;
    window.localStorage.setItem("contentflow-theme", next);
    setTheme(next);
  }

  function toggleTheme() {
    const current = document.documentElement.dataset.theme === "light" ? "light" : "dark";
    const next = current === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    window.localStorage.setItem("contentflow-theme", next);
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
      if (pendingSessionCreate) void createItem(pendingSessionCreate);
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

  function updateSelected(change: (current: ContentDetail) => ContentDetail) {
    if (!selected || pendingLifecycle?.id === selected.id) return;
    const next = change(selected);
    setSelected(next);
    setAllSummaries((items) => items.map((item) => item.id === next.id ? { ...item, working_title: next.working_title, status: next.status, updated_at: new Date().toISOString() } : item));
    setSummaries((items) => items.map((item) => item.id === next.id ? { ...item, working_title: next.working_title, status: next.status, updated_at: new Date().toISOString() } : item));
    autosaveManager?.enqueue(next);
  }

  function updateContent(content: ContentPayload) {
    updateSelected((current) => ({ ...current, content }));
  }

  function updateYouTubeField<K extends keyof YouTubeContent>(field: K, value: YouTubeContent[K]) {
    if (!selected || selected.type !== "youtube") return;
    updateContent({ ...(selected.content as YouTubeContent), [field]: value });
  }

  function updateYouTubeSections(sections: Section[]) {
    updateYouTubeField("sections", sections.map((section, position) => ({ ...section, position })));
  }

  function editYouTubeSection(clientKey: string, patch: Partial<Pick<Section, "title" | "body">>) {
    if (!selected || selected.type !== "youtube") return;
    const content = selected.content as YouTubeContent;
    updateYouTubeSections(content.sections.map((section) => section.clientKey === clientKey ? { ...section, ...patch } : section));
  }

  function moveYouTubeSection(index: number, direction: -1 | 1) {
    if (!selected || selected.type !== "youtube") return;
    const sections = [...(selected.content as YouTubeContent).sections];
    const target = index + direction;
    if (target < 0 || target >= sections.length) return;
    [sections[index], sections[target]] = [sections[target], sections[index]];
    updateYouTubeSections(sections);
  }

  function removeYouTubeSection(clientKey: string) {
    if (!selected || selected.type !== "youtube") return;
    updateYouTubeSections((selected.content as YouTubeContent).sections.filter((section) => section.clientKey !== clientKey));
  }

  async function createItem(type: ContentType) {
    if (csrfToken === undefined || createPendingRef.current) return;
    lifecycleSynchronizationRef.current += 1;
    createPendingRef.current = true;
    setCreatePending(true);
    setActionError("");
    const operationId = createOperationIdsRef.current.get(type) ?? newOperationId();
    createOperationIdsRef.current.set(type, operationId);
    const mutationSessionGeneration = sessionGenerationRef.current;
    let retryAfterStaleSession = false;
    try {
      const result = await createContent(type, csrfTokenRef.current, operationId);
      requestSequence.current += 1;
      setActionError("");
      createOperationIdsRef.current.delete(type);
      // Stay in the section the item was created from; clearing to "all" would
      // bounce the writer out of the type they deliberately filtered to.
      const retainedType = typeFilter === type ? type : "all";
      const clearedFilters: LibraryFilters = { query: "", type: retainedType, status: "all" };
      activeFiltersRef.current = clearedFilters;
      setQuery("");
      setTypeFilter(retainedType);
      setStatusFilter("all");
      setSelectedId(result.item_ids[0]);
      setCreateOpen(false);
      setLibraryOpen(false);
      const refresh = refreshLibraryRef.current(clearedFilters);
      const refreshSequence = requestSequence.current;
      try {
        await refresh;
      } catch (error) {
        if (refreshSequence !== requestSequence.current) return;
        if (isSessionRecoveryError(error)) {
          setSessionExpired(true);
        } else {
          setActionError("The item was created, but the library could not be refreshed.");
        }
      }
    } catch (error) {
      if (isSessionRecoveryError(error)) {
        if (mutationSessionGeneration !== sessionGenerationRef.current) retryAfterStaleSession = true;
        else {
          setPendingSessionCreate(type);
          setSessionExpired(true);
        }
      }
      if (error instanceof ApiError && error.status < 500 && !isSessionRecoveryError(error)) createOperationIdsRef.current.delete(type);
      if (!isSessionRecoveryError(error)) setActionError("The new item could not be created.");
    } finally {
      createPendingRef.current = false;
      setCreatePending(false);
    }
    if (retryAfterStaleSession) void createItem(type);
  }

  // A type section is already an answer to "what are you creating?", so skip the
  // picker there and only ask when the writer is in All content.
  // Scheduling is a plain replacement of the whole item, so it goes straight to
  // the API rather than through the selected-document autosave queue.
  async function rescheduleItem(id: string, day: string | undefined) {
    if (csrfToken === undefined) return;
    setCalendarError("");
    try {
      const detail = await getContent(id);
      const scheduled = day ? new Date(`${day}T09:00:00`).toISOString() : undefined;
      const body = serializeReplacement({ ...detail, scheduled_at: scheduled }, newOperationId());
      await replaceContent(id, body, csrfTokenRef.current);
      requestSequence.current += 1;
      await refreshLibraryRef.current();
    } catch (error) {
      if (isSessionRecoveryError(error)) setSessionExpired(true);
      else setCalendarError("That item could not be rescheduled. Try again.");
    }
  }

  function startCreate() {
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
          const activeFilters = activeFiltersRef.current;
          const immediateReplacementId = !activeFilters.query && activeFilters.type === "all" && activeFilters.status === "all" ? visibleRemaining[0]?.id : undefined;
          selectedIdRef.current = immediateReplacementId ?? "";
          setSelectedId(immediateReplacementId ?? "");
          if (!immediateReplacementId) {
            pendingDeletedSelectionRef.current = true;
            setSelected(undefined);
            setConflict(undefined);
          }
        }
        pendingLifecycleRef.current = undefined;
        setPendingLifecycle(undefined);
        const refresh = refreshLibraryRef.current();
        const refreshSequence = requestSequence.current;
        try {
          await refresh;
          if (refreshSequence !== requestSequence.current || lifecycleSynchronizationRef.current !== synchronizationGeneration) return;
          if (deletedSelection && !selectedIdRef.current) {
            const replacementId = summariesRef.current[0]?.id;
            if (replacementId) {
              selectedIdRef.current = replacementId;
              setSelectedId(replacementId);
            }
          }
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
        if (selectedIdRef.current === document.id && lifecycleSynchronizationRef.current === synchronizationGeneration) setActionError("The item could not be deleted.");
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

  function captureAttachments(files: FileList | null, kind: Attachment["kind"], append = false) {
    if (!selected) return;
    const captured = Array.from(files ?? []).map((file) => ({ id: uniqueId("asset"), kind, name: file.name, size: file.size }));
    if (!captured.length) return;
    setAttachments((current) => {
      const existing = append ? (current[selected.id] ?? []).filter((item) => item.kind === kind) : [];
      return { ...current, [selected.id]: [...existing, ...captured] };
    });
  }

  function replaceThumbnailPreview(file: File) {
    if (!selected) return;
    const contentId = selected.id;
    const requestId = uniqueId("thumbnail");
    const reader = new FileReader();
    setThumbnailPreviews((current) => ({ ...current, [contentId]: { requestId } }));
    reader.addEventListener("load", () => {
      if (typeof reader.result !== "string") return;
      setThumbnailPreviews((current) => current[contentId]?.requestId === requestId ? { ...current, [contentId]: { requestId, dataUrl: reader.result as string } } : current);
    }, { once: true });
    reader.readAsDataURL(file);
  }

  function renderWorkingTitle(label = "Working title") {
    if (!selected) return null;
    return (
      <label className="document-field document-field-large">
        <span>{label}</span>
        <input aria-label="Working title" value={selected.working_title} placeholder="Untitled video" onChange={(event) => updateSelected((current) => ({ ...current, working_title: event.target.value }))} />
      </label>
    );
  }

  function renderAssetPanel() {
    if (!selected) return null;
    type Action = { kind: Attachment["kind"]; label: string; accept: string; ariaLabel: string; multiple?: boolean };
    let title = "";
    let description = "";
    let actions: Action[] = [];
    if (selected.type === "youtube") {
      title = "Finished video"; description = "Keep the final YouTube upload beside its brief and script.";
      actions = [{ kind: "video", label: "Upload video", accept: "video/*", ariaLabel: "Choose YouTube video" }];
    } else if (selected.type === "instagram") {
      title = "Instagram media"; description = "Add one image, a finished Reel, or several Canva slides.";
      actions = [{ kind: "image", label: "Add images", accept: "image/*", ariaLabel: "Choose Instagram images", multiple: true }, { kind: "video", label: "Add Reel", accept: "video/*", ariaLabel: "Choose Instagram video" }];
    } else if (selected.type === "tiktok") {
      title = "Finished video"; description = "Keep the final TikTok video beside its script.";
      actions = [{ kind: "video", label: "Upload video", accept: "video/*", ariaLabel: "Choose TikTok video" }];
    } else if (selected.type === "linkedin") {
      title = "Post media"; description = "Add an image or upload a Canva carousel as a PDF.";
      actions = [{ kind: "image", label: "Add image", accept: "image/*", ariaLabel: "Choose LinkedIn image" }, { kind: "pdf", label: "Add PDF", accept: "application/pdf", ariaLabel: "Choose LinkedIn PDF" }];
    } else if (selected.type === "x") {
      title = "Post image"; description = "Add one image to publish with the post.";
      actions = [{ kind: "image", label: "Add image", accept: "image/*", ariaLabel: "Choose X image" }];
    } else return null;
    const files = attachments[selected.id] ?? [];
    const slides = selected.type === "instagram" ? files.filter((file) => file.kind === "image").length : 0;
    return (
      <section className="asset-panel" aria-label={`${typeMeta[selected.type].label} assets`}>
        <div className="asset-panel-header">
          <span className="asset-panel-icon"><Paperclip size={18} /></span>
          <div className="asset-panel-copy"><strong>{title}</strong><small>{description}</small></div>
          <div className="asset-actions">
            {actions.map((action) => {
              const inputId = `${selected.id}-${action.kind}-upload`;
              return <span className="asset-action" key={action.kind}>
                <input className="visually-hidden" id={inputId} type="file" accept={action.accept} multiple={action.multiple} aria-label={action.ariaLabel} onChange={(event) => { captureAttachments(event.currentTarget.files, action.kind, Boolean(action.multiple)); event.currentTarget.value = ""; }} />
                <label className="asset-upload-button" htmlFor={inputId}><Upload size={14} /> {action.label}</label>
              </span>;
            })}
          </div>
        </div>
        {files.length > 0 && <div className="asset-list">
          {slides > 1 && <div className="asset-summary"><ImagePlus size={14} /> Carousel · {slides} slides</div>}
          {files.map((file, index) => <div className="asset-file" key={file.id}>
            <span className="asset-file-icon"><AttachmentIcon kind={file.kind} /></span>
            <span className="asset-file-copy"><strong>{file.name}</strong><small>{file.kind === "pdf" ? "PDF" : file.kind === "video" ? "Video" : "Image"}{slides > 1 && file.kind === "image" ? ` · Slide ${index + 1}` : ""}{` · ${formatFileSize(file.size)}`}</small></span>
            <button aria-label={`Remove ${file.name}`} onClick={() => setAttachments((current) => ({ ...current, [selected.id]: (current[selected.id] ?? []).filter((item) => item.id !== file.id) }))}><X size={14} /></button>
          </div>)}
        </div>}
        <p className="prototype-note"><Paperclip size={12} /> Mock upload. Files are not persisted and reset on reload.</p>
      </section>
    );
  }

  // These callbacks only run from user events. The refs rule cannot trace that
  // through this nested renderer without treating them as render-time reads.
  function renderYouTubeEditor() {
    if (!selected || selected.type !== "youtube") return null;
    const content = selected.content as YouTubeContent;
    const thumbnail = thumbnailPreviews[selected.id]?.dataUrl;
    const thumbnailInput = `thumbnail-${selected.id}`;
    return <div className="youtube-editor">
      <details className="planning-card" open>
        <summary><span className="planning-summary-icon"><Target size={17} /></span><span className="planning-summary-copy"><strong>Video brief</strong><small>Decide why this video should exist before writing it.</small></span><ChevronDown className="summary-chevron" size={17} /></summary>
        <div className="planning-content">
          <div className="planning-section-heading"><span>Strategy</span><small>Shape the idea before you shape the script.</small></div>
          <div className="brief-grid">
            <label className="brief-field"><span><Lightbulb size={14} /> Topic</span><AutoTextarea aria-label="YouTube topic" minRows={2} value={content.topic} onChange={(event) => updateYouTubeField("topic", event.target.value)} /></label>
            <label className="brief-field"><span><Users size={14} /> ICP</span><AutoTextarea aria-label="YouTube ICP" minRows={2} value={content.icp} onChange={(event) => updateYouTubeField("icp", event.target.value)} /></label>
            <label className="brief-field brief-field-wide"><span>Unique angle</span><AutoTextarea aria-label="YouTube angle" minRows={2} value={content.angle} onChange={(event) => updateYouTubeField("angle", event.target.value)} /></label>
            <label className="brief-field brief-field-wide"><span><MousePointerClick size={14} /> CTA</span><input aria-label="YouTube CTA" value={content.cta} onChange={(event) => updateYouTubeField("cta", event.target.value)} /></label>
          </div>
          <div className="planning-section-heading publishing-heading"><span>Publishing details</span><small>Keep publishing copy separate from the working title.</small></div>
          <div className="publishing-grid">
            <div className="publishing-fields">
              <label className="brief-field"><span>YouTube title</span><input aria-label="YouTube title" value={content.publishing_title} onChange={(event) => updateYouTubeField("publishing_title", event.target.value)} /></label>
              <label className="brief-field"><span>Description</span><AutoTextarea aria-label="YouTube description" minRows={3} value={content.description} onChange={(event) => updateYouTubeField("description", event.target.value)} /></label>
            </div>
            <section className="youtube-preview-field" aria-label="YouTube video preview">
              <span className="attachment-label">Video preview</span>
              <div className="youtube-preview-card">
                <input className="visually-hidden" id={thumbnailInput} type="file" accept="image/*" aria-label="Choose YouTube thumbnail" onChange={(event) => { const file = event.target.files?.[0]; if (file) replaceThumbnailPreview(file); event.currentTarget.value = ""; }} />
                <label className={`youtube-preview-thumbnail ${thumbnail ? "has-preview" : ""}`} htmlFor={thumbnailInput} style={thumbnail ? { backgroundImage: `url("${thumbnail}")` } : undefined}>
                  {!thumbnail && <span className="youtube-thumbnail-placeholder" aria-hidden="true"><small>Content system</small><strong>One idea.<br />Every format.</strong><span>CF</span></span>}
                  <span className="youtube-thumbnail-action"><Upload size={14} /> {thumbnail ? "Replace thumbnail" : "Add thumbnail"}</span>
                </label>
                <div className="youtube-preview-details"><span className="youtube-channel-avatar">O</span><div><h3 aria-label="YouTube preview title">{content.publishing_title || "Your video title will appear here"}</h3><small>Owain Lewis <span>·</span> Draft</small></div><MoreHorizontal size={17} /></div>
              </div>
              {thumbnail && <button className="thumbnail-remove" aria-label="Remove YouTube thumbnail" onClick={() => setThumbnailPreviews((current) => { const next = { ...current }; delete next[selected.id]; return next; })}><X size={13} /> Remove thumbnail</button>}
            </section>
          </div>
        </div>
      </details>
      {renderAssetPanel()}
      <section className="transcript-card" aria-labelledby="transcript-heading">
        <div className="transcript-heading"><div><p className="eyebrow">Recording transcript</p><h2 id="transcript-heading">What was actually said</h2></div><span>Separate from the planned script</span></div>
        <p>Paste the spoken words here after recording. Editing this transcript never changes the script sections below.</p>
        <AutoTextarea aria-label="YouTube transcript: what was actually said" value={content.transcript} minRows={6} placeholder="Paste the words that were actually spoken…" onChange={(event) => updateYouTubeField("transcript", event.target.value)} />
      </section>
      <div className="section-intro"><div><p className="eyebrow">Planned script</p><h2>Build the story, one block at a time</h2></div><span>{content.sections.length} sections</span></div>
      {content.sections.map((section, index) => <section className="script-block" key={section.clientKey}>
        <div className="block-rail"><span>{String(index + 1).padStart(2, "0")}</span><span className="rail-line" /></div>
        <div className="block-content">
          <div className="block-topline"><input aria-label={`Section ${index + 1} name`} value={section.title} onChange={(event) => editYouTubeSection(section.clientKey, { title: event.target.value })} /><span className="section-actions"><button aria-label={`Move ${section.title || `section ${index + 1}`} up`} disabled={index === 0} onClick={() => moveYouTubeSection(index, -1)}><ArrowUp size={15} /></button><button aria-label={`Move ${section.title || `section ${index + 1}`} down`} disabled={index === content.sections.length - 1} onClick={() => moveYouTubeSection(index, 1)}><ArrowDown size={15} /></button><button aria-label={`Remove ${section.title || `section ${index + 1}`}`} onClick={() => removeYouTubeSection(section.clientKey)}><Trash2 size={15} /></button></span></div>
          <AutoTextarea aria-label={`${section.title || `Section ${index + 1}`} script`} value={section.body} minRows={3} onChange={(event) => editYouTubeSection(section.clientKey, { body: event.target.value })} />
          <span className="block-count">{wordCount(section.body)} words</span>
        </div>
      </section>)}
      <button className="add-block-button" onClick={() => updateYouTubeSections([...content.sections, { clientKey: newClientKey(), position: content.sections.length, title: `Section ${content.sections.length + 1}`, body: "" }])}><Plus size={17} /> Add section</button>
    </div>;
  }

  function renderPlainEditor() {
    if (!selected || selected.type === "youtube") return null;
    const label = selected.type === "email" ? "Email body" : selected.type === "substack" ? "Article body" : selected.type === "instagram" || selected.type === "tiktok" ? `${typeMeta[selected.type].label} script` : `${typeMeta[selected.type].label} post`;
    const content = selected.content;
    const body = "body" in content ? content.body : "script" in content ? content.script : "";
    const setBody = (value: string) => updateContent("body" in content ? { ...content, body: value } : { ...content, script: value });
    return <div className="plain-editor-wrap">
      {selected.type === "email" && "subject" in content && <label className="brief-field standalone-field"><span>Email subject</span><input aria-label="Email subject" value={content.subject} onChange={(event) => updateContent({ ...content, subject: event.target.value })} /></label>}
      {selected.type === "substack" && "headline" in content && <div className="publication-fields"><label className="brief-field"><span>Headline</span><input aria-label="Substack headline" value={content.headline} onChange={(event) => updateContent({ ...content, headline: event.target.value })} /></label><label className="brief-field"><span>Sub-headline</span><input aria-label="Substack sub-headline" value={content.subheadline} onChange={(event) => updateContent({ ...content, subheadline: event.target.value })} /></label></div>}
      <div className="plain-editor-label"><SquarePen size={16} /> {label}</div>
      <textarea className="plain-editor" aria-label={label} value={body} placeholder={selected.type === "email" ? "Write the email…" : selected.type === "substack" ? "Start the article…" : "Write here…"} onChange={(event) => setBody(event.target.value)} />
      {renderAssetPanel()}
    </div>;
  }

  if (authState === "loading") return <main className="centered-state"><LoaderCircle className="spin" /><h1>Opening ContentFlow</h1><p>Loading your workspace…</p></main>;
  if (authState === "signed-out") return <main className="centered-state sign-in-state">
    <div className="brand-mark"><Zap size={20} fill="currentColor" /></div>
    <h1>ContentFlow</h1>
    <p>Sign in to open your content workspace.</p>
    <form className="sign-in-form" onSubmit={(event) => void submitPasswordSignIn(event)}>
      <label>Email<input type="email" autoComplete="username" required value={signInEmail} onChange={(event) => setSignInEmail(event.target.value)} /></label>
      <label>Password<input type="password" autoComplete="current-password" required value={signInPassword} onChange={(event) => setSignInPassword(event.target.value)} /></label>
      {signInError && <p className="inline-error" role="alert">{signInError}</p>}
      <button className="primary-button" type="submit" disabled={signInPending}>{signInPending ? "Signing in…" : "Sign in"}</button>
    </form>
    <div className="sign-in-divider"><span>or</span></div>
    <a className="secondary-button" href="/api/v1/auth/login">Sign in with Google</a>
  </main>;
  if (authState === "error") return <main className="centered-state"><AlertTriangle /><h1>ContentFlow is unavailable</h1><p>{loadError}</p><button className="primary-button" onClick={() => window.location.reload()}>Try again</button></main>;
  if (sessionExpired) return <main className="centered-state"><AlertTriangle /><h1>Your session expired</h1><p>{pendingSessionLifecycle ? `Your ${pendingSessionLifecycle.action} action for “${displayTitle(pendingSessionLifecycle.document)}” is waiting. Sign in in a new tab, then return here to retry it.` : "Your unsaved changes are still queued. Sign in in a new tab, then return here to continue saving."}</p><a className="primary-button" href="/api/v1/auth/login" target="_blank" rel="noreferrer">Open sign in</a><button className="secondary-button" disabled={reauthChecking} onClick={() => void resumeExpiredSession()}>{reauthChecking ? "Checking…" : "I’ve signed in"}</button>{reauthError && <p className="inline-error" role="alert">{reauthError}</p>}</main>;

  const createLabel = typeFilter === "all" ? "New content" : `New ${typeMeta[typeFilter].label}`;
  const currentSaveState = selected ? saveStates[selected.id] ?? "saved" : "saved";
  const selectedPendingLifecycle = selected && pendingLifecycle?.id === selected.id ? pendingLifecycle : undefined;
  const foreignPendingLifecycle = selected && pendingLifecycle && pendingLifecycle.id !== selected.id && autosaveManager?.getConflict(pendingLifecycle.id) ? pendingLifecycle : undefined;
  const foreignPendingLifecycleItem = foreignPendingLifecycle ? allSummaries.find((item) => item.id === foreignPendingLifecycle.id) : undefined;
  const foreignPendingLifecycleTitle = foreignPendingLifecycleItem ? displayTitle(foreignPendingLifecycleItem) : undefined;
  const selectedExpiry = selected ? expiry(selected.expires_at) : undefined;
  const lifecycleDisabled = currentSaveState !== "saved" || Boolean(pendingLifecycle) || Boolean(selectedExpiry?.expired);

  return <main className={`app-shell ${sidebarCollapsed ? "sidebar-is-collapsed" : ""} ${libraryCollapsed ? "library-is-collapsed" : ""}`}>
    <aside className="sidebar" aria-label="Main navigation">
      <div className="brand-row"><div className="brand-mark"><Zap size={17} fill="currentColor" /></div><span className="brand-name">ContentFlow</span><button className="icon-button sidebar-collapse" onClick={toggleSidebar} aria-label={sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"} aria-expanded={!sidebarCollapsed}>{sidebarCollapsed ? <PanelLeftOpen size={17} /> : <PanelLeftClose size={17} />}</button></div>
      <button className="new-content-button" onClick={() => setCreateOpen(true)} title={sidebarCollapsed ? "New content" : undefined}><Plus size={18} /><span>New content</span><span className="key-hint">N</span></button>
      <nav className="primary-nav"><button className={`nav-item ${view === "workspace" && typeFilter === "all" ? "active" : ""}`} onClick={() => { setTypeFilter("all"); navigate("workspace"); }} title={sidebarCollapsed ? "All content" : undefined}><Inbox size={18} /><span>All content</span><span className="nav-count">{allSummaries.length}</span></button><button className={`nav-item ${view === "calendar" ? "active" : ""}`} onClick={() => navigate("calendar")} title={sidebarCollapsed ? "Calendar" : undefined}><CalendarDays size={18} /><span>Calendar</span></button></nav>
      <div className="nav-section"><p className="nav-label">Content types</p>{enabledTypes.map((type) => <button key={type} className={`nav-item ${typeFilter === type ? "active" : ""}`} onClick={() => { setTypeFilter(type); navigate("workspace"); }} title={sidebarCollapsed ? typeMeta[type].label : undefined}><span className="nav-type-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} /></span><span>{typeMeta[type].label}</span><span className="nav-count">{counts[type]}</span></button>)}</div>
      <div className="sidebar-bottom"><button className={`nav-item ${view === "settings" ? "active" : ""}`} onClick={() => navigate("settings")} title={sidebarCollapsed ? "Settings" : undefined}><SettingsIcon size={18} /><span>Settings</span></button><div className="profile-row"><div className="avatar">OL</div><div><strong>Owain Lewis</strong><span>Personal workspace</span></div><MoreHorizontal size={17} /></div></div>
    </aside>

    {view === "workspace" && <>
    <section id="content-library" ref={libraryPanelRef} className={`library-panel ${libraryOpen ? "open" : ""}`} aria-label="Content library" aria-hidden={isCompact ? !libraryOpen : libraryCollapsed || undefined} inert={isCompact ? !libraryOpen : libraryCollapsed}>
      <div className="library-header"><button className="icon-button mobile-close" onClick={() => setLibraryOpen(false)} aria-label="Close library"><ArrowLeft size={19} /></button><div><p className="eyebrow">Workspace</p><h1>{typeFilter === "all" ? "All content" : typeMeta[typeFilter].label}</h1></div><button ref={libraryCollapseButtonRef} className="icon-button library-collapse" onClick={toggleLibrary} aria-label="Collapse content library" aria-controls="content-library" aria-expanded="true"><PanelLeftClose size={18} /></button><button className="icon-button compact-new" onClick={startCreate} aria-label={createLabel}><Plus size={19} /></button></div>
      <label className="search-box"><Search size={17} /><input ref={searchRef} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search titles" aria-label="Search content titles" /><span className="key-hint">⌘ K</span></label>
      <div className="filter-row" aria-label="Filter by status"><button className={statusFilter === "all" ? "active" : ""} onClick={() => setStatusFilter("all")}>All</button>{contentStatuses.map((status) => <button key={status} className={statusFilter === status ? "active" : ""} onClick={() => setStatusFilter(status)}>{statusLabels[status]}</button>)}</div>
      {filterError && <div className="inline-error" role="alert">{filterError}</div>}
      <div className="library-summary"><span>{summaries.length} {summaries.length === 1 ? "item" : "items"}</span><span>Last edited <ChevronDown size={14} /></span></div>
      <div className="content-list">
        {summaries.map((item) => { const itemExpiry = expiry(item.expires_at); return <button className={`content-card ${selectedId === item.id ? "selected" : ""}`} aria-current={selectedId === item.id ? "true" : undefined} key={item.id} onClick={() => { setActionError(""); setSelectedId(item.id); setLibraryOpen(false); }}><span className="card-icon" style={{ color: typeMeta[item.type].color }}><TypeIcon type={item.type} size={17} /></span><span className="card-copy"><strong className={item.working_title.trim() ? "" : "card-untitled"}>{displayTitle(item)}</strong><span className="card-meta"><span className={`status-dot ${item.status}`} />{statusLabels[item.status]}<span className="meta-separator">·</span>{formatRelativeTime(item.updated_at)}</span><span className={`expiry-line ${itemExpiry.warning ? "warning" : ""}`}>{itemExpiry.warning ? itemExpiry.summary : `Expires ${itemExpiry.label}`}</span></span><span className="card-arrow">›</span></button>; })}
        {!summaries.length && <div className="empty-state"><Search size={22} /><strong>{allSummaries.length ? "No content found" : "Your workspace is empty"}</strong><span>{allSummaries.length ? "Try another title or clear your filters." : "Create your first piece of content."}</span>{allSummaries.length ? <button onClick={() => { setQuery(""); setStatusFilter("all"); setTypeFilter("all"); }}>Clear filters</button> : <button onClick={startCreate}>{createLabel}</button>}</div>}
      </div>
    </section>

    <section className="editor-panel" aria-label="Content editor" aria-hidden={isCompact && libraryOpen ? true : undefined} inert={isCompact && libraryOpen ? true : undefined}>
      <header className="mobile-app-header"><button ref={mobileLibraryButtonRef} className="icon-button" onClick={() => setLibraryOpen(true)} aria-label="Open content library"><Menu size={20} /></button><div className="brand-mark"><Zap size={15} fill="currentColor" /></div><strong>ContentFlow</strong><button className="icon-button mobile-theme-toggle" onClick={toggleTheme} aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}>{theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}</button><button className="icon-button" onClick={startCreate} aria-label={createLabel}><Plus size={20} /></button></header>
      {selected ? <>
        <div className="editor-toolbar"><div className="editor-context">{libraryCollapsed && !isCompact && <button ref={libraryExpandButtonRef} className="icon-button editor-library-toggle" onClick={toggleLibrary} aria-label="Expand content library" aria-controls="content-library" aria-expanded="false"><PanelLeftOpen size={18} /></button>}<span className="type-pill" style={{ color: typeMeta[selected.type].color }}><TypeIcon type={selected.type} />{typeMeta[selected.type].label}</span><span className="toolbar-divider" /><label className="status-select"><span className={`status-dot ${selected.status}`} /><select aria-label="Content status" value={selected.status} disabled={Boolean(selectedPendingLifecycle) || Boolean(selectedExpiry?.expired)} onChange={(event) => updateSelected((current) => ({ ...current, status: event.target.value as ContentStatus }))}>{contentStatuses.map((status) => <option value={status} key={status}>{statusLabels[status]}</option>)}</select><ChevronDown size={14} /></label></div><div className="editor-actions"><span className={`saved-state ${currentSaveState}`} aria-live="polite">{currentSaveState === "saving" || currentSaveState === "retrying" ? <LoaderCircle className="spin" size={14} /> : currentSaveState === "conflict" || currentSaveState === "error" ? <AlertTriangle size={14} /> : <Check size={14} />}{saveLabel(currentSaveState)}</span></div></div>
        {foreignPendingLifecycle && <div className="inline-error" role="alert">Review the {foreignPendingLifecycle.action} conflict for “{foreignPendingLifecycleTitle ?? "another item"}” before continuing. <button onClick={() => { setActionError(""); setSelectedId(foreignPendingLifecycle.id); setLibraryOpen(false); }}>Review item</button></div>}
        {actionError && <div className="inline-error" role="alert">{actionError}</div>}
        <div className="editor-scroll"><article className="editor-document">
          <div className="document-heading" inert={Boolean(selectedPendingLifecycle) || Boolean(selectedExpiry?.expired)}>{selected.type === "youtube" ? renderWorkingTitle() : <h1 className={`document-identity ${selected.working_title.trim() ? "" : "document-identity-untitled"}`}>{displayTitle(selected)}</h1>}<div className="document-meta"><span><Clock3 size={14} /> Edited {formatRelativeTime(selected.updated_at)}</span><span>{documentWordCount(selected).toLocaleString()} words</span><span className={selectedExpiry?.warning ? "expiry-warning" : ""}><CalendarDays size={14} /> Expires {selectedExpiry?.label}{selectedExpiry?.warning ? ` · ${selectedExpiry.detail}` : ""}</span></div></div>
          {conflict && <section className="conflict-panel" aria-labelledby="conflict-title"><div className="conflict-title"><AlertTriangle size={18} /><div><h2 id="conflict-title">This item changed elsewhere</h2><p>{selectedPendingLifecycle ? `Review the current server version before you retry or cancel ${selectedPendingLifecycle.action}.` : "Compare the saved server version with your unsaved local work. Nothing was overwritten."}</p></div></div><div className="conflict-columns"><div><h3>Server version</h3><pre>{JSON.stringify(editableSnapshot(conflict.server), null, 2)}</pre></div><div><h3>{selectedPendingLifecycle ? "Previous version" : "Your unsaved version"}</h3><pre>{JSON.stringify(editableSnapshot(conflict.local), null, 2)}</pre></div></div><div className="conflict-actions">{selectedPendingLifecycle ? <><button onClick={() => resolveLifecycleConflict(false)}>Cancel action</button><button className="primary-button" onClick={() => resolveLifecycleConflict(true)}>Retry {selectedPendingLifecycle.action}</button></> : <><button onClick={() => resolveSelectedConflict("server")}>Use server version</button><button className="primary-button" onClick={() => resolveSelectedConflict("local")}>Save my version</button></>}</div></section>}
          <div className="editor-content" inert={Boolean(selectedPendingLifecycle) || Boolean(selectedExpiry?.expired)}>{selected.type === "youtube" ? renderYouTubeEditor() : renderPlainEditor()}</div>
        </article></div>
        <footer className="editor-footer"><span>{typeMeta[selected.type].description}</span><button className="delete-button" disabled={lifecycleDisabled} onClick={() => setDeleteOpen(true)}><Trash2 size={15} /> Delete</button></footer>
      </> : <div className="editor-empty">{libraryCollapsed && !isCompact && <button ref={libraryExpandButtonRef} className="icon-button editor-empty-library-toggle" onClick={toggleLibrary} aria-label="Expand content library" aria-controls="content-library" aria-expanded="false"><PanelLeftOpen size={18} /></button>}{detailLoading ? <><LoaderCircle className="spin" /><p>Loading selected content…</p></> : detailError ? <><AlertTriangle /><h1>Could not open this item</h1><p role="alert">{detailError}</p><button className="primary-button" onClick={() => setDetailReload((value) => value + 1)}>Retry loading item</button></> : <><SquarePen size={28} /><h1>{allSummaries.length ? "Choose an item" : "Start writing"}</h1><p>{allSummaries.length ? "Select content from your library." : "Pick a format to create your first piece."}</p>{actionError && <p className="inline-error" role="alert">{actionError}</p>}<button className="primary-button" onClick={startCreate}><Plus size={16} /> {createLabel}</button></>}</div>}
    </section>
    </>}

    {view === "calendar" && <Calendar items={allSummaries} onOpen={(id) => { setSelectedId(id); navigate("workspace"); }} onSchedule={(id, day) => void rescheduleItem(id, day)} error={calendarError} />}

    {view === "settings" && <Settings theme={theme} onThemeChange={setThemeChoice} enabledTypes={enabledTypes} onToggleType={toggleType} counts={counts} workspaceId={workspaceId} />}

    {createOpen && <div className="modal-backdrop"><section ref={createModalRef} className="modal-card" role="dialog" aria-modal="true" aria-labelledby="create-title"><div className="modal-header"><div><p className="eyebrow">New content</p><h2 id="create-title">What are you creating?</h2><p>Choose a format. You can change its status as the work develops.</p></div><button className="icon-button" onClick={() => setCreateOpen(false)} aria-label="Close create dialog"><X size={19} /></button></div><div className="create-grid" aria-busy={createPending}>{enabledTypes.map((type) => <button key={type} disabled={createPending} onClick={() => void createItem(type)}><span className="create-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} size={19} /></span><span><strong>{typeMeta[type].label}</strong><small>{typeMeta[type].description}</small></span><span className="create-arrow">›</span></button>)}</div>{createPending && <p aria-live="polite">Creating…</p>}{actionError && <p className="inline-error" role="alert">{actionError}</p>}</section></div>}
    {deleteOpen && selected && <div className="modal-backdrop"><section ref={deleteModalRef} className="modal-card confirm-card" role="alertdialog" aria-modal="true" aria-labelledby="delete-title" aria-describedby="delete-description"><div className="modal-header"><div><p className="eyebrow">Permanent deletion</p><h2 id="delete-title">Delete “{displayTitle(selected)}”?</h2><p id="delete-description">This removes the item immediately and cannot be undone.</p></div></div><div className="confirm-actions"><button onClick={() => setDeleteOpen(false)}>Cancel</button><button className="danger-button" onClick={() => void confirmDelete()}><Trash2 size={15} /> Delete permanently</button></div></section></div>}
  </main>;
}
