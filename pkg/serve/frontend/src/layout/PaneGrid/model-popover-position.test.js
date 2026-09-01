import { expect, test } from "bun:test";
import { positionModelPopover } from "./model-popover-position.js";

const viewport = { width: 1200, height: 800 };
const popover = { width: 360, height: 560 };

test("model popover clamps its right-aligned position to the viewport", () => {
  expect(positionModelPopover(
    { top: 100, right: 50, bottom: 130 },
    popover,
    viewport,
  )).toEqual({ left: 8, top: 138 });
});

test("model popover opens above its badge when it would run below the viewport", () => {
  expect(positionModelPopover(
    { top: 700, right: 1100, bottom: 730 },
    popover,
    viewport,
  )).toEqual({ left: 740, top: 132 });
});

test("1280×800 conversation anchors remain fully visible", () => {
  const position = positionModelPopover(
    { top: 720, right: 1240, bottom: 750 },
    popover,
    { width: 1280, height: 800 },
  );
  expect(position).toEqual({ left: 880, top: 152 });
});

test("model popover stays within the viewport when neither side fits", () => {
  expect(positionModelPopover(
    { top: 300, right: 1100, bottom: 330 },
    { width: 360, height: 784 },
    viewport,
  )).toEqual({ left: 740, top: 8 });
});
