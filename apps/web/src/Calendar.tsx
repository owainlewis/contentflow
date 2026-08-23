import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { useMemo, useState, type DragEvent as ReactDragEvent } from "react";
import type { ContentSummary } from "./api";
import { TypeIcon, displayTitle, typeMeta } from "./content-meta";

type Props = {
  items: ContentSummary[];
  onOpen: (id: string) => void;
  onSchedule: (id: string, day: string | undefined) => void;
  blockedIds?: ReadonlySet<string>;
  pendingIds?: ReadonlySet<string>;
  error?: string;
};

const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

// Days are compared as local YYYY-MM-DD keys so a scheduled item lands on the
// day the writer picked, not on a UTC day that can be one off.
export function dayKey(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

function monthGrid(month: Date) {
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  // Monday-first: getDay() is 0 for Sunday.
  const lead = (first.getDay() + 6) % 7;
  const start = new Date(first.getFullYear(), first.getMonth(), 1 - lead);
  return Array.from({ length: 42 }, (_, index) => new Date(start.getFullYear(), start.getMonth(), start.getDate() + index));
}

export default function Calendar({ items, onOpen, onSchedule, blockedIds = new Set(), pendingIds = new Set(), error }: Props) {
  const [month, setMonth] = useState(() => {
    const today = new Date();
    return new Date(today.getFullYear(), today.getMonth(), 1);
  });
  const [dragging, setDragging] = useState<string>();
  const [dragOver, setDragOver] = useState<string>();

  const scheduled = useMemo(() => {
    const byDay = new Map<string, ContentSummary[]>();
    for (const item of items) {
      if (!item.scheduled_at) continue;
      const key = dayKey(new Date(item.scheduled_at));
      const existing = byDay.get(key);
      if (existing) existing.push(item);
      else byDay.set(key, [item]);
    }
    return byDay;
  }, [items]);

  const unscheduled = useMemo(() => items.filter((item) => !item.scheduled_at), [items]);
  const days = useMemo(() => monthGrid(month), [month]);
  const todayKey = dayKey(new Date());
  const monthLabel = month.toLocaleDateString(undefined, { month: "long", year: "numeric" });

  // The dragged id travels in the drag payload rather than in React state, so a
  // drop does not depend on a re-render having happened since drag start.
  function drop(event: ReactDragEvent, key: string | undefined) {
    const id = event.dataTransfer.getData("text/plain") || dragging;
    setDragging(undefined);
    setDragOver(undefined);
    if (id) onSchedule(id, key);
  }

  function chip(item: ContentSummary, inTray: boolean) {
    const scheduleBlocked = blockedIds.has(item.id);
    const schedulePending = pendingIds.has(item.id);
    return (
      <button
        key={item.id}
        className={`calendar-chip ${dragging === item.id ? "dragging" : ""} ${scheduleBlocked ? "schedule-blocked" : ""}`}
        draggable={!scheduleBlocked}
        disabled={schedulePending}
        onDragStart={(event) => {
          if (scheduleBlocked) return;
          // Firefox refuses to start a drag unless some data is set.
          event.dataTransfer.setData("text/plain", item.id);
          event.dataTransfer.effectAllowed = "move";
          setDragging(item.id);
        }}
        onDragEnd={() => { setDragging(undefined); setDragOver(undefined); }}
        onClick={() => onOpen(item.id)}
        title={scheduleBlocked ? `${displayTitle(item)} — wait for the current save before moving` : `${displayTitle(item)} — open`}
        aria-label={`${displayTitle(item)}${inTray ? ", unscheduled" : ""}`}
      >
        <span style={{ color: typeMeta[item.type].color }}><TypeIcon type={item.type} size={13} /></span>
        <span className="calendar-chip-title">{displayTitle(item)}</span>
      </button>
    );
  }

  return (
    <section className="page calendar-page" aria-label="Content calendar">
      <header className="page-header calendar-header">
        <div>
          <p className="eyebrow">Workspace</p>
          <h1>Calendar</h1>
        </div>
        <div className="calendar-controls">
          <button className="icon-button" aria-label="Previous month" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}><ChevronLeft size={18} /></button>
          <strong aria-live="polite">{monthLabel}</strong>
          <button className="icon-button" aria-label="Next month" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}><ChevronRight size={18} /></button>
          <button className="secondary-button" onClick={() => { const today = new Date(); setMonth(new Date(today.getFullYear(), today.getMonth(), 1)); }}>Today</button>
        </div>
      </header>

      {error && <div className="inline-error" role="alert">{error}</div>}

      <div className="calendar-body">
        <div className="calendar-grid" role="grid" aria-label={monthLabel}>
          {weekdays.map((day) => <div key={day} className="calendar-weekday" role="columnheader">{day}</div>)}
          {days.map((date) => {
            const key = dayKey(date);
            const outside = date.getMonth() !== month.getMonth();
            const entries = scheduled.get(key) ?? [];
            return (
              <div
                key={key}
                role="gridcell"
                tabIndex={-1}
                className={`calendar-day ${outside ? "outside" : ""} ${key === todayKey ? "today" : ""} ${dragOver === key ? "drag-over" : ""}`}
                onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDragOver(key); }}
                onDragLeave={() => setDragOver((current) => current === key ? undefined : current)}
                onDrop={(event) => { event.preventDefault(); drop(event, key); }}
                aria-label={date.toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" })}
              >
                <span className="calendar-date">{date.getDate()}</span>
                <div className="calendar-day-items">{entries.map((item) => chip(item, false))}</div>
              </div>
            );
          })}
        </div>

        <aside
          className={`calendar-tray ${dragOver === "tray" ? "drag-over" : ""}`}
          aria-label="Unscheduled content"
          onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDragOver("tray"); }}
          onDragLeave={() => setDragOver((current) => current === "tray" ? undefined : current)}
          onDrop={(event) => { event.preventDefault(); drop(event, undefined); }}
        >
          <div className="calendar-tray-header"><CalendarDays size={15} /><strong>Unscheduled</strong><span>{unscheduled.length}</span></div>
          <p className="settings-hint">Drag a piece onto a day to plan it. Drag it back here to unschedule.</p>
          <div className="calendar-tray-items">
            {unscheduled.map((item) => chip(item, true))}
            {!unscheduled.length && <p className="calendar-tray-empty">Everything is scheduled.</p>}
          </div>
        </aside>
      </div>
    </section>
  );
}
