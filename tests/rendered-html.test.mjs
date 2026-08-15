import assert from "node:assert/strict";
import { access, readFile, readdir } from "node:fs/promises";
import test from "node:test";

test("builds the ContentFlow client application", async () => {
  const buildRoot = new URL("../apps/api/web/dist/", import.meta.url);
  const html = await readFile(new URL("index.html", buildRoot), "utf8");
  assert.match(html, /ContentFlow/);
  assert.match(html, /ContentFlow · Your content, in one place/);
  assert.match(html, /contentflow-theme/);
  assert.equal((html.match(/__CONTENTFLOW_SOCIAL_IMAGE__/g) ?? []).length, 2);
  assert.match(html, /<div id="root"><\/div>/);

  const assets = await readdir(new URL("assets/", buildRoot));
  assert.ok(assets.some((name) => name.endsWith(".js")), "Vite build should contain JavaScript");
  assert.ok(assets.some((name) => name.endsWith(".css")), "Vite build should contain CSS");
});

test("ships the final product surface without starter artifacts", async () => {
  const [page, html, packageJson] = await Promise.all([
    readFile(new URL("../apps/web/src/App.tsx", import.meta.url), "utf8"),
    readFile(new URL("../apps/web/index.html", import.meta.url), "utf8"),
    readFile(new URL("../package.json", import.meta.url), "utf8"),
  ]);

  for (const label of ["YouTube", "LinkedIn", "Instagram", "TikTok", "Email", "Substack"]) {
    assert.match(page, new RegExp(label));
  }
  assert.match(page, /function createRepurposedDrafts/);
  assert.match(page, /className="plain-editor"/);
  assert.match(page, /className="script-block"/);
  assert.equal((html.match(/__CONTENTFLOW_SOCIAL_IMAGE__/g) ?? []).length, 2);
  assert.doesNotMatch(packageJson, /vinext|react-server-dom-webpack|wrangler/);
  assert.doesNotMatch(packageJson, /react-loading-skeleton/);

  await access(new URL("../apps/web/public/og.png", import.meta.url));
});
