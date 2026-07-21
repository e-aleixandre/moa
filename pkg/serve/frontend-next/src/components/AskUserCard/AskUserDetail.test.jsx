import { expect, test } from "bun:test";
import { AskUserDetail, parseAskAnswers } from "./AskUserDetail.jsx";

function descendants(node, nodes = []) {
  if (node == null || typeof node === "string") return nodes;
  nodes.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) descendants(child, nodes);
  return nodes;
}

function textContent(node) {
  if (node == null) return "";
  if (typeof node === "string") return node;
  const children = node.props?.children;
  return (Array.isArray(children) ? children : [children]).map(textContent).join("");
}

test("parseAskAnswers parses single, multiple, skipped, and empty results", () => {
  expect(parseAskAnswers("  yes  ", 1)).toEqual(["yes"]);
  expect(parseAskAnswers("Q: First\nA: a1\n\nQ: Second\nA: a2", 2)).toEqual(["a1", "a2"]);
  expect(parseAskAnswers("(skipped)", 1)).toEqual(["(skipped)"]);
  expect(parseAskAnswers("", 1)).toEqual([]);
  expect(parseAskAnswers(null, 2)).toEqual([]);
});

test("AskUserDetail shows selected options, free answers, and skipped answers", () => {
  const node = AskUserDetail({
    questions: [
      { question: "Pick one", options: ["Alpha", "Beta"] },
      { question: "Name it", options: ["Moa", "Other"] },
      { question: "Continue?", options: ["Yes", "No"] },
    ],
    result: "Q: Pick one\nA: Beta\n\nQ: Name it\nA: Nova\n\nQ: Continue?\nA: (skipped)",
  });
  const nodes = descendants(node);
  const chosen = nodes.find((child) => child.props?.class === "ask-detail-option chosen");
  const custom = nodes.find((child) => child.props?.class === "ask-detail-answer custom");
  const skipped = nodes.find((child) => child.props?.class === "ask-detail-answer skipped");

  expect(textContent(node)).toContain("Pick one");
  expect(textContent(node)).toContain("Name it");
  expect(textContent(node)).toContain("Continue?");
  expect(textContent(chosen)).toContain("Beta");
  expect(textContent(custom)).toContain("Nova");
  expect(textContent(skipped)).toContain("Skipped");
});
