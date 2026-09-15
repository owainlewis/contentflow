import { CalendarDays, ChevronLeft, ChevronRight, Plus, SlidersHorizontal, Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type DragEvent as ReactDragEvent } from "react";
import { newOperationId, type ContentSummary, type ContentType } from "./api";
import { dayKey } from "./Calendar";
import { TypeIcon, displayTitle, statusLabels, typeMeta } from "./content-meta";
import { useWeeklyRhythm } from "./useWeeklyRhythm";
import { weeklyLabels, weeklyTypeOrder, type WeeklyTargets } from "./weekly-rhythm";

type Props = {
  items: ContentSummary[];
  enabledTypes: ContentType[];
  csrfToken: string;
  onSessionExpired: () => void;
  weekStart: Date;
  onWeekChange: (date: Date) => void;
  onOpen: (id: string) => void;
  onSchedule: (id: string, day: string | undefined) => void;
  onCreate: (type: ContentType, day: string, title: string, attemptId: string) => Promise<boolean>;
  createPending?: boolean;
  createError?: string;
  completedAttemptId?: string;
  frozenPlan?: { type: ContentType; day: string; title: string; attemptId: string };
  blockedIds?: ReadonlySet<string>;
  pendingIds?: ReadonlySet<string>;
  error?: string;
};

const dayName = new Intl.DateTimeFormat(undefined, { weekday: "short" });
const fullDate = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });

export function mondayOf(date: Date) {
  const offset = (date.getDay() + 6) % 7;
  return new Date(date.getFullYear(), date.getMonth(), date.getDate() - offset);
}

export function datesForWeek(start: Date) {
  return Array.from({ length: 7 }, (_, index) => new Date(start.getFullYear(), start.getMonth(), start.getDate() + index));
}

function weekLabel(start: Date, end: Date) {
  const starts = start.toLocaleDateString(undefined, { day: "numeric", month: "short", year: start.getFullYear() === end.getFullYear() ? undefined : "numeric" });
  const ends = end.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
  return `${starts} – ${ends}`;
}

