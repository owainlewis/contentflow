import { describe, expect, it } from "vitest";
import { normalizeUnicode15NFKC, normalizeUnicode15Title } from "../apps/web/src/unicode-normalization";

describe("Unicode 15 title normalization", () => {
  it.each([
    ["ＦＯＯ Straße", "foo strasse"],
    ["A\u030a", "å"],
    ["ﬃ", "ffi"],
    ["각", "각"],
  ])("matches the API for %s", (input, expected) => {
    expect(normalizeUnicode15Title(input)).toBe(expected);
  });

  it("does not apply normalization mappings introduced after Unicode 15", () => {
    const outlinedLatinCapitalA = "\u{1CCD6}";
    expect(outlinedLatinCapitalA.normalize("NFKC")).toBe("A");
    expect(normalizeUnicode15NFKC(outlinedLatinCapitalA)).toBe(outlinedLatinCapitalA);
    expect(normalizeUnicode15Title(outlinedLatinCapitalA)).toBe(outlinedLatinCapitalA);
  });
});
