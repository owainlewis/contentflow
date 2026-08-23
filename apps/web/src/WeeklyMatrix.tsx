import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { useMemo, useRef, useState, type DragEvent as ReactDragEvent } from "react";
import type { ContentSummary, ContentType } from "./api";
import { dayKey } from "./Calendar";
import { TypeIcon, displayTitle, statusLabels, typeMeta } from "./content-meta";

type Props = {
  items: ContentSummary[];
  enabledTypes: ContentType[];
  onOpen: (id: string) => void;
  onSchedule: (id: string, day: string | undefined) => void;
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

export default function WeeklyMatrix({ items, enabledTypes, onOpen, onSchedule, blockedIds = new Set(), pendingIds = new Set(), error }: Props) {
  const [weekStart, setWeekStart] = useState(() => mondayOf(new Date()));
  const [dragging, setDragging] = useState<string>();
  const [dragOver, setDragOver] = useState<string>();
  const draggingType = useRef<ContentType>();
  const suppressOpen = useRef(false);
  const days = useMemo(() => datesForWeek(weekStart), [weekStart]);
  const today = dayKey(new Date());
  const label = weekLabel(days[0], days[6]);

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
    const visibleTypes = new Set(enabledTypes);
    return items.filter((item) => visibleTypes.has(item.type) && item.scheduled_at && keys.has(dayKey(new Date(item.scheduled_at)))).length;
  }, [days, enabledTypes, items]);

  function moveWeek(offset: number) {
    setWeekStart((current) => new Date(current.getFullYear(), current.getMonth(), current.getDate() + (offset * 7)));
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
          <span>{statusLabels[item.status]}</span>
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
            {days.map((date) => <option value={dayKey(date)} key={dayKey(date)}>{fullDate.format(date)}</option>)}
          </select>
        </label>
      </article>
    );
  }

  return (
    <section className="page weekly-page" aria-label="Weekly content matrix">
      <header className="page-header weekly-header">
        <div>
          <p className="eyebrow">Content cadence</p>
          <h1>Weekly matrix</h1>
          <p className="weekly-summary">{scheduledThisWeek} {scheduledThisWeek === 1 ? "piece" : "pieces"} scheduled this week</p>
        </div>
        <div className="calendar-controls">
          <button className="icon-button" aria-label="Previous week" onClick={() => moveWeek(-1)}><ChevronLeft size={18} /></button>
          <strong aria-live="polite">{label}</strong>
          <button className="icon-button" aria-label="Next week" onClick={() => moveWeek(1)}><ChevronRight size={18} /></button>
          <button className="secondary-button" onClick={() => setWeekStart(mondayOf(new Date()))}>This week</button>
        </div>
      </header>

      {error && <div className="inline-error" role="alert">{error}</div>}

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
            {enabledTypes.map((type) => {
              const rowCount = days.reduce((count, date) => count + (byCell.get(`${type}:${dayKey(date)}`)?.length ?? 0), 0);
              return (
                <tr key={type}>
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
                        {entries.length ? entries.map(card) : <span className="weekly-empty">Empty</span>}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="weekly-help">Drag a card along its platform row, or use its move control with the keyboard.</p>
    </section>
  );
}
