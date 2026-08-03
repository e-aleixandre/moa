import { useEffect } from "preact/hooks";

const ITEM_SELECTOR = '[role^="menuitem"]';

export function useMenuKeyboard(open, setOpen, triggerRef, menuRef) {
  const restoreFocus = () => {
    setOpen(false);
    requestAnimationFrame(() => triggerRef.current?.focus());
  };

  useEffect(() => {
    if (!open) return;
    const focusInitialItem = () => {
      const items = menuRef.current?.querySelectorAll(ITEM_SELECTOR);
      const selected = [...(items || [])].find((item) => item.getAttribute("aria-checked") === "true");
      (selected || items?.[0])?.focus();
    };
    const frame = requestAnimationFrame(focusInitialItem);
    return () => cancelAnimationFrame(frame);
  }, [open, menuRef]);

  const onMenuKeyDown = (event) => {
    const items = [...(menuRef.current?.querySelectorAll(ITEM_SELECTOR) || [])];
    if (!items.length) return;
    const current = items.indexOf(document.activeElement);
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      restoreFocus();
      return;
    }
    let next;
    if (event.key === "ArrowDown") next = (current + 1 + items.length) % items.length;
    else if (event.key === "ArrowUp") next = (current - 1 + items.length) % items.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = items.length - 1;
    else return;
    event.preventDefault();
    items[next].focus();
  };

  return { onMenuKeyDown, closeMenu: restoreFocus };
}
