// parseUnifiedDiff turns unified and numbered fallback diffs into display rows.
export function parseUnifiedDiff(diffText) {
  const lines = diffText.split("\n");
  const out = [];
  let oldNo = 0;
  let newNo = 0;
  let inHunk = false;
  for (const raw of lines) {
    if (raw === "") continue;
    if (raw.startsWith("\\ No newline at end of file")) continue;
    if (raw.startsWith("diff --git") || raw.startsWith("index ")) continue;
    if (raw.startsWith("--- ") || raw.startsWith("+++ ")) continue;
    const hunk = raw.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (hunk) {
      oldNo = parseInt(hunk[1], 10);
      newNo = parseInt(hunk[2], 10);
      inHunk = true;
      continue;
    }
    // Numbered fallback rows arrive while edit arguments are still streaming.
    if (!inHunk) {
      const fallbackChange = raw.match(/^\s*(\d+)\s+([+-])\s(.*)$/);
      if (fallbackChange) {
        const no = parseInt(fallbackChange[1], 10);
        out.push({
          oldNo: fallbackChange[2] === "-" ? no : undefined,
          newNo: fallbackChange[2] === "+" ? no : undefined,
          type: fallbackChange[2] === "+" ? "add" : "del",
          text: fallbackChange[3],
        });
        continue;
      }
      const fallbackContext = raw.match(/^\s*(\d+)\s{3}(.*)$/);
      if (fallbackContext) {
        const no = parseInt(fallbackContext[1], 10);
        out.push({ oldNo: no, newNo: no, type: "ctx", text: fallbackContext[2] });
      }
      continue;
    }
    if (raw.startsWith("+")) {
      out.push({ newNo, type: "add", text: raw.slice(1) });
      newNo++;
    } else if (raw.startsWith("-")) {
      out.push({ oldNo, type: "del", text: raw.slice(1) });
      oldNo++;
    } else {
      const text = raw.startsWith(" ") ? raw.slice(1) : raw;
      out.push({ oldNo, newNo, type: "ctx", text });
      oldNo++;
      newNo++;
    }
  }
  return out;
}
