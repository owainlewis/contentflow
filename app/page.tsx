"use client";

import {
  ArrowLeft,
  CalendarDays,
  Check,
  ChevronDown,
  Clock3,
  FileText,
  FileVideo,
  Film,
  ImagePlus,
  Inbox,
  Lightbulb,
  Mail,
  Menu,
  MoreHorizontal,
  MousePointerClick,
  Network,
  PanelLeftClose,
  Paperclip,
  Plus,
  Search,
  Settings,
  Sparkles,
  SquarePen,
  Target,
  Trash2,
  Upload,
  Users,
  X,
  Video,
  Zap,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

type ContentType = "youtube" | "linkedin" | "x" | "reel" | "email" | "substack";
type ContentStatus = "Idea" | "Draft" | "Ready" | "Published";

type ScriptBlock = {
  id: string;
  label: string;
  text: string;
};

type YouTubeBrief = {
  topic: string;
  icp: string;
  angle: string;
  cta: string;
  description: string;
  thumbnailName?: string;
};

type Attachment = {
  kind: "image" | "video";
  name: string;
};

type ContentItem = {
  id: string;
  type: ContentType;
  title: string;
  body: string;
  status: ContentStatus;
  updated: string;
  words: number;
  blocks?: ScriptBlock[];
  youtube?: YouTubeBrief;
  subject?: string;
  subheadline?: string;
  attachment?: Attachment;
};

const typeMeta: Record<ContentType, { label: string; shortLabel: string; icon: LucideIcon; color: string }> = {
  youtube: { label: "YouTube", shortLabel: "YouTube", icon: Video, color: "#9eaaa0" },
  linkedin: { label: "LinkedIn", shortLabel: "LinkedIn", icon: Network, color: "#9eaaa0" },
  x: { label: "X", shortLabel: "X", icon: X, color: "#9eaaa0" },
  reel: { label: "Short-form reels", shortLabel: "Reels", icon: Film, color: "#9eaaa0" },
  email: { label: "Email", shortLabel: "Email", icon: Mail, color: "#9eaaa0" },
  substack: { label: "Substack", shortLabel: "Substack", icon: FileText, color: "#9eaaa0" },
};

const createDescriptions: Record<ContentType, string> = {
  youtube: "Video brief and script blocks",
  linkedin: "One focused post",
  x: "One short-form post",
  reel: "Script and video asset",
  email: "Subject line and email body",
  substack: "Headline, sub-headline, and article",
};

const initialItems: ContentItem[] = [
  {
    id: "yt-1",
    type: "youtube",
    title: "Build an AI content system that actually saves time",
    body: "",
    status: "Draft",
    updated: "12 min ago",
    words: 1264,
    youtube: {
      topic: "A practical content system for creators using AI",
      icp: "Solo creators and technical educators publishing every week",
      angle: "The bottleneck is not ideas. It is losing the value of each good idea after publishing.",
      cta: "Download the ContentFlow starter workflow",
      description: "A practical walkthrough of a simple source, shape, and distribute system for turning one strong idea into a useful body of content.",
      thumbnailName: "content-system-thumbnail-v3.png",
    },
    blocks: [
      {
        id: "b-1",
        label: "Intro",
        text: "Most creators don’t have a content problem. They have a system problem. Ideas live in notes, scripts live in docs, and the best parts of every video disappear after publishing.",
      },
      {
        id: "b-2",
        label: "The problem",
        text: "In this video, I’ll show you the simple content system I use to capture one useful idea, develop it into a strong video, and turn it into a week of content without starting from zero each time.",
      },
      {
        id: "b-3",
        label: "Core lesson",
        text: "The system has three stages: source, shape, and distribute. Your source is the deepest version of the idea. Shaping adapts the idea to each platform. Distribution gives every piece a clear next step.",
      },
      {
        id: "b-4",
        label: "Outro",
        text: "Start with one source this week. Make it useful, keep the system simple, and let every smaller post point back to the main idea.",
      },
    ],
  },
  {
    id: "li-1",
    type: "linkedin",
    title: "You don’t need more content ideas",
    body: "You don’t need more content ideas.\n\nYou need a better way to reuse the good ones.\n\nThe strongest creators I know don’t start from scratch every morning. They build one useful idea deeply, then shape it for the places their audience already spends time.",
    status: "Ready",
    updated: "1 hr ago",
    words: 94,
  },
  {
    id: "x-1",
    type: "x",
    title: "The source, shape, distribute framework",
    body: "A simple content system:\n\n1. Source one useful idea\n2. Shape it for the platform\n3. Distribute with a clear next step\n\nConsistency gets easier when you stop starting from zero.",
    status: "Draft",
    updated: "Yesterday",
    words: 42,
  },
  {
    id: "em-1",
    type: "email",
    title: "The system behind my content",
    body: "Hey,\n\nI used to think consistency meant finding something new to say every day. That approach is exhausting, and it usually leads to weaker work.\n\nNow I build one useful idea deeply and let the format change, not the idea.",
    status: "Ready",
    updated: "Yesterday",
    words: 328,
    subject: "The system behind my content",
  },
  {
    id: "ss-1",
    type: "substack",
    title: "One idea, six useful pieces of content",
    body: "The goal of a content system is not to publish everywhere. It is to make the best use of every idea worth sharing.\n\nHere is the workflow I use to turn one long-form source into a small, coherent body of work.",
    status: "Idea",
    updated: "2 days ago",
    words: 812,
    subheadline: "A practical workflow for getting more value from every strong idea",
  },
  {
    id: "re-1",
    type: "reel",
    title: "Stop starting from scratch",
    body: "Hook: If content creation feels exhausting, you’re probably starting from scratch too often.\n\nBeat 1: Pick one strong source idea.\nBeat 2: Pull out the sharpest lesson.\nBeat 3: Rebuild it for one platform.\n\nCTA: Save this and use it for your next post.",
    status: "Published",
    updated: "4 days ago",
    words: 76,
    attachment: { kind: "video", name: "stop-starting-from-scratch-v2.mp4" },
  },
  {
    id: "li-2",
    type: "linkedin",
    title: "Why plain text beats a complex editor",
    body: "A writing tool should get out of the way. Plain text is portable, durable, and almost impossible to break. Your ideas deserve more attention than your formatting toolbar.",
    status: "Published",
    updated: "6 days ago",
    words: 118,
  },
];

const statusOptions: ContentStatus[] = ["Idea", "Draft", "Ready", "Published"];

function wordCount(value: string) {
  return value.trim() ? value.trim().split(/\s+/).length : 0;
}

function firstLine(value: string, fallback: string) {
  const line = value.split("\n").find((part) => part.trim())?.trim();
  if (!line) return fallback;
  return line.length > 68 ? `${line.slice(0, 68)}…` : line;
}

function displayTitle(item: ContentItem) {
  if (item.type === "youtube") {
    const hasTitle = item.title && !item.title.startsWith("Untitled");
    return hasTitle ? item.title : item.youtube?.topic || "Untitled YouTube video";
  }
  if (item.type === "email") return item.subject || "Untitled email";
  if (item.type === "substack") return item.title || "Untitled Substack post";
  if (item.type === "linkedin") return firstLine(item.body, "Untitled LinkedIn post");
  if (item.type === "x") return firstLine(item.body, "Untitled X post");
  return firstLine(item.body, "Untitled reel");
}

function uniqueId(prefix: string) {
  return `${prefix}-${crypto.randomUUID()}`;
}

function TypeIcon({ type, size = 16 }: { type: ContentType; size?: number }) {
  const Icon = typeMeta[type].icon;
  return <Icon size={size} strokeWidth={1.9} />;
}

export default function Home() {
  const [items, setItems] = useState(initialItems);
  const [selectedId, setSelectedId] = useState(initialItems[0].id);
  const [typeFilter, setTypeFilter] = useState<ContentType | "all">("all");
  const [statusFilter, setStatusFilter] = useState<ContentStatus | "All">("All");
  const [query, setQuery] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [repurposeOpen, setRepurposeOpen] = useState(false);
  const [selectedOutputs, setSelectedOutputs] = useState<ContentType[]>(["linkedin", "x", "email"]);
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [savePulse, setSavePulse] = useState(false);
  const [isCompact, setIsCompact] = useState(false);
  const createModalRef = useRef<HTMLElement>(null);
  const repurposeModalRef = useRef<HTMLElement>(null);
  const reelVideoInputRef = useRef<HTMLInputElement>(null);

  const selected = items.find((item) => item.id === selectedId) ?? items[0];

  useEffect(() => {
    const modal = createOpen ? createModalRef.current : repurposeOpen ? repurposeModalRef.current : null;
    if (!modal) return;

    const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusableSelector = 'button:not([disabled]), input, textarea, select, [tabindex]:not([tabindex="-1"])';
    const focusable = Array.from(modal.querySelectorAll<HTMLElement>(focusableSelector));
    focusable[0]?.focus();

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        setCreateOpen(false);
        setRepurposeOpen(false);
        return;
      }
      if (event.key !== "Tab" || !focusable.length) return;

      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      previouslyFocused?.focus();
    };
  }, [createOpen, repurposeOpen]);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const media = window.matchMedia("(max-width: 900px)");
    const sync = () => setIsCompact(media.matches);
    sync();
    media.addEventListener("change", sync);
    return () => media.removeEventListener("change", sync);
  }, []);

  const filteredItems = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return items.filter((item) => {
      const matchesType = typeFilter === "all" || item.type === typeFilter;
      const matchesStatus = statusFilter === "All" || item.status === statusFilter;
      const searchableText = [
        displayTitle(item),
        item.title,
        item.subject,
        item.subheadline,
        item.body,
        item.youtube?.topic,
        item.youtube?.icp,
        item.youtube?.angle,
      ].filter(Boolean).join(" ").toLowerCase();
      const matchesQuery = !normalizedQuery || searchableText.includes(normalizedQuery);
      return matchesType && matchesStatus && matchesQuery;
    });
  }, [items, query, statusFilter, typeFilter]);

  const counts = useMemo(() => {
    return items.reduce<Record<ContentType, number>>(
      (all, item) => ({ ...all, [item.type]: all[item.type] + 1 }),
      { youtube: 0, linkedin: 0, x: 0, reel: 0, email: 0, substack: 0 },
    );
  }, [items]);

  function updateSelected(patch: Partial<ContentItem>) {
    setItems((current) =>
      current.map((item) => (item.id === selected.id ? { ...item, ...patch, updated: "Just now" } : item)),
    );
    setSavePulse(true);
    window.setTimeout(() => setSavePulse(false), 900);
  }

  function updateYouTubeBrief(field: keyof YouTubeBrief, value: string) {
    const youtube = selected.youtube ?? { topic: "", icp: "", angle: "", cta: "", description: "" };
    updateSelected({ youtube: { ...youtube, [field]: value } });
  }

  function captureAttachment(file: File | undefined, kind: Attachment["kind"]) {
    if (!file) return;
    updateSelected({ attachment: { kind, name: file.name } });
  }

  function updateBlock(blockId: string, text: string) {
    const blocks = selected.blocks?.map((block) => (block.id === blockId ? { ...block, text } : block)) ?? [];
    const words = blocks.reduce((total, block) => total + wordCount(block.text), 0);
    updateSelected({ blocks, words });
  }

  function addBlock() {
    const nextNumber = (selected.blocks?.length ?? 0) + 1;
    updateSelected({
      blocks: [...(selected.blocks ?? []), { id: uniqueId(`${selected.id}-block`), label: `Section ${nextNumber}`, text: "" }],
    });
  }

  function createItem(type: ContentType) {
    const isYoutube = type === "youtube";
    const item: ContentItem = {
      id: uniqueId(type),
      type,
      title: `Untitled ${typeMeta[type].shortLabel}`,
      body: "",
      status: "Idea",
      updated: "Just now",
      words: 0,
      blocks: isYoutube
        ? [
            { id: uniqueId("intro"), label: "Intro", text: "" },
            { id: uniqueId("section"), label: "Main section", text: "" },
            { id: uniqueId("outro"), label: "Outro", text: "" },
          ]
        : undefined,
      youtube: isYoutube
        ? { topic: "", icp: "", angle: "", cta: "", description: "" }
        : undefined,
      subject: type === "email" ? "" : undefined,
      subheadline: type === "substack" ? "" : undefined,
    };
    setItems((current) => [item, ...current]);
    setSelectedId(item.id);
    setTypeFilter("all");
    setStatusFilter("All");
    setCreateOpen(false);
    setLibraryOpen(false);
  }

  function createRepurposedDrafts() {
    const sourceTitle = displayTitle(selected);
    const sourceText = selected.type === "youtube"
      ? selected.blocks?.map((block) => block.text).filter(Boolean).join("\n\n") ?? ""
      : selected.body;
    const drafts = selectedOutputs.map<ContentItem>((type) => {
      const lead = `Draft repurposed from “${sourceTitle}”.`;
      const blocks: ScriptBlock[] | undefined = type === "youtube"
        ? [
            { id: uniqueId("intro"), label: "Intro", text: lead },
            { id: uniqueId("main"), label: "Main section", text: sourceText },
            { id: uniqueId("outro"), label: "Outro", text: "Bring the lesson together and give the viewer a clear next step." },
          ]
        : undefined;
      const body = type === "youtube"
        ? ""
        : `${lead}\n\n${sourceText}\n\nShape this idea for ${typeMeta[type].label}.`;
      return {
        id: uniqueId(`${type}-repurposed`),
        type,
        title: `${sourceTitle} · ${typeMeta[type].shortLabel}`,
        body,
        status: "Draft",
        updated: "Just now",
        words: blocks ? blocks.reduce((total, block) => total + wordCount(block.text), 0) : wordCount(body),
        blocks,
        youtube: type === "youtube"
          ? {
              topic: sourceTitle,
              icp: "",
              angle: `Adapt the core lesson from ${sourceTitle} into a clear, practical walkthrough.`,
              cta: "",
              description: "",
            }
          : undefined,
        subject: type === "email" ? sourceTitle : undefined,
        subheadline: type === "substack" ? `A practical guide based on ${sourceTitle}` : undefined,
      };
    });
    if (drafts.length) {
      setItems((current) => [...drafts, ...current]);
      setSelectedId(drafts[0].id);
      setTypeFilter("all");
      setStatusFilter("All");
      setQuery("");
    }
    setRepurposeOpen(false);
  }

  function openRepurpose() {
    const preferredOutputs: ContentType[] = ["linkedin", "x", "email", "reel", "substack", "youtube"];
    setSelectedOutputs(preferredOutputs.filter((type) => type !== selected.type).slice(0, 3));
    setRepurposeOpen(true);
  }

  function deleteSelected() {
    if (items.length === 1) return;
    const remaining = items.filter((item) => item.id !== selected.id);
    setItems(remaining);
    setSelectedId(remaining[0].id);
  }

  function renderDocumentHeading() {
    if (selected.type === "youtube") {
      return (
        <div className="document-title-copy">
          <p className="eyebrow">Video workspace</p>
          <h1>{displayTitle(selected)}</h1>
        </div>
      );
    }

    if (selected.type === "email") {
      return (
        <label className="document-field document-field-large">
          <span>Subject line</span>
          <input
            aria-label="Email subject"
            value={selected.subject ?? ""}
            placeholder="Write a subject line…"
            onChange={(event) => updateSelected({ subject: event.target.value, title: event.target.value })}
          />
        </label>
      );
    }

    if (selected.type === "substack") {
      return (
        <div className="publication-heading">
          <label className="document-field document-field-large">
            <span>Headline</span>
            <input
              aria-label="Substack headline"
              value={selected.title}
              placeholder="Write a headline…"
              onChange={(event) => updateSelected({ title: event.target.value })}
            />
          </label>
          <label className="document-field document-field-subtitle">
            <span>Sub-headline</span>
            <input
              aria-label="Substack sub-headline"
              value={selected.subheadline ?? ""}
              placeholder="Add a short promise or summary…"
              onChange={(event) => updateSelected({ subheadline: event.target.value })}
            />
          </label>
        </div>
      );
    }

    const heading = selected.type === "linkedin" ? "LinkedIn post" : selected.type === "x" ? "X post" : "Short-form reel";
    return (
      <div className="document-title-copy compact">
        <p className="eyebrow">{typeMeta[selected.type].label}</p>
        <h1>{heading}</h1>
      </div>
    );
  }

  function renderYouTubeEditor() {
    const youtube = selected.youtube ?? { topic: "", icp: "", angle: "", cta: "", description: "" };
    const youtubeTitle = selected.title.startsWith("Untitled") ? "" : selected.title;
    const completed = [youtube.topic, youtube.icp, youtube.angle, youtube.cta, youtubeTitle, youtube.description, youtube.thumbnailName]
      .filter((value) => value?.trim()).length;
    const thumbnailInputId = `thumbnail-${selected.id}`;

    return (
      <div className="youtube-editor">
        <details className="planning-card" open>
          <summary>
            <span className="planning-summary-icon"><Target size={17} /></span>
            <span className="planning-summary-copy">
              <strong>Video brief</strong>
              <small>Decide why this video should exist before writing it.</small>
            </span>
            <span className="brief-progress">{completed}/7 complete</span>
            <ChevronDown className="summary-chevron" size={17} />
          </summary>

          <div className="planning-content">
            <div className="planning-section-heading">
              <span>Strategy</span>
              <small>Shape the idea before you shape the script.</small>
            </div>
            <div className="brief-grid">
              <label className="brief-field">
                <span><Lightbulb size={14} /> Topic</span>
                <textarea
                  aria-label="YouTube topic"
                  rows={2}
                  value={youtube.topic}
                  placeholder="What is this video really about?"
                  onChange={(event) => updateYouTubeBrief("topic", event.target.value)}
                />
              </label>
              <label className="brief-field">
                <span><Users size={14} /> ICP</span>
                <textarea
                  aria-label="YouTube ICP"
                  rows={2}
                  value={youtube.icp}
                  placeholder="Who is this specifically for?"
                  onChange={(event) => updateYouTubeBrief("icp", event.target.value)}
                />
              </label>
              <label className="brief-field brief-field-wide">
                <span><Sparkles size={14} /> Unique angle</span>
                <textarea
                  aria-label="YouTube angle"
                  rows={2}
                  value={youtube.angle}
                  placeholder="Why would someone choose this video over every other one?"
                  onChange={(event) => updateYouTubeBrief("angle", event.target.value)}
                />
              </label>
              <label className="brief-field brief-field-wide">
                <span><MousePointerClick size={14} /> CTA</span>
                <input
                  aria-label="YouTube CTA"
                  value={youtube.cta}
                  placeholder="What should the viewer do next?"
                  onChange={(event) => updateYouTubeBrief("cta", event.target.value)}
                />
              </label>
            </div>

            <div className="planning-section-heading publishing-heading">
              <span>Publishing details</span>
              <small>Capture these here, even if you generate them after the script.</small>
            </div>
            <div className="publishing-grid">
              <div className="publishing-fields">
                <label className="brief-field">
                  <span>YouTube title</span>
                  <input
                    aria-label="YouTube title"
                    value={youtubeTitle}
                    placeholder="Generate or write the final title…"
                    onChange={(event) => updateSelected({ title: event.target.value })}
                  />
                </label>
                <label className="brief-field">
                  <span>Description</span>
                  <textarea
                    aria-label="YouTube description"
                    rows={4}
                    value={youtube.description}
                    placeholder="Add the final video description…"
                    onChange={(event) => updateYouTubeBrief("description", event.target.value)}
                  />
                </label>
              </div>
              <div className="thumbnail-field">
                <span className="attachment-label">Thumbnail</span>
                <input
                  className="visually-hidden"
                  id={thumbnailInputId}
                  type="file"
                  accept="image/*"
                  aria-label="Choose YouTube thumbnail"
                  onChange={(event) => {
                    const file = event.target.files?.[0];
                    if (file) updateYouTubeBrief("thumbnailName", file.name);
                  }}
                />
                <label className={`attachment-dropzone thumbnail-dropzone ${youtube.thumbnailName ? "has-file" : ""}`} htmlFor={thumbnailInputId}>
                  <span className="attachment-visual"><ImagePlus size={22} /></span>
                  {youtube.thumbnailName ? (
                    <><strong>{youtube.thumbnailName}</strong><small>Choose a different image</small></>
                  ) : (
                    <><strong>Add thumbnail</strong><small>PNG, JPG, or WebP</small></>
                  )}
                </label>
              </div>
            </div>
            <p className="prototype-note"><Paperclip size={12} /> Attachments are held in this session for the UI prototype.</p>
          </div>
        </details>

        <div className="section-intro">
          <div><p className="eyebrow">Script structure</p><h2>Build the story, one block at a time</h2></div>
          <span>{selected.blocks?.length ?? 0} sections</span>
        </div>
        {selected.blocks?.map((block, index) => (
          <section className="script-block" key={block.id}>
            <div className="block-rail"><span>{String(index + 1).padStart(2, "0")}</span><span className="rail-line" /></div>
            <div className="block-content">
              <div className="block-topline">
                <input
                  aria-label={`Section ${index + 1} name`}
                  value={block.label}
                  placeholder="Name this section"
                  onChange={(event) => {
                    const blocks = selected.blocks?.map((current) => current.id === block.id ? { ...current, label: event.target.value } : current);
                    updateSelected({ blocks });
                  }}
                />
                <button aria-label={`More options for ${block.label || `section ${index + 1}`}`}><MoreHorizontal size={17} /></button>
              </div>
              <textarea
                aria-label={`${block.label || `Section ${index + 1}`} script`}
                value={block.text}
                placeholder="Write this part of your script…"
                onChange={(event) => updateBlock(block.id, event.target.value)}
                rows={Math.max(3, Math.ceil(block.text.length / 92))}
              />
              <span className="block-count">{wordCount(block.text)} words</span>
            </div>
          </section>
        ))}
        <button className="add-block-button" onClick={addBlock}><Plus size={17} /> Add section</button>
      </div>
    );
  }

  function renderPlainEditor() {
    const editorLabel = selected.type === "email"
      ? "Email"
      : selected.type === "substack"
        ? "Article body"
        : selected.type === "reel"
          ? "Reel script"
          : `${typeMeta[selected.type].label} post`;
    const videoInputId = `video-${selected.id}`;

    return (
      <div className="plain-editor-wrap">
        {selected.type === "reel" && (
          <section className="media-panel" aria-label="Reel video attachment">
            <div className="media-panel-copy">
              <span className="media-panel-icon"><FileVideo size={19} /></span>
              <div><strong>Video asset</strong><small>Keep the finished reel with its script.</small></div>
            </div>
            <input
              ref={reelVideoInputRef}
              className="visually-hidden"
              id={videoInputId}
              type="file"
              accept="video/*"
              aria-label="Choose reel video"
              onChange={(event) => captureAttachment(event.target.files?.[0], "video")}
            />
            <label className="media-upload-button" htmlFor={videoInputId}>
              <Upload size={15} /> {selected.attachment ? "Replace video" : "Attach video"}
            </label>
            {selected.attachment && (
              <div className="attached-file">
                <FileVideo size={15} />
                <span>{selected.attachment.name}</span>
                <button
                  aria-label="Remove reel video"
                  onClick={() => {
                    if (reelVideoInputRef.current) reelVideoInputRef.current.value = "";
                    updateSelected({ attachment: undefined });
                  }}
                >
                  <X size={14} />
                </button>
              </div>
            )}
          </section>
        )}
        <div className="plain-editor-label"><SquarePen size={16} /> {editorLabel}</div>
        <textarea
          className="plain-editor"
          aria-label={editorLabel}
          value={selected.body}
          placeholder={selected.type === "email" ? "Write the email…" : selected.type === "substack" ? "Start the article…" : "Write the post…"}
          onChange={(event) => updateSelected({ body: event.target.value, words: wordCount(event.target.value) })}
        />
      </div>
    );
  }

  return (
    <main className="app-shell">
      <aside className="sidebar" aria-label="Main navigation">
        <div className="brand-row">
          <div className="brand-mark"><Zap size={17} fill="currentColor" /></div>
          <span className="brand-name">ContentFlow</span>
          <button className="icon-button sidebar-collapse" aria-label="Collapse sidebar"><PanelLeftClose size={17} /></button>
        </div>

        <button className="new-content-button" onClick={() => setCreateOpen(true)}>
          <Plus size={18} />
          <span>New content</span>
          <span className="key-hint">N</span>
        </button>

        <nav className="primary-nav">
          <button className={`nav-item ${typeFilter === "all" ? "active" : ""}`} onClick={() => setTypeFilter("all")}>
            <Inbox size={18} /> <span>All content</span><span className="nav-count">{items.length}</span>
          </button>
          <button className="nav-item"><CalendarDays size={18} /> <span>Calendar</span></button>
        </nav>

        <div className="nav-section">
          <p className="nav-label">Content types</p>
          {(Object.keys(typeMeta) as ContentType[]).map((type) => (
            <button
              key={type}
              className={`nav-item ${typeFilter === type ? "active" : ""}`}
              onClick={() => setTypeFilter(type)}
            >
              <span className="nav-type-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} /></span>
              <span>{typeMeta[type].label}</span>
              <span className="nav-count">{counts[type]}</span>
            </button>
          ))}
        </div>

        <div className="sidebar-bottom">
          <button className="nav-item"><Settings size={18} /> <span>Settings</span></button>
          <div className="profile-row">
            <div className="avatar">OL</div>
            <div><strong>Owain Lewis</strong><span>Personal workspace</span></div>
            <MoreHorizontal size={17} />
          </div>
        </div>
      </aside>

      <section
        className={`library-panel ${libraryOpen ? "open" : ""}`}
        aria-label="Content library"
        aria-hidden={isCompact && !libraryOpen ? true : undefined}
        inert={isCompact && !libraryOpen ? true : undefined}
      >
        <div className="library-header">
          <button className="icon-button mobile-close" onClick={() => setLibraryOpen(false)} aria-label="Close library"><ArrowLeft size={19} /></button>
          <div>
            <p className="eyebrow">Workspace</p>
            <h1>{typeFilter === "all" ? "All content" : typeMeta[typeFilter].label}</h1>
          </div>
          <button className="icon-button compact-new" onClick={() => setCreateOpen(true)} aria-label="Create content"><Plus size={19} /></button>
        </div>

        <label className="search-box">
          <Search size={17} />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search your content" />
          <span className="key-hint">⌘ K</span>
        </label>

        <div className="filter-row" aria-label="Filter by status">
          {(["All", ...statusOptions] as const).map((status) => (
            <button key={status} className={statusFilter === status ? "active" : ""} onClick={() => setStatusFilter(status)}>
              {status}
            </button>
          ))}
        </div>

        <div className="library-summary">
          <span>{filteredItems.length} {filteredItems.length === 1 ? "item" : "items"}</span>
          <button>Last edited <ChevronDown size={14} /></button>
        </div>

        <div className="content-list">
          {filteredItems.map((item) => (
            <button
              className={`content-card ${selected.id === item.id ? "selected" : ""}`}
              aria-current={selected.id === item.id ? "true" : undefined}
              key={item.id}
              onClick={() => { setSelectedId(item.id); setLibraryOpen(false); }}
            >
              <span className="card-icon" style={{ color: typeMeta[item.type].color }}><TypeIcon type={item.type} size={17} /></span>
              <span className="card-copy">
                <strong>{displayTitle(item)}</strong>
                <span className="card-meta">
                  <span className={`status-dot ${item.status.toLowerCase()}`} />
                  {item.status}<span className="meta-separator">·</span>{item.updated}
                </span>
              </span>
              <span className="card-arrow">›</span>
            </button>
          ))}
          {!filteredItems.length && (
            <div className="empty-state">
              <Search size={22} />
              <strong>No content found</strong>
              <span>Try another search or clear your filters.</span>
              <button onClick={() => { setQuery(""); setStatusFilter("All"); }}>Clear filters</button>
            </div>
          )}
        </div>
      </section>

      <section className="editor-panel" aria-label="Content editor">
        <header className="mobile-app-header">
          <button className="icon-button" onClick={() => setLibraryOpen(true)} aria-label="Open content library"><Menu size={20} /></button>
          <div className="brand-mark"><Zap size={15} fill="currentColor" /></div>
          <strong>ContentFlow</strong>
          <button className="icon-button" onClick={() => setCreateOpen(true)} aria-label="Create content"><Plus size={20} /></button>
        </header>

        <div className="editor-toolbar">
          <div className="editor-context">
            <span className="type-pill" style={{ color: typeMeta[selected.type].color }}><TypeIcon type={selected.type} />{typeMeta[selected.type].label}</span>
            <span className="toolbar-divider" />
            <label className="status-select">
              <span className={`status-dot ${selected.status.toLowerCase()}`} />
              <select value={selected.status} onChange={(event) => updateSelected({ status: event.target.value as ContentStatus })}>
                {statusOptions.map((status) => <option key={status}>{status}</option>)}
              </select>
              <ChevronDown size={14} />
            </label>
          </div>
          <div className="editor-actions">
            <span className={`saved-state ${savePulse ? "pulse" : ""}`}><Check size={14} /> Saved</span>
            <button className="repurpose-button" onClick={openRepurpose}><Sparkles size={16} /> Repurpose</button>
            <button className="icon-button" aria-label="More options"><MoreHorizontal size={19} /></button>
          </div>
        </div>

        <div className="editor-scroll">
          <article className="editor-document">
            <div className="document-heading">
              {renderDocumentHeading()}
              <div className="document-meta">
                <span><Clock3 size={14} /> Edited {selected.updated.toLowerCase()}</span>
                <span>{selected.words.toLocaleString()} words</span>
              </div>
            </div>

            {selected.type === "youtube" ? renderYouTubeEditor() : renderPlainEditor()}
          </article>
        </div>

        <footer className="editor-footer">
          <span>{selected.type === "youtube" ? `${selected.blocks?.length ?? 0} script blocks` : createDescriptions[selected.type]}</span>
          <button className="delete-button" onClick={deleteSelected}><Trash2 size={15} /> Delete</button>
        </footer>
      </section>

      {createOpen && (
        <div className="modal-backdrop">
          <section ref={createModalRef} className="modal-card create-modal" role="dialog" aria-modal="true" aria-labelledby="create-title">
            <div className="modal-header">
              <div><p className="eyebrow">Create</p><h2 id="create-title">What are you making?</h2><p>Start with the right shape. You can change it later.</p></div>
              <button className="icon-button" onClick={() => setCreateOpen(false)} aria-label="Close"><X size={19} /></button>
            </div>
            <div className="type-grid">
              {(Object.keys(typeMeta) as ContentType[]).map((type) => (
                <button key={type} onClick={() => createItem(type)}>
                  <span className="type-grid-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} size={20} /></span>
                  <span><strong>{typeMeta[type].label}</strong><small>{createDescriptions[type]}</small></span>
                  <span className="type-arrow">→</span>
                </button>
              ))}
            </div>
          </section>
        </div>
      )}

      {repurposeOpen && (
        <div className="modal-backdrop">
          <section ref={repurposeModalRef} className="modal-card repurpose-modal" role="dialog" aria-modal="true" aria-labelledby="repurpose-title">
            <div className="repurpose-glow" />
            <div className="modal-header">
              <div><p className="eyebrow"><Sparkles size={13} /> Repurpose</p><h2 id="repurpose-title">Turn one idea into more</h2><p>Choose the formats you want to create from this source.</p></div>
              <button className="icon-button" onClick={() => setRepurposeOpen(false)} aria-label="Close"><X size={19} /></button>
            </div>
            <div className="source-card">
              <span style={{ color: typeMeta[selected.type].color }}><TypeIcon type={selected.type} size={18} /></span>
              <div><small>Source content</small><strong>{displayTitle(selected)}</strong></div>
              <Check size={16} />
            </div>
            <p className="output-label">Create drafts for</p>
            <div className="output-list">
              {(Object.keys(typeMeta) as ContentType[]).filter((type) => type !== selected.type).map((type) => {
                const active = selectedOutputs.includes(type);
                return (
                  <button
                    key={type}
                    className={active ? "active" : ""}
                    aria-pressed={active}
                    onClick={() => setSelectedOutputs((current) => active ? current.filter((item) => item !== type) : [...current, type])}
                  >
                    <span style={{ color: typeMeta[type].color }}><TypeIcon type={type} /></span>
                    <strong>{typeMeta[type].label}</strong>
                    <span className="checkbox">{active && <Check size={13} />}</span>
                  </button>
                );
              })}
            </div>
            <div className="modal-footer">
              <span>{selectedOutputs.length} {selectedOutputs.length === 1 ? "draft" : "drafts"} will be added to your library</span>
              <button className="primary-button" disabled={!selectedOutputs.length} onClick={createRepurposedDrafts}><Sparkles size={16} /> Create drafts</button>
            </div>
          </section>
        </div>
      )}
    </main>
  );
}
