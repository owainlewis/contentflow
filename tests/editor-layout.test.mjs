import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { extname } from "node:path";
import test from "node:test";

// Renders the built editor in a real browser at phone, tablet and desktop
// widths, against a mocked API, and checks the layout rules that jsdom cannot:
// no sideways overflow, a toolbar that wraps instead of clipping, and writing
// fields that do not swallow the viewport.

const origin = "http://contentflow.test";
const distRoot = new URL("../apps/api/web/dist/", import.meta.url);
const viewports = [
  { width: 319, height: 710 },
  { width: 390, height: 844 },
  { width: 768, height: 1024 },
  { width: 1280, height: 800 },
];
const contentByType = {
  youtube: { topic: "", icp: "", angle: "", cta: "", publishing_title: "Inside the factory", description: "", transcript: "", sections: [{ id: "s1", title: "Hook", body: "" }] },
  linkedin: { body: "" },
  x: { body: "" },
  instagram: { script: "", caption: "" },
  tiktok: { script: "" },
  email: { subject: "", body: "" },
  substack: { headline: "", subheadline: "", body: "" },
  topic: { source: "", source_url: "" },
};
const now = new Date().toISOString();
const items = Object.entries(contentByType).map(([type, content], index) => ({
  id: `01LAYOUT${String(index).padStart(18, "0")}`,
  type,
  status: "published",
  working_title: `A deliberately long ${type} working title that should never push the page sideways`,
  revision: 1,
  created_at: now,
  updated_at: now,
  expires_at: "9999-12-31T00:00:00Z",
  scheduled_at: type === "topic" ? undefined : "2026-09-30T09:00:00Z",
  format: undefined,
  document_url: "",
  video_url: "",
  asset_counts: {},
  content,
}));
// Two pieces inside the topic, so the embedded editors and "Open alongside" are covered too.
const topicId = items.find((item) => item.type === "topic").id;
for (const [index, type] of ["instagram", "youtube"].entries()) {
  const source = items.find((item) => item.type === type);
  items.push({ ...source, id: `01LAYOUTRELATED${String(index).padStart(11, "0")}`, topic_id: topicId, working_title: `Related ${type} piece` });
}
const longestSaveLabel = "Sign in to continue saving";
const mimeTypes = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2" };

async function launchBrowser() {
  let playwright;
  try {
    playwright = await import("playwright");
  } catch {
    return undefined;
  }
  // Prefer Playwright's own Chromium, then an installed Chrome.
  for (const options of [{}, { channel: "chrome" }]) {
    try {
      return await playwright.chromium.launch(options);
    } catch {
      // Try the next browser.
    }
  }
  return undefined;
}

async function serve(route) {
  const url = new URL(route.request().url());
  if (url.pathname === "/api/v1/session") return route.fulfill({ json: { csrf_token: "layout-test", workspace_id: "layout" } });
  if (url.pathname === "/api/v1/content") return route.fulfill({ json: { items: items.map((item) => ({ ...item, content: undefined })) } });
  const detail = url.pathname.match(/^\/api\/v1\/content\/([^/]+)$/);
  if (detail) return route.fulfill({ json: items.find((item) => item.id === decodeURIComponent(detail[1])) });
  if (url.pathname.startsWith("/api/")) return route.fulfill({ status: 404, json: { error: "not_found" } });
  const extension = extname(url.pathname);
  const file = extension ? new URL(url.pathname.slice(1), distRoot) : new URL("index.html", distRoot);
  try {
    return route.fulfill({ body: await readFile(file), contentType: mimeTypes[extension || ".html"] ?? "application/octet-stream" });
  } catch {
    return route.fulfill({ status: 404, body: "" });
  }
}

