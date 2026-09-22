import { expect, mock, test } from "bun:test";
import { readFileSync } from "node:fs";
import { focusPanelSubpage, panelAccessibleName } from "./session-panel.js";

const mobileConversationScreen = readFileSync(
  new URL("../layout/mobile/MobileConversationScreen/MobileConversationScreen.jsx", import.meta.url),
  "utf8",
);

test("a panel sub-page moves focus to Back and names the dialog after that page", () => {
  const backButton = { focus: mock(() => {}) };

  focusPanelSubpage({ open: true, page: "ownerEdit", backButton });

  expect(backButton.focus).toHaveBeenCalledTimes(1);
  expect(panelAccessibleName({ kind: "owner" }, "ownerEdit")).toBe("Edit owner");
  expect(panelAccessibleName({ kind: "owner" }, "usage")).toBe("Usage");
  expect(mobileConversationScreen).toContain("title={panelAccessibleName(session, panel.page)}");
});

test("the root neither takes focus nor loses its dossier name", () => {
  const backButton = { focus: mock(() => {}) };

  focusPanelSubpage({ open: true, page: "root", backButton });

  expect(backButton.focus).not.toHaveBeenCalled();
  expect(panelAccessibleName({ kind: "owner" }, "root")).toBe("This owner");
});
