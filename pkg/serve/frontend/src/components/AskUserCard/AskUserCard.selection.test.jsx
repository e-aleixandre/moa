import { expect, test } from "bun:test";
import { AskUserCard } from "./AskUserCard.jsx";

function descendants(node, result = []) {
  if (node == null || typeof node !== "object") return result;
  if (Array.isArray(node)) {
    node.forEach((child) => descendants(child, result));
    return result;
  }
  result.push(node);
  descendants(node.props?.children, result);
  return result;
}

function optionsFor(currentAnswer) {
  const tree = AskUserCard({
    question: "Continue?",
    options: [{ label: "Yes", recommended: true }, { label: "No" }],
    currentAnswer,
  });
  return descendants(tree).filter((node) => node.type === "button" && node.props?.class?.startsWith("ask-opt"));
}

test("options start unselected and exactly the current option is visibly and accessibly selected", () => {
  const initial = optionsFor("");
  expect(initial.map((option) => option.props.class)).toEqual(["ask-opt", "ask-opt"]);
  expect(initial.map((option) => option.props["aria-pressed"])).toEqual([false, false]);

  const selected = optionsFor("Yes");
  expect(selected.map((option) => option.props.class)).toEqual(["ask-opt chosen", "ask-opt"]);
  expect(selected.map((option) => option.props["aria-pressed"])).toEqual([true, false]);
  const selectedCheck = descendants(selected[0]).find((node) => node.props?.class === "ask-opt-check");
  const unselectedCheck = descendants(selected[1]).find((node) => node.props?.class === "ask-opt-check");
  expect(selectedCheck.props.children).toBeTruthy();
  expect(unselectedCheck.props.children).toBe(false);
});

test("free text does not leave a predefined option selected", () => {
  const options = optionsFor("Something else");
  expect(options.map((option) => option.props.class)).toEqual(["ask-opt", "ask-opt"]);
  expect(options.map((option) => option.props["aria-pressed"])).toEqual([false, false]);
});
