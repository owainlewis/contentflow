import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import WeeklyMatrix from "../apps/web/src/WeeklyMatrix";

afterEach(() => { cleanup(); vi.useRealTimers(); });

it.each([
  [new Date(2026, 8, 30, 12), "2026-09-28", "2026-10-05"],
  [new Date(2026, 11, 31, 12), "2026-12-28", "2027-01-04"],
])("jumps to actual this/next week after browsing away (%s)", (today, currentMonday, nextMonday) => {
  vi.useFakeTimers();
  vi.setSystemTime(today);
  render(<WeeklyMatrix items={[]} enabledTypes={["linkedin"]} onOpen={() => {}} onSchedule={() => {}} onCreate={async () => true} />);
  const dateLabel = (day: string) => new Date(`${day}T12:00:00`).toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
  const thisWeek = screen.getByRole("button", { name: "This week" });
  const nextWeek = screen.getByRole("button", { name: "Next week" });
  expect(thisWeek.getAttribute("aria-pressed")).toBe("true");
  fireEvent.click(screen.getByRole("button", { name: "Previous week" }));
  fireEvent.click(screen.getByRole("button", { name: "Previous week" }));
  fireEvent.click(nextWeek);
  expect(nextWeek.getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByRole("button", { name: `Add LinkedIn for ${dateLabel(nextMonday)}` })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Following week" }));
  expect(nextWeek.getAttribute("aria-pressed")).toBe("false");
  fireEvent.click(nextWeek);
  expect(screen.getByRole("button", { name: `Add LinkedIn for ${dateLabel(nextMonday)}` })).toBeTruthy();
  fireEvent.click(thisWeek);
  expect(thisWeek.getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByRole("button", { name: `Add LinkedIn for ${dateLabel(currentMonday)}` })).toBeTruthy();
});

function summary(id: string, overrides: Record<string, unknown> = {}) {
  return { id, type: "linkedin", working_title: id, status: "idea", revision: 1, created_at: "2026-09-01T09:00:00Z", updated_at: "2026-09-01T09:00:00Z", asset_counts: {}, ...overrides } as never;
}

it("lists undated pieces in the tray and schedules them from there", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(2026, 8, 30, 12));
  const schedule = vi.fn();
  render(<WeeklyMatrix items={[summary("Undated post"), summary("Hidden type", { type: "tiktok" }), summary("Dated post", { status: "ready", scheduled_at: new Date(2026, 8, 29, 9).toISOString() })]} enabledTypes={["linkedin"]} onOpen={() => {}} onSchedule={schedule} onCreate={async () => true} />);
  const tray = screen.getByRole("complementary", { name: "Unscheduled content" });
  expect(within(tray).getByRole("button", { name: "Open Undated post" })).toBeTruthy();
  expect(within(tray).queryByRole("button", { name: "Open Hidden type" })).toBeNull();
  expect(within(tray).queryByRole("button", { name: "Open Dated post" })).toBeNull();
  expect(screen.getByText("1 piece scheduled")).toBeTruthy();
  expect(screen.getByText("1 ready")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Move Undated post"), { target: { value: "2026-10-01" } });
  expect(schedule).toHaveBeenCalledWith("Undated post", "2026-10-01");
});

it("pages weeks with the arrow keys and returns with T, but not while typing", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(2026, 8, 30, 12));
  render(<WeeklyMatrix items={[]} enabledTypes={["linkedin"]} onOpen={() => {}} onSchedule={() => {}} onCreate={async () => true} />);
  const thisWeek = screen.getByRole("button", { name: "This week" });
  fireEvent.keyDown(document.body, { key: "ArrowRight" });
  expect(screen.getByRole("button", { name: "Next week" }).getAttribute("aria-pressed")).toBe("true");
  fireEvent.keyDown(document.body, { key: "t" });
  expect(thisWeek.getAttribute("aria-pressed")).toBe("true");
  fireEvent.click(screen.getAllByRole("button", { name: /^Add LinkedIn for/ })[0]);
  fireEvent.keyDown(screen.getByPlaceholderText("Working title"), { key: "ArrowRight" });
  expect(thisWeek.getAttribute("aria-pressed")).toBe("true");
});
