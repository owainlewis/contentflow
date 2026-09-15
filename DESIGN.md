---
name: ContentFlow
description: A compact workspace for planning and writing content.
colors:
  "accent": "#776ac5"
  "accent-ink": "#ffffff"
  "background": "#16171a"
  "sidebar": "#17181b"
  "panel": "#1b1c20"
  "editor": "#1d1e22"
  "surface": "#202126"
  "surface-strong": "#26272d"
  "control": "#18191d"
  "hover": "#212228"
  "ink": "#e8e8eb"
  "muted": "#9a9aa2"
  "text-strong": "#e8e8eb"
  "text-soft": "#b9b9c0"
  "text-muted": "#8d8d95"
  "placeholder": "#898991"
  "platform-icon": "#8f9098"
  "line": "#2d2e34"
  "line-soft": "#25262b"
  "line-strong": "#45464e"
  "warning": "#d9a454"
  "danger": "#e47d7d"
  "accent-light": "#675bc2"
  "background-light": "#f7f7f8"
  "sidebar-light": "#f7f7f8"
  "panel-light": "#f1f1f3"
  "editor-light": "#fcfcfd"
  "surface-light": "#ffffff"
  "surface-strong-light": "#ebebee"
  "control-light": "#ffffff"
  "hover-light": "#eeeef1"
  "ink-light": "#242429"
  "muted-light": "#68686f"
  "text-strong-light": "#26262b"
  "text-soft-light": "#55555c"
  "text-muted-light": "#696971"
  "placeholder-light": "#73737b"
  "platform-icon-light": "#66666e"
  "line-light": "#dedee3"
  "line-soft-light": "#e9e9ec"
  "line-strong-light": "#b9b9c1"
  "warning-light": "#7a5100"
  "danger-light": "#a02525"
  "status-idea": "#70736e"
  "status-draft": "#918f86"
  "status-published": "#b2b4af"
typography:
  display:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "clamp(30px, 3vw, 44px)"
    fontWeight: 590
    lineHeight: 1.16
    letterSpacing: "-0.038em"
  headline:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "26px"
    fontWeight: 620
    letterSpacing: "-0.03em"
  planner-title:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "30px"
    fontWeight: 620
    letterSpacing: "-0.035em"
  title:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "16px"
    fontWeight: 600
  body:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "1rem"
    lineHeight: 1.618
  ui:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "0.875rem"
  label:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "0.75rem"
  button:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "12.5px"
    fontWeight: 650
  card-title:
    fontFamily: "\"Geist Variable\", Arial, Helvetica, sans-serif"
    fontSize: "12px"
    fontWeight: 550
    lineHeight: 1.45
  mono:
    fontFamily: "\"Geist Mono Variable\", monospace"
    fontSize: "0.75rem"
rounded:
  "4": "4px"
  "5": "5px"
  "6": "6px"
  "7": "7px"
  "8": "8px"
  "9": "9px"
spacing:
  "4": "4px"
  "6": "6px"
  "8": "8px"
  "12": "12px"
  "16": "16px"
  "18": "18px"
  "20": "20px"
  "24": "24px"
  "32": "32px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.accent-ink}"
    typography: "{typography.button}"
    rounded: "{rounded.6}"
    padding: "0 15px"
    height: "38px"
  button-icon:
    backgroundColor: "transparent"
    textColor: "{colors.muted}"
    rounded: "{rounded.9}"
    padding: "0"
    width: "34px"
    height: "34px"
  button-add:
    backgroundColor: "transparent"
    textColor: "{colors.text-muted}"
    typography: "{typography.label}"
    rounded: "{rounded.5}"
    padding: "5px"
  input-search:
    backgroundColor: "{colors.control}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.6}"
    padding: "0 12px"
    height: "42px"
  nav-item:
    backgroundColor: "transparent"
    textColor: "{colors.muted}"
    typography: "{typography.ui}"
    rounded: "{rounded.8}"
    padding: "0 12px"
    height: "42px"
  nav-item-active:
    backgroundColor: "{colors.surface-strong}"
    textColor: "{colors.ink}"
  calendar-chip:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text-soft}"
    typography: "{typography.label}"
    rounded: "{rounded.5}"
    padding: "2px"
  weekly-card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text-strong}"
    rounded: "{rounded.5}"
    padding: "8px"
  weekly-matrix:
    backgroundColor: "{colors.line}"
    rounded: "{rounded.8}"
  rhythm-editor:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.8}"
    padding: "20px"
