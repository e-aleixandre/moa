import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* A phone scene that draws desktop density is worse than no scene: it is a
   green check over a screen nobody is looking at. That is not hypothetical --
   the sidebar's search door shipped on the phone as a full-width sunken box
   that did nothing, and all 33 scenes passed, because the two phone sidebar
   scenes asked for the 300px frame and then rendered desktop density in it.

   These two rules are the ones that failure needed. They are cheap and they
   are about the lab, not the product: the lab must not lie about which
   product it is photographing. */
const lab = readFileSync(join(import.meta.dir, "zones-lab.jsx"), "utf8");
const sidebarHost = lab.slice(lab.indexOf("function Sidebar({"), lab.indexOf("function Sidebar({") + 900);

describe("the sidebar lab host", () => {
  it("does not default phone scenes to desktop density", () => {
    // `density = "desktop"` as a default is the exact shape of the bug: the
    // scene passes a phone frame, says nothing about density, and gets the
    // desktop head.
    expect(sidebarHost).not.toMatch(/density\s*=\s*["']desktop["']/);
  });

  it("passes onSearch, so the scenes photograph the door and not the inert branch", () => {
    // Sidebar renders a non-interactive <span> when onSearch is missing. A
    // golden of that span proves nothing about the control users tap.
    expect(sidebarHost).toMatch(/onSearch=/);
  });
});
