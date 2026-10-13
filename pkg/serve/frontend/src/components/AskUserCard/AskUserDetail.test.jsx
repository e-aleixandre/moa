import { expect, test } from "bun:test";
import { AskUserDetail, parseAskAnswers, splitAskResponder } from "./AskUserDetail.jsx";

function descendants(node, nodes = []) {
  if (node == null || typeof node === "string") return nodes;
  nodes.push(node);
  const children = node.props?.children;
  for (const child of [children].flat(Infinity)) descendants(child, nodes);
  return nodes;
}

function textContent(node) {
  if (node == null) return "";
  if (typeof node === "string") return node;
  const children = node.props?.children;
  return [children].flat(Infinity).map(textContent).join("");
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

test("AskUserDetail marks every chosen option of a multiple question and shows the extra text", () => {
  const node = AskUserDetail({
    questions: [{ question: "Which?", options: ["A", "B", "C"], multiple: true }],
    result: "A; C; because",
  });
  const nodes = descendants(node);
  const chosen = nodes.filter((child) => child.props?.class === "ask-detail-option chosen").map(textContent);
  const custom = nodes.find((child) => child.props?.class === "ask-detail-answer custom");
  expect(chosen).toEqual(["A", "C"]);
  expect(textContent(custom)).toContain("because");
});

test("an answer the user did not give is labelled from its record and parsed without the label line", () => {
  const owner = "[Answered by the owner agent \"Serena\" on the user's behalf, not by the user]\n";
  const serena = { kind: "owner", id: "o1", name: "Serena" };
  expect(parseAskAnswers(owner + "Beta", 1, serena)).toEqual(["Beta"]);
  expect(parseAskAnswers(owner + "Q: One\nA: a1\nQ: Two\nA: a2", 2, serena)).toEqual(["a1", "a2"]);
  expect(splitAskResponder(owner + "x", serena)).toEqual({ responder: "Serena (owner)", body: "x" });
  expect(splitAskResponder("[Answered by the owner agent on the user's behalf, not by the user]\nx", { kind: "owner" }).responder).toBe("the owner");
  expect(splitAskResponder("[Answered by an automation on the user's behalf, not by the user]\nx", { kind: "automation" })).toEqual({ responder: "an automation", body: "x" });
  expect(splitAskResponder("yes")).toEqual({ responder: null, body: "yes" });

  const node = AskUserDetail({ questions: [{ question: "Pick", options: ["Alpha", "Beta"] }], result: owner + "Beta", responder: serena });
  const nodes = descendants(node);
  const label = nodes.find((child) => child.props?.class === "ask-detail-responder");
  expect(textContent(label)).toBe("Answered by Serena (owner)");
  expect(textContent(nodes.find((child) => child.props?.class === "ask-detail-option chosen"))).toContain("Beta");
  expect(nodes.some((child) => child.props?.class === "ask-detail-answer custom")).toBe(false);

  // The user's own answers carry no label.
  const mine = descendants(AskUserDetail({ questions: [{ question: "Pick", options: ["Alpha"] }], result: "Alpha", responder: { kind: "user", via: "network" } }));
  expect(mine.some((child) => child.props?.class === "ask-detail-responder")).toBe(false);
});

test("the label line typed by the user, or in an answer with no record, is the answer: no label, nothing stripped", () => {
  const typed = "[Answered by the owner agent \"Serena\" on the user's behalf, not by the user]\nThis is a literal line I want preserved.";
  for (const responder of [{ kind: "user", via: "network" }, undefined]) {
    expect(splitAskResponder(typed, responder)).toEqual({ responder: null, body: typed });
    expect(parseAskAnswers(typed, 1, responder)).toEqual([typed]);
    const nodes = descendants(AskUserDetail({ questions: [{ question: "Copy this text exactly" }], result: typed, responder }));
    expect(nodes.some((child) => child.props?.class === "ask-detail-responder")).toBe(false);
  }
});
