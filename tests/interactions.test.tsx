// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import Home from "../app/page";

afterEach(cleanup);

describe("ContentFlow interactions", () => {
  it("searches the library and filters by status", async () => {
    const user = userEvent.setup();
    render(<Home />);

    const search = screen.getByPlaceholderText("Search your content");
    await user.type(search, "plain text");
    expect(screen.getByRole("button", { name: /Why plain text beats a complex editor/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /You don’t need more content ideas/ })).toBeNull();

    await user.clear(search);
    await user.click(screen.getByRole("button", { name: "Ready" }));
    expect(screen.getByRole("button", { name: /You don’t need more content ideas/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Stop starting from scratch/ })).toBeNull();
  });

  it.each([
    ["YouTube", "Structured script blocks", true],
    ["LinkedIn", "Clean plain-text draft", false],
    ["X", "Clean plain-text draft", false],
    ["Short-form reels", "Clean plain-text draft", false],
    ["Email", "Clean plain-text draft", false],
    ["Substack", "Clean plain-text draft", false],
  ])("creates a %s draft with the correct editor", async (label, description, structured) => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: /New content/ }));
    await user.click(screen.getByRole("button", { name: new RegExp(`^${label}${description}`) }));

    expect((screen.getByLabelText("Content title") as HTMLInputElement).value).toBe(`Untitled ${label === "Short-form reels" ? "Reels" : label}`);
    if (structured) {
      expect(screen.getByLabelText("Intro script")).toBeTruthy();
      expect(screen.getByText("3 sections")).toBeTruthy();
    } else {
      expect(screen.getByLabelText("Content body")).toBeTruthy();
    }
  });

  it("edits plain text and structured YouTube blocks", async () => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: /You don’t need more content ideas/ }));
    const body = screen.getByLabelText("Content body") as HTMLTextAreaElement;
    await user.clear(body);
    await user.type(body, "A clearer LinkedIn draft.");
    expect(body.value).toBe("A clearer LinkedIn draft.");

    await user.click(screen.getByRole("button", { name: /Build an AI content system that actually saves time/ }));
    const intro = screen.getByLabelText("Intro script") as HTMLTextAreaElement;
    await user.clear(intro);
    await user.type(intro, "A sharper YouTube hook.");
    expect(intro.value).toBe("A sharper YouTube hook.");

    await user.click(screen.getByRole("button", { name: "Add section" }));
    expect(screen.getByText("5 sections")).toBeTruthy();
    expect(screen.getByLabelText("Section 5 script")).toBeTruthy();
  });

  it("repurposes a plain-text source into a structured YouTube draft", async () => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: /^LinkedIn2$/ }));
    expect(screen.getByRole("heading", { name: "LinkedIn" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /You don’t need more content ideas/ }));
    await user.click(screen.getByRole("button", { name: "Repurpose" }));

    for (const output of ["X", "Email", "Short-form reels"]) {
      const button = screen.getByRole("button", { name: output });
      expect(button.getAttribute("aria-pressed")).toBe("true");
      await user.click(button);
    }
    const youtube = screen.getByRole("button", { name: "YouTube" });
    expect(youtube.getAttribute("aria-pressed")).toBe("false");
    await user.click(youtube);
    await user.click(screen.getByRole("button", { name: /Create drafts/ }));

    expect((screen.getByLabelText("Content title") as HTMLInputElement).value).toMatch(/YouTube$/);
    expect(screen.getByRole("heading", { name: "All content" })).toBeTruthy();
    expect(screen.getByText("3 sections")).toBeTruthy();
    expect((screen.getByLabelText("Main section script") as HTMLTextAreaElement).value).toMatch(/better way to reuse the good ones/i);
  });

  it("closes dialogs with Escape and exposes output selection state", async () => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: "Repurpose" }));
    const linkedIn = screen.getByRole("button", { name: "LinkedIn" });
    expect(linkedIn.getAttribute("aria-pressed")).toBe("true");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: /Turn one idea into more/ })).toBeNull();
  });

  it("keeps the closed mobile library out of the focus tree", async () => {
    const user = userEvent.setup();
    const originalMatchMedia = window.matchMedia;
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: () => ({ matches: true, addEventListener() {}, removeEventListener() {} }),
    });

    render(<Home />);
    const library = document.querySelector<HTMLElement>('[aria-label="Content library"]');
    expect(library?.hasAttribute("inert")).toBe(true);
    expect(library?.getAttribute("aria-hidden")).toBe("true");

    await user.click(screen.getByRole("button", { name: "Open content library" }));
    expect(library?.hasAttribute("inert")).toBe(false);
    expect(library?.hasAttribute("aria-hidden")).toBe(false);

    cleanup();
    Object.defineProperty(window, "matchMedia", { configurable: true, value: originalMatchMedia });
  });
});
