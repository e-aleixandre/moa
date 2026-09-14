import { expect, test } from "bun:test";
import { setPopoverOpenFromClick } from "./popover-click.js";

function dispatchPointerClick(target, detail) {
  for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) {
    const event = new Event(type);
    Object.defineProperty(event, "detail", { value: detail });
    target.dispatchEvent(event);
  }
}

function popoverTrigger() {
  const target = new EventTarget();
  let open = false;
  const states = [];
  target.addEventListener("click", (event) => {
    setPopoverOpenFromClick((update) => {
      open = update(open);
      states.push(open);
    }, event);
  });
  return { target, states, isOpen: () => open };
}

test("isolated clicks still open and close a popover", () => {
  const trigger = popoverTrigger();

  dispatchPointerClick(trigger.target, 1);
  dispatchPointerClick(trigger.target, 1);

  expect(trigger.states).toEqual([true, false]);
});

test("the second click of a native double-click does not undo the open", () => {
  const trigger = popoverTrigger();

  dispatchPointerClick(trigger.target, 1);
  dispatchPointerClick(trigger.target, 2);
  trigger.target.dispatchEvent(new Event("dblclick"));

  expect(trigger.states).toEqual([true, true]);
  expect(trigger.isOpen()).toBe(true);
});

test("a continued native click burst keeps the popover open", () => {
  const trigger = popoverTrigger();

  for (let detail = 1; detail <= 6; detail++) {
    dispatchPointerClick(trigger.target, detail);
  }

  expect(trigger.states).toEqual([true, true, true, true, true, true]);
});
