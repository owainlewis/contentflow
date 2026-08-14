// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import Home from "../app/page";

afterEach(cleanup);

describe("ContentFlow interactions", () => {
  it("exposes the selected library item", async () => {
    const user = userEvent.setup();
    render(<Home />);

    const youtube = screen.getByRole("button", { name: /Build an AI content system that actually saves time/ });
    const linkedIn = screen.getByRole("button", { name: /You don’t need more content ideas/ });
    expect(youtube.getAttribute("aria-current")).toBe("true");
    expect(linkedIn.getAttribute("aria-current")).toBeNull();

    await user.click(linkedIn);
    expect(youtube.getAttribute("aria-current")).toBeNull();
    expect(linkedIn.getAttribute("aria-current")).toBe("true");
  });

  it("searches the library and filters by status", async () => {
    const user = userEvent.setup();
    render(<Home />);

    const search = screen.getByPlaceholderText("Search your content");
    await user.type(search, "plain text");
    expect(screen.getByRole("button", { name: /A writing tool should get out of the way/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /You don’t need more content ideas/ })).toBeNull();

    await user.clear(search);
    await user.click(screen.getByRole("button", { name: "Ready" }));
    expect(screen.getByRole("button", { name: /You don’t need more content ideas/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Stop starting from scratch/ })).toBeNull();
  });

  it.each([
    ["YouTube", "Video brief and script blocks", "youtube"],
    ["LinkedIn", "One focused post", "linkedin"],
    ["X", "One short-form post", "x"],
    ["Instagram", "Script and video asset", "instagram"],
    ["TikTok", "Script and video asset", "tiktok"],
    ["Email", "Subject line and email body", "email"],
    ["Substack", "Headline, sub-headline, and article", "substack"],
  ])("creates a %s draft with the correct editor", async (label, description, type) => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: /New content/ }));
    await user.click(screen.getByRole("button", { name: new RegExp(`^${label}${description}`) }));

    if (type === "youtube") {
      expect(screen.getByLabelText("YouTube topic")).toBeTruthy();
      expect(screen.getByLabelText("YouTube ICP")).toBeTruthy();
      expect(screen.getByLabelText("YouTube angle")).toBeTruthy();
      expect(screen.getByLabelText("YouTube CTA")).toBeTruthy();
      expect(screen.getByLabelText("YouTube title")).toBeTruthy();
      expect(screen.getByLabelText("YouTube description")).toBeTruthy();
      expect(screen.getByLabelText("Choose YouTube thumbnail")).toBeTruthy();
      expect(screen.getByLabelText("Intro script")).toBeTruthy();
      expect(screen.getByText("3 sections")).toBeTruthy();
    } else if (type === "email") {
      expect(screen.getByLabelText("Email subject")).toBeTruthy();
      expect(screen.getByLabelText("Email")).toBeTruthy();
    } else if (type === "substack") {
      expect(screen.getByLabelText("Substack headline")).toBeTruthy();
      expect(screen.getByLabelText("Substack sub-headline")).toBeTruthy();
      expect(screen.getByLabelText("Article body")).toBeTruthy();
    } else if (type === "instagram" || type === "tiktok") {
      expect(screen.getByLabelText(`${label} script`)).toBeTruthy();
      expect(screen.getByLabelText(`Choose ${label} video`)).toBeTruthy();
    } else {
      expect(screen.getByLabelText(`${label} post`)).toBeTruthy();
    }
  });

  it("edits and collapses the complete YouTube brief", async () => {
    const user = userEvent.setup();
    render(<Home />);

    const topic = screen.getByLabelText("YouTube topic") as HTMLTextAreaElement;
    const angle = screen.getByLabelText("YouTube angle") as HTMLTextAreaElement;
    await user.clear(topic);
    await user.type(topic, "Agent-first content systems");
    await user.clear(angle);
    await user.type(angle, "Treat content as a graph, not a folder.");
    expect(topic.value).toBe("Agent-first content systems");
    expect(angle.value).toBe("Treat content as a graph, not a folder.");

    const thumbnail = screen.getByLabelText("Choose YouTube thumbnail") as HTMLInputElement;
    await user.upload(thumbnail, new File(["image"], "agent-content-system.png", { type: "image/png" }));
    expect(screen.getByText("agent-content-system.png")).toBeTruthy();

    const brief = screen.getByText("Video brief").closest("details") as HTMLDetailsElement;
    expect(brief.open).toBe(true);
    await user.click(screen.getByText("Video brief"));
    expect(brief.open).toBe(false);
  });

  it("edits plain text and structured YouTube blocks", async () => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: /You don’t need more content ideas/ }));
    const body = screen.getByLabelText("LinkedIn post") as HTMLTextAreaElement;
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

    for (const output of ["X", "Email", "Instagram"]) {
      const button = screen.getByRole("button", { name: output });
      expect(button.getAttribute("aria-pressed")).toBe("true");
      await user.click(button);
    }
    const youtube = screen.getByRole("button", { name: "YouTube" });
    expect(youtube.getAttribute("aria-pressed")).toBe("false");
    await user.click(youtube);
    await user.click(screen.getByRole("button", { name: /Create drafts/ }));

    expect((screen.getByLabelText("YouTube topic") as HTMLTextAreaElement).value).toMatch(/You don’t need more content ideas/);
    expect(screen.getByRole("heading", { name: "All content" })).toBeTruthy();
    expect(screen.getByText("3 sections")).toBeTruthy();
    expect((screen.getByLabelText("Main section script") as HTMLTextAreaElement).value).toMatch(/better way to reuse the good ones/i);
  });

  it("captures and removes an Instagram video attachment", async () => {
    const user = userEvent.setup();
    render(<Home />);

    await user.click(screen.getByRole("button", { name: /Hook: If content creation feels exhausting/ }));
    expect(screen.getByText("stop-starting-from-scratch-v2.mp4")).toBeTruthy();

    const videoInput = screen.getByLabelText("Choose Instagram video") as HTMLInputElement;
    const video = new File(["video"], "content-system-instagram.mp4", { type: "video/mp4" });
    await user.upload(videoInput, video);
    expect(screen.getByText("content-system-instagram.mp4")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Remove Instagram video" }));
    expect(screen.queryByText("content-system-instagram.mp4")).toBeNull();
    expect(videoInput.value).toBe("");

    await user.upload(videoInput, video);
    expect(screen.getByText("content-system-instagram.mp4")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /You do not need another list of content ideas/ }));
    const tikTokInput = screen.getByLabelText("Choose TikTok video") as HTMLInputElement;
    expect(tikTokInput.value).toBe("");
    await user.upload(tikTokInput, video);
    expect(screen.getByText("content-system-instagram.mp4")).toBeTruthy();
  });

  it("switches between persistent light and dark themes", async () => {
    const user = userEvent.setup();
    document.documentElement.dataset.theme = "dark";
    window.localStorage.removeItem("contentflow-theme");
    render(<Home />);

    await user.click(screen.getAllByRole("button", { name: "Switch to light mode" })[0]);
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(window.localStorage.getItem("contentflow-theme")).toBe("light");

    await user.click(screen.getAllByRole("button", { name: "Switch to dark mode" })[0]);
    expect(document.documentElement.dataset.theme).toBe("dark");
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