---

# Design System: ContentFlow

## Overview

**Creative North Star: "The planning workspace"**

ContentFlow uses compact Geist typography, closely spaced neutral surfaces, fine borders, and violet state accents. Planning views keep counts, titles, statuses, and controls visible together; the writing view gives the document more space.

This record describes the implemented UI in apps/web/src/styles.css, WeeklyMatrix.tsx, App.tsx, content-meta.tsx, and Calendar.tsx, checked against the desktop and mobile captures in .impeccable/review. The stylesheet remains the implementation source of truth.

**Key Characteristics:**

- Dark and light themes share semantic color roles.
- Flat planning surfaces use borders and tonal changes for grouping.
- Compact controls retain text labels beside status and format icons.
- The weekly matrix scrolls horizontally while row labels remain visible.

## Colors

The palette combines cool neutral surfaces with a muted violet accent. Frontmatter keys without a suffix record the dark theme. The corresponding `-light` keys record light-theme overrides; unchanged values are shared. Component references show the dark mapping, while the live implementation uses theme-aware CSS variables.

### Primary

- **Violet accent:** primary actions, ready dots, target tracks, current-day marks, selected mobile navigation, and explicit focus treatments.
- **Accent ink:** white text on filled violet actions.

### Neutral

- **Background and sidebar:** the outer workspace and navigation.
- **Panel and editor:** library, toolbar, and writing or full-width page regions.
- **Surface and control:** card faces, matrix headers, fields, and day cells.
- **Surface strong and hover:** active navigation, selected library cards, and interaction feedback.
- **Ink and text strong:** principal copy and titles. **Text soft** supports secondary values; **muted and text muted** support labels and metadata.
- **Line, line soft, and line strong:** fine divisions, quiet separators, and stronger card or field outlines.
- **Placeholder and platform icon:** field hints and neutral format symbols.

### Semantic states

Warning and danger identify lifecycle or save problems. Idea, draft, and published dots use quiet neutral colors; ready uses the accent. Status labels accompany dots.

**The Shared Accent Rule.** Use the theme accent for primary actions, ready state, current-day marks, target progress, and explicit selection or focus treatments.

## Typography

Geist Variable serves headings, body copy, and controls, with Arial, Helvetica, and sans-serif fallbacks. Geist Mono Variable is used for script block numbers.

The hierarchy is compact and continuous. Weight and spacing distinguish roles within one family; the editor adds a larger, tighter document title.

- **Display:** the responsive editor document heading.
- **Headline:** full-page headings. The weekly heading uses the larger planner-title role and becomes 27px at the smallest breakpoint.
- **Title:** rhythm editor and unscheduled tray section headings.
- **Body:** general content at the base reading line height. Script and plain-text editors have their own roomier line heights.
- **UI and label:** navigation, field text, counts, and supporting copy.
- **Card title:** compact weekly titles, wrapped across up to three lines. Status and target metadata use 11px locally.
- **Numbers:** weekly summary counts and calendar dates use tabular figures where declared.

The extracted roles record observed values, not a mathematical type scale.

## Layout

The desktop shell uses a sidebar, an optional library column, and a flexible editor. The standard columns are 238px, 380px, and the remaining width; the sidebar can collapse to 68px. Full-width pages span the library and editor area, with padding of 34px 38px 44px.

The weekly page contains a heading and week controls, a ruled progress summary, the matrix, and an unscheduled tray. Its seven day columns run Monday through Sunday. YouTube leads the visible format rows. Row headers hold a title, planned/target text, and a thin progress track. These are weekly-view patterns, not required layouts for every screen.

The matrix is a fixed-layout table with a 950px minimum width and sticky 166px row labels. Its scroll container stays keyboard focusable. Day cells start at 107px tall and grow with their content. The tray uses an auto-filling grid with a 215px minimum card column and 18px gaps. Rhythm fields use three columns.

### Responsive behavior

