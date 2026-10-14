import { parseUnifiedDiff } from "../../data/util/unified-diff.js";

// Rows mounted across ALL open files of one apply_patch card before "Show all".
// The cap is per card, not per file: fifty open files of 300 rows each is the
// same 15k-row freeze as one huge file.
export const PATCH_ROWS_CAP = 400;

// patchRowBudget parses the diff of each OPEN file and hands out rows in order
// until the card's cap is spent. Returns, per file, the rows to mount (null for
// a closed or diff-less file) plus how many were held back in total.
export function patchRowBudget(files, isOpen, cap = PATCH_ROWS_CAP) {
  let left = cap;
  let hidden = 0;
  const shown = files.map((file, i) => {
    if (!file.diff || !isOpen(i)) return null;
    const rows = parseUnifiedDiff(file.diff);
    const take = Math.max(0, Math.min(rows.length, left));
    left -= take;
    hidden += rows.length - take;
    return rows.slice(0, take);
  });
  return { shown, hidden };
}
