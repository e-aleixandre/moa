import { expect, test } from "bun:test";
import { EVENT_BODY_PREVIEW, eventAge, eventBodyLang, eventBodyLine, eventBodyPreview } from "./EventBlock.jsx";

test("a long event payload is previewed and keeps its full text for Show all", () => {
  const body = "{".repeat(EVENT_BODY_PREVIEW + 40);
  const preview = eventBodyPreview(body);
  expect(preview.truncated).toBe(true);
  expect(preview.text).toHaveLength(EVENT_BODY_PREVIEW + 1);
  expect(body).toContain(preview.text.slice(0, -1));
});

test("a short payload does not offer an expansion that would change nothing", () => {
  expect(eventBodyPreview('{"status":"failed"}')).toEqual({ text: '{"status":"failed"}', truncated: false });
});

test("an event with no body renders no payload at all", () => {
  expect(eventBodyPreview(undefined)).toEqual({ text: "", truncated: false });
});

test("the block speaks the session list's clock", () => {
  const now = Date.now();
  expect(eventAge(now - 20_000)).toBe("now");
  expect(eventAge(now - 12 * 60_000)).toBe("12m");
  expect(eventAge(now - 5 * 3600_000)).toBe("5h");
  expect(eventAge(now - 3 * 86400_000)).toBe("3d");
});

// A fixture (or a server that already formatted the age) must survive intact:
// re-parsing "18m" as a date would blank the only provenance the header has.
test("an already-formatted age is passed through, and garbage is dropped", () => {
  expect(eventAge("18m")).toBe("18m");
  expect(eventAge(undefined)).toBe("");
  expect(eventAge("not a date")).toBe("not a date");
});

// Providers post arbitrary bodies. Only a payload that really parses as JSON
// is highlighted; a prose alert must not have its words coloured at random.
test("only a payload that parses as JSON is highlighted as JSON", () => {
  expect(eventBodyLang('{"level":"error"}')).toBe("json");
  expect(eventBodyLang("[1, 2, 3]")).toBe("json");
  expect(eventBodyLang("Checkout is down for everyone")).toBeUndefined();
  expect(eventBodyLang("{not really json")).toBeUndefined();
  expect(eventBodyLang(undefined)).toBeUndefined();
});

// A truncated preview is no longer valid JSON, so the collapsed view falls back
// to plain text rather than silently losing the highlight it promised.
test("a truncated payload is not claimed to be JSON", () => {
  const body = `{"a":"${"x".repeat(EVENT_BODY_PREVIEW)}"}`;
  expect(eventBodyLang(eventBodyPreview(body).text)).toBeUndefined();
  expect(eventBodyLang(body)).toBe("json");
});

test("a payload flattens to one line for the inbox row", () => {
  expect(eventBodyLine('{\n  "ok": true\n}')).toBe('{ "ok": true }');
  expect(eventBodyLine(undefined)).toBe("");
});