- **1180px:** the standard shell narrows to a 220px sidebar and 350px library.
- **1100px:** weekly summary content wraps and rhythm fields use two columns.
- **900px:** the sidebar gives way to a fixed four-item bottom navigation bar, 58px tall. Pages use 22px 18px 32px padding and reserve the bar's height. The matrix remains horizontally scrollable, with a 980px minimum width.
- **600px:** week controls wrap, totals fill a row, sticky matrix labels narrow to 145px, and rhythm fields use one column. The tray heading and search stack. Day selects, composer fields and actions, and tray search reach 44px minimum height.

Spacing values in frontmatter are recurring observed steps. The implementation also has local optical adjustments; it does not expose a global spacing variable scale.

## Elevation & Depth

Planning surfaces are flat, separated by fine borders and nearby neutral tones. Selected library cards use a narrow inset violet edge. The weekly current day has an inset top mark and a faint accent wash; drag destinations use an inset outline.

Existing overlays add depth: modals use a diffuse outer shadow and blurred backdrop, while the mobile library drawer uses a side shadow. Exact shadow and motion values are carried in the sidecar. Day-cell feedback transitions over 150ms; existing overlays and saved-state feedback use short 180ms to 220ms transitions.

**The Flat Planning Rule.** Planning cards and containers use borders and tonal surfaces at rest. Outer shadows belong to the existing modal and mobile drawer layers.

## Shapes

Compact rounded rectangles are the recurring form. Small fields and selects use 4px corners, weekly cards and calendar chips use 5px, primary actions and search containers use 6px, and navigation or larger planning containers use 8px. Icon buttons and desktop modals use 9px. Avatars and status dots are circular.

Most boundaries are one-pixel strokes. Matrix rows and columns form a continuous ruled grid within a rounded scroll container. Platform symbols are Lucide line icons using the shared neutral icon color.

## Components

### Buttons

Primary actions use violet fill, white text, and the primary-button frontmatter dimensions. Hover raises brightness to 1.06; disabled actions use 0.35 opacity and a blocked cursor. Icon buttons are transparent at rest and gain a neutral fill and border on hover.

Weekly Add buttons are transparent and span their cell. Empty cells give them a 90px minimum height; hover and keyboard focus strengthen the border and text. Most other buttons retain the browser's native focus outline. There is no shared custom primary-button focus ring in the current CSS.

### Inputs / Fields

Library search uses a bordered neutral container and changes its border to violet on focus within. Weekly tray search retains a native input outline with an offset. The inline composer has an accent outline, a small text field, and Add and Cancel actions. Rhythm target fields are numeric, 62px wide, and use a stronger neutral border.

### Navigation

Desktop navigation uses full-width text-and-icon rows, muted at rest and filled with surface strong when active. Collapsing the sidebar hides labels and counts and leaves an icon rail with control titles. Mobile uses four fixed bottom actions; the active item has violet text and a faint accent wash, with an inset violet keyboard focus outline.

### Cards / Containers

Weekly cards use a strong neutral border, compact padding, and no outer shadow. A title button opens the record; a labelled native select below it changes the scheduled day. Cards lighten on hover, fade to 0.45 while dragged, and use 0.62 opacity with a waiting cursor while scheduling is blocked.

The rhythm editor is an inline bordered surface above the matrix. Its field grid adapts across three, two, and one column. The unscheduled tray reuses weekly cards below a divider, with format labels and search.

### Chips

Calendar items are small rounded, bordered rows with a title action and native date control. They use the same neutral surfaces and drag or blocked opacity treatments as weekly cards. Status on weekly cards is a text label with a dot, without a pill background.

### Weekly matrix

The header marks today with a violet top edge; the corresponding day cells use a five-percent accent mix. Target bars are three pixels tall. The drop highlight is limited to matching content-format rows. Empty cells retain a labelled Add control, and moving a card remains available through its day selector.

## Do's and Don'ts

### Do:

- **Do** use the existing semantic CSS variables so both themes keep the same roles.
- **Do** pair status dots with text labels and keep platform icons neutral.
- **Do** reuse the compact control and card shapes recorded here.
- **Do** preserve keyboard scrolling and sticky row labels in the weekly matrix.
- **Do** use the existing native day selector alongside drag interactions.

### Don't:

- **Don't** introduce a second accent for content formats.
- **Don't** use floating card shadows in the weekly plan.
- **Don't** hide the remaining days by clipping the weekly matrix.
- **Don't** encode status through color alone.