export default function WeeklyMatrix({ items, enabledTypes, csrfToken, onSessionExpired, weekStart, onWeekChange, onOpen, onSchedule, onCreate, createPending = false, createError, completedAttemptId, frozenPlan, blockedIds = new Set(), pendingIds = new Set(), error }: Props) {
  const rhythm = useWeeklyRhythm(csrfToken, onSessionExpired);
  const [targetDraft, setTargetDraft] = useState<WeeklyTargets>();
  const [trayQuery, setTrayQuery] = useState("");
  const [showOtherFormats, setShowOtherFormats] = useState(false);
  const [dragging, setDragging] = useState<string>();
  const [dragOver, setDragOver] = useState<string>();
  const [composer, setComposer] = useState<{ cell: string; title: string; attemptId: string }>();
  const draggingType = useRef<ContentType | undefined>(undefined);
  const suppressOpen = useRef(false);
  const composerInput = useRef<HTMLInputElement>(null);
  const days = useMemo(() => datesForWeek(weekStart), [weekStart]);
  const today = dayKey(new Date());
  const label = weekLabel(days[0], days[6]);
  const restoredComposer = frozenPlan ? { cell: `${frozenPlan.type}:${frozenPlan.day}`, title: frozenPlan.title, attemptId: frozenPlan.attemptId } : undefined;
  const activeComposer = composer?.attemptId === completedAttemptId ? undefined : composer ?? restoredComposer;
  const composerCellKey = activeComposer?.cell;
  const composerFrozen = activeComposer?.attemptId === frozenPlan?.attemptId;
  const displayedTypes = weeklyTypeOrder.filter((type) => enabledTypes.includes(type) || frozenPlan?.type === type);

  useEffect(() => {
    if (composerCellKey) composerInput.current?.focus();
  }, [composerCellKey]);

  const byCell = useMemo(() => {
    const result = new Map<string, ContentSummary[]>();
    for (const item of items) {
      if (!item.scheduled_at) continue;
      const key = `${item.type}:${dayKey(new Date(item.scheduled_at))}`;
      const existing = result.get(key);
      if (existing) existing.push(item);
      else result.set(key, [item]);
    }
    return result;
  }, [items]);

  const keys = new Set(days.map(dayKey));
  const scheduled = items.filter((item) => displayedTypes.includes(item.type) && item.scheduled_at && keys.has(dayKey(new Date(item.scheduled_at))));
  const gapCount = displayedTypes.reduce((total, type) => total + Math.max(0, rhythm.targets[type] - scheduled.filter((item) => item.type === type).length), 0);
  const unscheduled = items.filter((item) => !item.scheduled_at && item.status !== "published" && displayedTypes.includes(item.type));
  const trayItems = unscheduled.filter((item) => `${displayTitle(item)} ${typeMeta[item.type].label}`.toLowerCase().includes(trayQuery.toLowerCase()));
  const currentWeek = dayKey(weekStart) === dayKey(mondayOf(new Date()));
  const rowTypes = displayedTypes.filter((type) => showOtherFormats || rhythm.targets[type] > 0 || scheduled.some((item) => item.type === type) || frozenPlan?.type === type || activeComposer?.cell.startsWith(`${type}:`));

  function moveWeek(offset: number) {
    onWeekChange(new Date(weekStart.getFullYear(), weekStart.getMonth(), weekStart.getDate() + (offset * 7)));
    if (!composerFrozen) setComposer(undefined);
  }

  function drop(event: ReactDragEvent, type: ContentType, date: Date) {
    const id = event.dataTransfer.getData("text/plain") || dragging;
    setDragging(undefined);
    setDragOver(undefined);
    draggingType.current = undefined;
    if (!id) return;
    const item = items.find((candidate) => candidate.id === id);
    if (item?.type === type) onSchedule(id, dayKey(date));
  }

  async function submitComposer(type: ContentType, date: Date) {
    if (!activeComposer || createPending) return;
    const title = activeComposer.title.trim();
    if (!title) return;
    if (await onCreate(type, dayKey(date), title, activeComposer.attemptId)) setComposer(undefined);
  }

  function composerCell(type: ContentType, date: Date, cellKey: string, hasEntries: boolean) {
    if (activeComposer?.cell !== cellKey) {
      return (
        <button
          className={`weekly-add ${hasEntries ? "" : "weekly-add-empty"}`}
          disabled={Boolean(frozenPlan)}
          onClick={() => setComposer({ cell: cellKey, title: "", attemptId: newOperationId() })}
          aria-label={`Add ${typeMeta[type].label} for ${fullDate.format(date)}`}
        >
          <Plus size={14} />
          <span>Add</span>
        </button>
      );
    }
    return (
      <form className="weekly-composer" onSubmit={(event) => { event.preventDefault(); void submitComposer(type, date); }}>
        <input
          ref={composerInput}
          aria-label={`New ${typeMeta[type].label} title for ${fullDate.format(date)}`}
          placeholder="Working title"
          required
          value={activeComposer.title}
          disabled={createPending || composerFrozen}
          onChange={(event) => setComposer({ ...activeComposer, title: event.target.value })}
          onKeyDown={(event) => { if (event.key === "Escape" && !composerFrozen) { event.preventDefault(); setComposer(undefined); } }}
        />
        <div className="weekly-composer-actions">
          <button type="submit" className="weekly-composer-save" disabled={createPending || !activeComposer.title.trim()}>{createPending ? "Adding…" : composerFrozen ? "Retry" : "Add"}</button>
          <button type="button" disabled={composerFrozen} onClick={() => setComposer(undefined)}>Cancel</button>
        </div>
      </form>
    );
  }

  function card(item: ContentSummary) {
    const scheduleBlocked = blockedIds.has(item.id);
    const schedulePending = pendingIds.has(item.id);
    return (
      <article
        key={item.id}
        className={`weekly-card ${dragging === item.id ? "dragging" : ""} ${scheduleBlocked ? "schedule-blocked" : ""}`}
        draggable={!scheduleBlocked}
        aria-busy={scheduleBlocked || undefined}
        onDragStart={(event) => {
          if (scheduleBlocked) return;
          event.dataTransfer.setData("text/plain", item.id);
          event.dataTransfer.effectAllowed = "move";
          draggingType.current = item.type;
          suppressOpen.current = true;
          setDragging(item.id);
        }}
        onDragEnd={() => {
          setDragging(undefined);
          setDragOver(undefined);
          draggingType.current = undefined;
          window.setTimeout(() => { suppressOpen.current = false; }, 0);
        }}
      >
        <button className="weekly-card-open" disabled={schedulePending} onClick={() => { if (!suppressOpen.current) onOpen(item.id); }} aria-label={`Open ${displayTitle(item)}`}>
          <strong>{displayTitle(item)}</strong>
          <span className={`weekly-status ${item.status}`}><i className={`status-dot ${item.status}`} />{statusLabels[item.status]}</span>
        </button>
        <label className="weekly-card-move">
          <span className="visually-hidden">Move {displayTitle(item)}</span>
          <select
            aria-label={`Move ${displayTitle(item)}`}
            disabled={scheduleBlocked}
            value={item.scheduled_at ? dayKey(new Date(item.scheduled_at)) : ""}
            onChange={(event) => onSchedule(item.id, event.target.value || undefined)}
          >
            <option value="">Unscheduled</option>
            {days.map((date) => <option value={dayKey(date)} key={dayKey(date)}>{dayName.format(date)} {date.getDate()}</option>)}
          </select>
        </label>
      </article>
    );
  }

  return (
    <section className="page weekly-page" aria-label="Weekly content matrix">
      <header className="page-header weekly-header">
        <div>
          <h1>{currentWeek ? "This week" : "Weekly plan"}</h1>
          <p className="weekly-summary">Plan, write, and track your weekly content.</p>
        </div>
        <div className="calendar-controls">
          <button className="icon-button" aria-label="Previous week" onClick={() => moveWeek(-1)}><ChevronLeft size={18} /></button>
          <strong aria-live="polite">{label}</strong>
          <button className="icon-button" aria-label="Next week" onClick={() => moveWeek(1)}><ChevronRight size={18} /></button>
          {!currentWeek && <button className="secondary-button" onClick={() => { onWeekChange(mondayOf(new Date())); if (!composerFrozen) setComposer(undefined); }}>Jump to this week</button>}
        </div>
      </header>

      <div className="weekly-overview">
        <dl className="weekly-totals" aria-label="Week progress">
          <div><dt>Planned</dt><dd>{scheduled.length}</dd></div>
          <div><dt>Ready</dt><dd>{scheduled.filter((item) => item.status === "ready").length}</dd></div>
          <div><dt>Published</dt><dd>{scheduled.filter((item) => item.status === "published").length}</dd></div>
        </dl>
        <p className="weekly-gap" aria-live="polite">{gapCount ? <><strong>{gapCount}</strong> {gapCount === 1 ? "piece" : "pieces"} still to plan</> : "Your weekly targets are planned"}</p>
        <button className="secondary-button" disabled={!rhythm.loaded || rhythm.pending} aria-expanded={Boolean(targetDraft)} onClick={() => setTargetDraft(targetDraft ? undefined : { ...rhythm.targets })}><SlidersHorizontal size={15} /> Edit rhythm</button>
      </div>
      {rhythm.error && <div className="inline-error" role="alert">{rhythm.error} {!rhythm.loaded && <button onClick={() => { setTargetDraft(undefined); rhythm.retry(); }}>Reload targets</button>}</div>}
      {targetDraft && <form className="rhythm-editor" onSubmit={(event) => { event.preventDefault(); void rhythm.save(targetDraft).then((saved) => { if (saved) setTargetDraft(undefined); }); }}>
        <div className="rhythm-editor-heading"><h2>Your weekly rhythm</h2><p>Set a target for each format. These repeat every week. Zero means no weekly target.</p></div>
        <div className="rhythm-fields">{displayedTypes.map((type) => <label key={type}><span>{weeklyLabels[type] ?? typeMeta[type].label}</span><input aria-label={`${typeMeta[type].label} weekly target`} type="number" min="0" max="35" step="1" required disabled={rhythm.pending || !rhythm.loaded} value={Number.isNaN(targetDraft[type]) ? "" : targetDraft[type]} onChange={(event) => setTargetDraft({ ...targetDraft, [type]: event.target.valueAsNumber })} /></label>)}</div>
        <div className="rhythm-editor-actions"><button type="submit" className="primary-button" disabled={rhythm.pending || !rhythm.loaded}>{rhythm.pending ? "Saving…" : "Save rhythm"}</button><button type="button" className="secondary-button" disabled={rhythm.pending} onClick={() => setTargetDraft(undefined)}>Cancel</button></div>
      </form>}

      {error && <div className="inline-error" role="alert">{error}</div>}
      {createError && <div className="inline-error" role="alert">{createError}</div>}

      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- Keyboard users need to scroll all seven days. */}
      <div className="weekly-scroll" role="region" tabIndex={0} aria-label={`${label} matrix. Scroll horizontally to see every day.`}>
        <table className="weekly-matrix" aria-label={`Content scheduled for ${label}`}>
          <thead>
            <tr>
              <th scope="col" className="weekly-corner"><CalendarDays size={15} /> Weekly rhythm</th>
              {days.map((date) => {
                const key = dayKey(date);
                return <th scope="col" key={key} className={key === today ? "today" : ""}><span>{dayName.format(date)}</span><strong>{date.getDate()}</strong></th>;
              })}
            </tr>
          </thead>
          <tbody>
            {rowTypes.map((type) => {
              const rowCount = days.reduce((count, date) => count + (byCell.get(`${type}:${dayKey(date)}`)?.length ?? 0), 0);
              return (
                <tr key={type}>
                  <th scope="row">
                    <span className="weekly-platform-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} size={16} /></span>
                    <span><strong>{weeklyLabels[type] ?? typeMeta[type].label}</strong><small>{rowCount} planned{rhythm.targets[type] ? ` / ${rhythm.targets[type]} target` : " · no target"}</small><span className="weekly-target-track" aria-hidden="true"><span style={{ width: `${rhythm.targets[type] ? Math.min(100, rowCount / rhythm.targets[type] * 100) : 0}%` }} /></span></span>
                  </th>
                  {days.map((date) => {
                    const dateKey = dayKey(date);
                    const cellKey = `${type}:${dateKey}`;
                    const entries = byCell.get(cellKey) ?? [];
                    return (
                      <td
                        key={cellKey}
                        className={`${dateKey === today ? "today" : ""} ${dragOver === cellKey ? "drag-over" : ""}`}
                        aria-label={`${typeMeta[type].label} on ${fullDate.format(date)}`}
                        onDragOver={(event) => {
                          if (draggingType.current !== type) return;
                          event.preventDefault();
                          event.dataTransfer.dropEffect = "move";
                          setDragOver(cellKey);
                        }}
                        onDragLeave={() => setDragOver((current) => current === cellKey ? undefined : current)}
                        onDrop={(event) => { event.preventDefault(); drop(event, type, date); }}
                      >
                        {entries.map(card)}
                        {composerCell(type, date, cellKey, entries.length > 0)}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {displayedTypes.some((type) => rhythm.targets[type] === 0) && <button className="weekly-formats-toggle" onClick={() => setShowOtherFormats(!showOtherFormats)}>{showOtherFormats ? "Hide formats without a target" : "Show formats without a target"}</button>}
      <p className="weekly-help">Add a working title to plan a piece. Move cards within their row by dragging or choosing a day. Targets count scheduled pieces, including published work.</p>
      <section className="weekly-backlog" aria-labelledby="weekly-backlog-title">
        <div className="weekly-backlog-heading"><div><h2 id="weekly-backlog-title">Ready to plan <span>{unscheduled.length}</span></h2><p>Bring an idea into the week when you have room for it.</p></div><label className="weekly-search"><Search size={16} /><input aria-label="Find an unscheduled idea" placeholder="Find an idea…" value={trayQuery} onChange={(event) => setTrayQuery(event.target.value)} /></label></div>
        {trayItems.length ? <div className="weekly-backlog-items">{trayItems.map((item) => <div className="weekly-backlog-item" key={item.id}><span className="weekly-backlog-type"><TypeIcon type={item.type} size={15} />{weeklyLabels[item.type] ?? typeMeta[item.type].label}</span>{card(item)}</div>)}</div> : <p className="weekly-backlog-empty">{unscheduled.length ? "No ideas match your search." : "Your unscheduled ideas will appear here. You can also start with Add in the week above."}</p>}
      </section>
    </section>
  );
}
