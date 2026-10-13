import { expect, test } from "bun:test";
import { HistoryDetail } from "./HistoryDetail.jsx";

function nodesOf(node, nodes = []) {
  if (!node || typeof node !== "object") return nodes;
  nodes.push(node);
  for (const child of [node.props?.children].flat(Infinity)) nodesOf(child, nodes);
  return nodes;
}
function textOf(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node !== "object") return String(node);
  return [node.props?.children].flat(Infinity).map(textOf).join("");
}
const byClass = (node, cls) => nodesOf(node).filter((n) => String(n.props?.class || "").split(" ").includes(cls));

test("each retrieved message shows who wrote it, when, and its text; the requested one is marked", () => {
  const node = HistoryDetail({ action: "around", result: "  [#1 id=a 2026-10-13 08:07 user] the canary port is 7413\n» [#2 id=b 2026-10-13 08:08 ask_user answer, answered by an automation] No\n" });
  const hits = byClass(node, "history-hit");
  expect(hits).toHaveLength(2);
  expect(textOf(byClass(hits[0], "history-hit-author")[0])).toBe("user");
  expect(textOf(hits[0])).toContain("2026-10-13 08:07");
  expect(textOf(byClass(hits[0], "history-hit-text")[0])).toBe("the canary port is 7413");
  expect(hits[1].props.class).toContain("focus");
  expect(textOf(byClass(hits[1], "history-hit-requested")[0])).toBe("requested");
});

test("text it cannot read is shown as it came", () => {
  const node = HistoryDetail({ action: "read", result: "no message with id \"x\" in this session" });
  expect(node.props.class).toBe("history-detail-raw");
});
