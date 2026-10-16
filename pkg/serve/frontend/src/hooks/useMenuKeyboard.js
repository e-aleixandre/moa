import { useEffect } from "preact/hooks";

const ITEM_SELECTOR = '[role^="menuitem"]';

// A disabled item cannot take focus, so it is not part of the keyboard's list:
// were it first, the menu would open with focus on nothing and swallow no keys.
export function navigableItems(items) {
  return [...(items || [])].filter((item) => !item.hasAttribute("disabled"));
}

// nextItemIndex is where a navigation key lands among `count` navigable items;
// `current` is -1 when focus is not on one. null = not a navigation key.
export function nextItemIndex(key, current, count) {
  if (key === "ArrowDown") return (current + 1 + count) % count;
  if (key === "ArrowUp") return (current - 1 + count) % count;
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  return null;
}

export function useMenuKeyboard(open, setOpen, triggerRef, menuRef) {
  const restoreFocus = () => {
    setOpen(false);
    requestAnimationFrame(() => triggerRef.current?.focus());
  };

  useEffect(() => {
    if (!open) return;
    const focusInitialItem = () => {
      const items = navigableItems(menuRef.current?.querySelectorAll(ITEM_SELECTOR));
      const selected = items.find((item) => item.getAttribute("aria-checked") === "true");
      (selected || items?.[0])?.focus();
    };
    const frame = requestAnimationFrame(focusInitialItem);
    return () => cancelAnimationFrame(frame);
  }, [open, menuRef]);

  const onMenuKeyDown = (event) => {
    const items = navigableItems(menuRef.current?.querySelectorAll(ITEM_SELECTOR));
    if (!items.length) return;
    const current = items.indexOf(document.activeElement);
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      restoreFocus();
      return;
    }
    const next = nextItemIndex(event.key, current, items.length);
    if (next === null) return;
    event.preventDefault();
    items[next].focus();
  };

  return { onMenuKeyDown, closeMenu: restoreFocus };
}
