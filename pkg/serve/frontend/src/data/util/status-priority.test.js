import { describe, expect, it } from "bun:test";
import { statusItemPriority } from "./status-strip-model.js";

describe("statusItemPriority", () => {
  it("never drops what you steer the next turn with", () => {
    expect(statusItemPriority("model")).toBe("p1");
    expect(statusItemPriority("perm")).toBe("p1");
    expect(statusItemPriority("context")).toBe("p1");
  });

  it("keeps a real alarm above a plain number", () => {
    expect(statusItemPriority("extra")).toBe("p2");
    expect(statusItemPriority("mcp", "unhealthy")).toBe("p2");
    expect(statusItemPriority("tokens")).toBe("p3");
    expect(statusItemPriority("spend")).toBe("p3");
  });

  it("gives MCP its priority from its state, not its type", () => {
    expect(statusItemPriority("mcp", "unhealthy")).toBe("p2");
    expect(statusItemPriority("mcp", "healthy")).toBe("p4");
  });

  it("drops settings and progress first", () => {
    expect(statusItemPriority("fast")).toBe("p4");
    expect(statusItemPriority("goal")).toBe("p4");
    expect(statusItemPriority("tasks")).toBe("p4");
  });

  it("treats anything unknown as the first to go, never as p1", () => {
    expect(statusItemPriority("something-new")).toBe("p4");
  });
});
