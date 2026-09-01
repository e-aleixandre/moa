import { test, expect } from "bun:test";
import { catalogResponse, sessionInitEvent, CATALOG_MODELS, CATALOG_CAPS } from "./catalog-backend.js";
import { CATALOG_SESSIONS } from "./specimen.js";
import { projectStream, liveTrayAgents } from "../data/stream-model.js";
import { normalizeHistory } from "../data/ws-handlers.js";

test("the lab answers the endpoints the chrome actually fetches", () => {
  expect(catalogResponse("GET", "/api/models")).toEqual(CATALOG_MODELS);
  expect(catalogResponse("GET", "/api/capabilities")).toEqual(CATALOG_CAPS);
  expect(catalogResponse("GET", "/api/model-preferences").pinned_models.length).toBeGreaterThan(0);
  expect(catalogResponse("GET", "/api/sessions/catalog-session/skills").skills.length).toBeGreaterThan(0);
  expect(catalogResponse("GET", "/api/sessions/catalog-session/mcp")).toEqual({ servers: [] });
  expect(catalogResponse("GET", "/api/fs/complete?path=/home/ealeixandre/dev/moa/").entries).toContain("desktop-design");
});

test("a config patch returns the display name the selector selected", () => {
  const res = catalogResponse("PATCH", "/api/sessions/catalog-session/config", {
    model: "openai/gpt-5-sol",
    thinking: "high",
  });
  expect(res).toEqual({ model: "GPT Sol", thinking: "high" });
});

test("creating a session does not 404", () => {
  const res = catalogResponse("POST", "/api/sessions", { cwd: "/tmp/demo" });
  expect(res.id).toMatch(/^lab-/);
  expect(res.cwd).toBe("/tmp/demo");
  expect(res.state).toBe("idle");
});

test("a missing subagent is 204, a live one has a transcript", () => {
  expect(catalogResponse("GET", "/api/sessions/ws-race/subagents/missing")).toBeNull();
  const live = catalogResponse("GET", "/api/sessions/ws-race/subagents/changelog");
  expect(live.status).toBe("running");
  expect(live.messages.length).toBeGreaterThan(0);
});

test("every catalog session projects without throwing", () => {
  for (const session of Object.values(CATALOG_SESSIONS)) {
    const blocks = projectStream(session);
    expect(Array.isArray(blocks)).toBe(true);
    expect(blocks.length).toBeGreaterThan(0);
  }
});

test("the running session has a live async subagent the dock can show", () => {
  const session = CATALOG_SESSIONS["ws-race"];
  expect(projectStream(session).some((b) => b.kind === "streaming")).toBe(true);
  expect(liveTrayAgents(session).some((c) => c.kind === "subagent")).toBe(true);
});

test("the permission session keeps a pending bash prompt", () => {
  expect(CATALOG_SESSIONS.deploy.pendingPerm.tool_name).toBe("bash");
});

test("unknown /api GET does not 404 the lab", () => {
  expect(catalogResponse("GET", "/api/version")).toEqual({});
});

test("a lab init keeps tools after the real history normalizer", () => {
  const evt = sessionInitEvent(CATALOG_SESSIONS.deploy);
  const messages = normalizeHistory(evt.data.messages);
  expect(messages.some((m) => m._type === "tool_start" && m.tool_name === "bash")).toBe(true);
  expect(evt.data.pending_permission.tool_name).toBe("bash");
});