test("the editor fits every content type at phone, tablet and desktop widths", async (t) => {
  try {
    await access(new URL("index.html", distRoot));
  } catch {
    t.skip("Build the web app first (npm run build:web); apps/api/web/dist has no index.html.");
    return;
  }
  const browser = await launchBrowser();
  if (!browser) {
    t.skip("No browser for the layout check. Run `npx playwright install chromium` or install Google Chrome.");
    return;
  }
  t.after(() => browser.close());

  for (const viewport of viewports) {
    const page = await browser.newPage({ viewport, locale: "en-GB", timezoneId: "Europe/London" });
    await page.route(`${origin}/**`, serve);
    for (const item of items) {
      const label = `${item.type} at ${viewport.width}px`;
      await page.goto(`${origin}/content?id=${item.id}`);
      await page.locator(".editor-toolbar").waitFor();
      await page.locator(".editor-document .resource-field").first().waitFor();
      if (item.type === "topic") {
        await page.getByLabel("Open alongside").selectOption({ index: 1 });
        await page.locator(".topic-pieces.comparing .topic-piece .piece-editor").nth(1).waitFor();
      }
      // Measure with the longest save label the toolbar can show, not just "Saved".
      await page.evaluate((label) => {
        const state = document.querySelector(".editor-toolbar .saved-state");
        const text = [...state.childNodes].find((node) => node.nodeType === Node.TEXT_NODE);
        text.textContent = label;
      }, longestSaveLabel);
      const layout = await page.evaluate(() => {
        const box = (element) => {
          const rect = element.getBoundingClientRect();
          return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width, height: rect.height };
        };
        const toolbar = box(document.querySelector(".editor-toolbar"));
        const controls = [".toolbar-back", ".type-pill", ".status-select", ".schedule-chip", ".saved-state", ".delete-button"]
          .map((selector) => [selector, document.querySelector(`.editor-toolbar ${selector}`)])
          .filter(([, element]) => element)
          .map(([selector, element]) => ({ selector, ...box(element) }));
        const main = document.querySelector(".piece-main, .piece-topic-source");
        const rail = document.querySelector(".editor-document > .editor-content .piece-rail");
        const related = [...document.querySelectorAll(".topic-piece")].map((piece) => ({ main: box(piece.querySelector(".piece-main")), rail: box(piece.querySelector(".piece-rail")) }));
        return {
          toolbar,
          related,
          innerWidth: window.innerWidth,
          innerHeight: window.innerHeight,
          scrollWidth: document.documentElement.scrollWidth,
          controls,
          textareas: [...document.querySelectorAll(".editor-document textarea")].map((element) => ({ label: element.getAttribute("aria-label"), height: element.getBoundingClientRect().height })),
          title: box(document.querySelector(".document-field-large input")),
          firstField: box(document.querySelector(".editor-document .resource-field")),
          main: main && box(main),
          rail: rail && box(rail),
        };
      });

      assert.ok(layout.scrollWidth <= layout.innerWidth, `${label}: page scrolls sideways (${layout.scrollWidth}px wide)`);
      for (const control of layout.controls) {
        assert.ok(control.width > 0 && control.height > 0, `${label}: ${control.selector} is hidden`);
        assert.ok(control.left >= 0 && control.right <= layout.innerWidth, `${label}: ${control.selector} is clipped`);
        assert.ok(control.top >= layout.toolbar.top && control.bottom <= layout.toolbar.bottom, `${label}: ${control.selector} spills out of the toolbar`);
      }
      assert.equal(layout.controls.length, item.type === "topic" ? 5 : 6, `${label}: a toolbar control is missing`);
      for (const field of layout.textareas) {
        assert.ok(field.height <= layout.innerHeight * 0.5, `${label}: ${field.label} is ${field.height}px tall in a ${layout.innerHeight}px viewport`);
      }
      assert.ok(Math.abs(layout.title.left - layout.firstField.left) <= 1, `${label}: title and fields do not share a left edge`);
      if (layout.rail) {
        assert.ok(layout.rail.right <= layout.innerWidth, `${label}: links and details are clipped`);
        if (viewport.width <= 900) assert.ok(layout.rail.top >= layout.main.bottom, `${label}: links and details should follow the writing on narrow screens`);
        else assert.ok(layout.rail.left >= layout.main.right, `${label}: links and details should sit beside the writing on wide screens`);
      }
      if (item.type === "topic") assert.equal(layout.related.length, 2, `${label}: both related pieces should be open side by side`);
      for (const piece of layout.related) {
        assert.ok(piece.main.right <= layout.innerWidth && piece.rail.right <= layout.innerWidth, `${label}: a related piece is clipped`);
        assert.ok(piece.rail.top >= piece.main.bottom, `${label}: a related piece's links should follow its writing`);
      }
    }
    await page.close();
  }
});
