import { Button, Input, Select } from "./ui";
import { CalendarDays, ChevronLeft, ChevronRight, Plus } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type DragEvent as ReactDragEvent } from "react";
import { contentStatuses, newOperationId, type ContentSummary, type ContentType } from "./api";
import { dayKey } from "./Calendar";
import { TypeIcon, displayTitle, statusLabels, typeMeta } from "./content-meta";

type Props = {
  items: ContentSummary[];
  topics?: ContentSummary[];
  enabledTypes: ContentType[];
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

export default function WeeklyMatrix({ items, topics = [], enabledTypes, onOpen, onSchedule, onCreate, createPending = false, createError, completedAttemptId, frozenPlan, blockedIds = new Set(), pendingIds = new Set(), error }: Props) {
  const [weekStart, setWeekStart] = useState(() => mondayOf(frozenPlan ? new Date(`${frozenPlan.day}T12:00:00`) : new Date()));
  const [dragging, setDragging] = useState<string>();
  const [dragOver, setDragOver] = useState<string>();
  const [dragType, setDragType] = useState<ContentType>();
  const [composer, setComposer] = useState<{ cell: string; title: string; attemptId: string }>();
  const draggingType = useRef<ContentType | undefined>(undefined);
  const suppressOpen = useRef(false);
  const composerInput = useRef<HTMLInputElement>(null);
  const days = useMemo(() => datesForWeek(weekStart), [weekStart]);
  const currentWeek = mondayOf(new Date());
  const nextWeek = new Date(currentWeek.getFullYear(), currentWeek.getMonth(), currentWeek.getDate() + 7);
  const today = dayKey(new Date());
  const label = weekLabel(days[0], days[6]);
  const restoredComposer = frozenPlan ? { cell: `${frozenPlan.type}:${frozenPlan.day}`, title: frozenPlan.title, attemptId: frozenPlan.attemptId } : undefined;
  const activeComposer = composer?.attemptId === completedAttemptId ? undefined : composer ?? restoredComposer;
  const composerCellKey = activeComposer?.cell;
  const composerFrozen = activeComposer?.attemptId === frozenPlan?.attemptId;
  const displayedTypes = useMemo(() => frozenPlan && !enabledTypes.includes(frozenPlan.type) ? [...enabledTypes, frozenPlan.type] : enabledTypes, [enabledTypes, frozenPlan]);

  useEffect(() => {
    if (composerCellKey) composerInput.current?.focus();
  }, [composerCellKey]);

  // Arrow keys page through weeks and T returns to today, unless the writer is typing.
  useEffect(() => {
    function shortcut(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.target instanceof Element && event.target.closest('input, textarea, select, [role="dialog"], [role="alertdialog"]')) return;
      const shift = (offset: number) => setWeekStart((current) => new Date(current.getFullYear(), current.getMonth(), current.getDate() + (offset * 7)));
      if (event.key === "ArrowLeft") shift(-1);
      else if (event.key === "ArrowRight") shift(1);
      else if (event.key.toLowerCase() === "t") setWeekStart(mondayOf(new Date()));
      else return;
      event.preventDefault();
    }
    document.addEventListener("keydown", shortcut);
    return () => document.removeEventListener("keydown", shortcut);
  }, []);

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

  const scheduledThisWeek = useMemo(() => {
    const keys = new Set(days.map(dayKey));
    const visibleTypes = new Set(displayedTypes);
    return items.filter((item) => visibleTypes.has(item.type) && item.scheduled_at && keys.has(dayKey(new Date(item.scheduled_at))));
  }, [days, displayedTypes, items]);

  const unscheduled = useMemo(() => {
    const visibleTypes = new Set(displayedTypes);
    return items.filter((item) => visibleTypes.has(item.type) && !item.scheduled_at);
  }, [displayedTypes, items]);

  function moveWeek(offset: number) {
    setWeekStart((current) => new Date(current.getFullYear(), current.getMonth(), current.getDate() + (offset * 7)));
  }

  function endDrag() {
    setDragging(undefined);
    setDragOver(undefined);
    setDragType(undefined);
    draggingType.current = undefined;
  }

  // Without a type and date the drop lands in the tray, which clears the schedule.
  function drop(event: ReactDragEvent, type?: ContentType, date?: Date) {
    const id = event.dataTransfer.getData("text/plain") || dragging;
    endDrag();
    if (!id) return;
    const item = items.find((candidate) => candidate.id === id);
    if (!item) return;
    if (!type || !date) { if (item.scheduled_at) onSchedule(id, undefined); }
    else if (item.type === type) onSchedule(id, dayKey(date));
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
        <Button
          className={`weekly-add ${hasEntries ? "" : "weekly-add-empty"}`}
          disabled={Boolean(frozenPlan)}
          onClick={() => setComposer({ cell: cellKey, title: "", attemptId: newOperationId() })}
          aria-label={`Add ${typeMeta[type].label} for ${fullDate.format(date)}`}
        >
          <Plus size={14} />
          <span>Add</span>
        </Button>
      );
    }
    return (
      <form className="weekly-composer" onSubmit={(event) => { event.preventDefault(); void submitComposer(type, date); }}>
        <Input
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
          <Button type="submit" className="weekly-composer-save" disabled={createPending || !activeComposer.title.trim()}>{createPending ? "Adding…" : composerFrozen ? "Retry" : "Add"}</Button>
          <Button type="button" disabled={composerFrozen} onClick={() => setComposer(undefined)}>Cancel</Button>
        </div>
      </form>
    );
  }

  function card(item: ContentSummary, inTray = false) {
    const scheduleBlocked = blockedIds.has(item.id);
    const schedulePending = pendingIds.has(item.id);
    return (
      <article
        key={item.id}
        className={`weekly-card status-${item.status} ${dragging === item.id ? "dragging" : ""} ${scheduleBlocked ? "schedule-blocked" : ""}`}
        draggable={!scheduleBlocked}
        aria-busy={scheduleBlocked || undefined}
        onDragStart={(event) => {
          if (scheduleBlocked) return;
          event.dataTransfer.setData("text/plain", item.id);
          event.dataTransfer.effectAllowed = "move";
          draggingType.current = item.type;
          suppressOpen.current = true;
          setDragging(item.id);
          setDragType(item.type);
        }}
        onDragEnd={() => {
          endDrag();
          window.setTimeout(() => { suppressOpen.current = false; }, 0);
        }}
      >
        <Button className="weekly-card-open" disabled={schedulePending} onClick={() => { if (!suppressOpen.current) onOpen(item.id); }} aria-label={`Open ${displayTitle(item)}`}>
          <strong>{inTray && <span className="weekly-card-platform" style={{ color: typeMeta[item.type].color }}><TypeIcon type={item.type} size={13} /></span>}{displayTitle(item)}</strong>
          <span className="weekly-card-status"><span className={`status-dot ${item.status}`} />{statusLabels[item.status]}{item.format ? ` · ${item.format.charAt(0).toUpperCase()}${item.format.slice(1)}` : ""}</span>{item.topic_id && <small className="weekly-topic">{topics.find((topic) => topic.id === item.topic_id)?.working_title || "Topic group"}</small>}
        </Button>
        <label className="weekly-card-move" title="Move to another day">
          <CalendarDays size={13} aria-hidden="true" />
          <span className="visually-hidden">Move {displayTitle(item)}</span>
          <Select
            aria-label={`Move ${displayTitle(item)}`}
            disabled={scheduleBlocked}
            value={item.scheduled_at ? dayKey(new Date(item.scheduled_at)) : ""}
            onChange={(event) => onSchedule(item.id, event.target.value || undefined)}
          >
            <option value="">Unscheduled</option>
            {days.map((date) => <option value={dayKey(date)} key={dayKey(date)}>{fullDate.format(date)}</option>)}
          </Select>
        </label>
      </article>
    );
  }

  return (
    <section className="page weekly-page" aria-label="Weekly content matrix">
      <header className="page-header weekly-header">
        <div>

          <h1>Week</h1>
          <p className="weekly-summary"><span>{scheduledThisWeek.length} {scheduledThisWeek.length === 1 ? "piece" : "pieces"} scheduled</span>{contentStatuses.map((status) => {
            const count = scheduledThisWeek.filter((item) => item.status === status).length;
            return count ? <span className="weekly-status-count" key={status}><span className={`status-dot ${status}`} />{count} {statusLabels[status].toLowerCase()}</span> : null;
          })}</p>
        </div>
        <div className="calendar-controls">
          <Button className="icon-button" aria-label="Previous week" onClick={() => moveWeek(-1)}><ChevronLeft size={18} /></Button>
          <strong aria-live="polite">{label}</strong>
          <Button className="icon-button" aria-label="Following week" onClick={() => moveWeek(1)}><ChevronRight size={18} /></Button>
          <div className="week-shortcuts" aria-label="Jump to week"><Button className="secondary-button" aria-pressed={dayKey(weekStart) === dayKey(currentWeek)} onClick={() => setWeekStart(mondayOf(new Date()))}>This week</Button><Button className="secondary-button" aria-pressed={dayKey(weekStart) === dayKey(nextWeek)} onClick={() => { const start = mondayOf(new Date()); setWeekStart(new Date(start.getFullYear(), start.getMonth(), start.getDate() + 7)); }}>Next week</Button></div>
        </div>
      </header>

      {error && <div className="inline-error" role="alert">{error}</div>}
      {createError && <div className="inline-error" role="alert">{createError}</div>}

      <aside
        className={`weekly-tray ${dragOver === "tray" ? "drag-over" : ""}`}
        aria-label="Unscheduled content"
        onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDragOver("tray"); }}
        onDragLeave={() => setDragOver((current) => current === "tray" ? undefined : current)}
        onDrop={(event) => { event.preventDefault(); drop(event); }}
      >
        <div className="weekly-tray-header"><strong>Unscheduled</strong><span>{unscheduled.length}</span></div>
        {unscheduled.length ? <div className="weekly-tray-items">{unscheduled.map((item) => card(item, true))}</div> : <p className="weekly-tray-empty">Nothing waiting. Drag a card here to take it off the schedule.</p>}
      </aside>

      <div className="weekly-scroll" role="region" aria-label={`${label} matrix. Scroll horizontally to see every day.`}>
        <table className="weekly-matrix" aria-label={`Content scheduled for ${label}`}>
          <thead>
            <tr>
              <th scope="col" className="weekly-corner"><CalendarDays size={15} /> Platform</th>
              {days.map((date) => {
                const key = dayKey(date);
                return <th scope="col" key={key} className={key === today ? "today" : ""}><span>{dayName.format(date)}</span><strong>{date.getDate()}</strong></th>;
              })}
            </tr>
          </thead>
          <tbody>
            {displayedTypes.map((type) => {
              const rowCount = days.reduce((count, date) => count + (byCell.get(`${type}:${dayKey(date)}`)?.length ?? 0), 0);
              return (
                <tr key={type} className={dragType === type ? "weekly-row-target" : dragType ? "weekly-row-dim" : undefined}>
                  <th scope="row">
                    <span className="weekly-platform-icon" style={{ color: typeMeta[type].color }}><TypeIcon type={type} size={16} /></span>
                    <span><strong>{typeMeta[type].label}</strong><small>{rowCount} {rowCount === 1 ? "post" : "posts"}</small></span>
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
                        {entries.map((item) => card(item))}
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
      <p className="weekly-help">Drag a card along its platform row or in from Unscheduled. Each card also has a move control for the keyboard. Press ← and → to change week, T for this week.</p>
    </section>
  );
}
