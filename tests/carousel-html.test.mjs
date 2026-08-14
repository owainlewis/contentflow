import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { JSDOM } from "jsdom";

const carouselUrl = new URL("../mockups/ai-engineer-design-planning-carousel.html", import.meta.url);

test("carousel presents the transcript as one nine-slide argument", async () => {
  const html = await readFile(carouselUrl, "utf8");
  const slides = html.match(/<article class="slide(?: [^"]*)?" aria-label="Slide \d of 9"/g) ?? [];

  assert.equal(slides.length, 9);
  assert.match(html, /My planning system for <span class="accent">Claude Code and Codex<\/span>/);
  assert.match(html, /Software still has/);
  assert.match(html, /Agents can build the/);
  assert.match(html, /<strong>Requirements<\/strong>/);
  assert.match(html, /<strong>Technical design<\/strong>/);
  assert.match(html, /You set the standard\. Tests and reviews collect the evidence\./);
  assert.match(html, /A task is a/);
  assert.doesNotMatch(html, /Coding is no longer the hard part/);
});

test("carousel uses the reference document's dark editorial system", async () => {
  const html = await readFile(carouselUrl, "utf8");

  assert.match(html, /--page: #03080f/);
  assert.match(html, /--paper: #0a1623/);
  assert.match(html, /--accent: #9db9ce/);
  assert.match(html, /radial-gradient\(circle at 88% 0%, rgb\(40 83 116 \/ 30%\)/);
  assert.match(html, /font-family: Georgia, "Times New Roman", serif/);
  assert.match(html, /class="phase agent-owned"/);
  assert.match(html, /class="phase shared"/);
});

test("carousel includes responsive, keyboard, and accessible navigation", async () => {
  const html = await readFile(carouselUrl, "utf8");

  assert.match(html, /@media \(max-width: 900px\)/);
  assert.match(html, /aria-label="Previous slide"/);
  assert.match(html, /aria-label="Next slide"/);
  assert.match(html, /event\.key === "ArrowLeft"/);
  assert.match(html, /event\.key === "ArrowRight"/);
  assert.match(html, /button\.setAttribute\("aria-current", isActive \? "true" : "false"\)/);
});

test("carousel controls and arrow keys move through the deck", async () => {
  const html = await readFile(carouselUrl, "utf8");
  const dom = new JSDOM(html, { runScripts: "dangerously" });
  const { document, KeyboardEvent } = dom.window;

  document.querySelector("#next").click();
  assert.equal(document.querySelector(".slide.is-active").getAttribute("aria-label"), "Slide 2 of 9");
  assert.equal(document.querySelector("#progress").value, "02 / 09");

  document.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" }));
  assert.equal(document.querySelector(".slide.is-active").getAttribute("aria-label"), "Slide 1 of 9");
  assert.equal(document.querySelector('.thumbnail[aria-current="true"]').getAttribute("aria-label"), "Show slide 1: My planning system for Claude Code and Codex");
});

test("carousel copy follows the plain-language constraints", async () => {
  const html = await readFile(carouselUrl, "utf8");
  const visibleCopy = html
    .replace(/<style>[\s\S]*?<\/style>/, "")
    .replace(/<script>[\s\S]*?<\/script>/, "")
    .replace(/<[^>]+>/g, " ");

  assert.doesNotMatch(visibleCopy, /—/);
  assert.doesNotMatch(visibleCopy, /\b(?:unlock|elevate|supercharge|transform|leverage|seamless|robust|holistic|game-changing)\b/i);
});
