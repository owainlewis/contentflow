import { cleanup, fireEvent, render, screen } from "@testing-library/react";
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
