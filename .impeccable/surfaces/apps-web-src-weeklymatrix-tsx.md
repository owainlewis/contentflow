---
version: 1
slug: "apps-web-src-weeklymatrix-tsx"
primary_target: "apps/web/src/WeeklyMatrix.tsx"
related_targets: ["apps/web/src/App.tsx","apps/web/src/styles.css"]
---

# Weekly planning

Mode: Operate. Scope: the weekly planner and navigation into the existing editors.

## Task

Make the week the app's starting point, with YouTube first and a visible publishing rhythm.

## Acceptance criteria

- AC-1: The root URL opens This week; the library, calendar, settings, and existing weekly links remain reachable.
- AC-2: A Monday-to-Sunday matrix displays weekly targets and separate planned, ready, and published counts. Empty target slots remain visible.
- AC-3: Users can create a titled item in a day, open it, move it, and schedule an existing unscheduled item. Content schedules survive reload.
- AC-4: Changing weeks, returning from an editor, and recovering uncertain creates do not lose the selected week or bypass existing mutation locks.
- AC-5: Weekly targets can be edited and survive reload. Missing, invalid, or unavailable saved preferences have an explicit fallback.
- AC-6: Desktop and mobile offer usable controls, readable content, and keyboard access.

## Direction contract

THESIS: Start with this week's commitments and reveal what remains to plan.
OWN-WORLD: Existing Geist UI, neutral light/dark surfaces, restrained violet selection, fine rules, and compact controls.
STORY: Choose a week, spot a gap, schedule an idea, and open the work.
FIRST VIEWPORT: This week and date controls above a compact readiness summary. YouTube leads a seven-day matrix; target counts sit in row headers. An unscheduled tray follows.
FORM: User-pinned weekly matrix, seed 80edbf3d acknowledged. The specified grid takes precedence over alternate compositions.
FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Boundaries

No publishing integrations or media storage are introduced. Existing content and editor contracts remain compatible. Weekly quotas beyond the user's confirmed quantities are editable starting assumptions.
