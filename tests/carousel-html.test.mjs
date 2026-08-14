import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { JSDOM } from "jsdom";

const carouselUrl = new URL("../mockups/ai-engineer-design-planning-carousel.html", import.meta.url);

test("carousel presents the transcript as one nine-slide argument", async () => {
  const html = await readFile(carouselUrl, "utf8");
  const slides = html.match(/<article class="slide(?: [^"]*)?" aria-label="Slide \d of 9"/g) ?? [];

  assert.equal(slides.length, 9);
  assert.match(html, /Coding is no longer the <span class="accent">hard part<\/span>/);
  assert.match(html, /Better tools will not fix a/);
  assert.match(html, /Every feature moves through/);
  assert.match(html, /Good design answers/);
  assert.match(html, /Planning makes a design/);
  assert.match(html, /Spend your best thinking on the/);
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
  assert.equal(document.querySelector('.thumbnail[aria-current="true"]').getAttribute("aria-label"), "Show slide 1: Coding is no longer the hard part");
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
