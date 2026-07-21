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
    // The backend's completed edit diff has a %4d line-number field before
    // its marker: "%4d -%s", "%4d +%s", or "%4d  %s". Recognize it before
    // standard unified rows so the display gutter uses the real file number.
    const numbered = raw.match(/^ *(\d+) ([+\- ])(.*)$/);
    if (numbered) {
      const no = parseInt(numbered[1], 10);
      const marker = numbered[2];
      if (marker === "-") {
        out.push({ oldNo: no, type: "del", text: numbered[3] });
        oldNo = no + 1;
      } else if (marker === "+") {
        out.push({ newNo: no, type: "add", text: numbered[3] });
        newNo = no + 1;
      } else {
        out.push({ oldNo: no, newNo, type: "ctx", text: numbered[3] });
        oldNo = no + 1;
        newNo++;
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

// countUnifiedDiffRows mirrors parseUnifiedDiff's row eligibility without
// splitting the complete diff into an array. Live previews use it to retain an
// absolute row offset while only parsing a bounded tail.
export function countUnifiedDiffRows(diffText) {
  let count = 0;
  let inHunk = false;
  let start = 0;

  for (let end = 0; end <= diffText.length; end++) {
    if (end !== diffText.length && diffText.charCodeAt(end) !== 10) continue;
    if (end === start) {
      start = end + 1;
      continue;
    }
    if (lineStartsWith(diffText, start, end, "\\ No newline at end of file") ||
        lineStartsWith(diffText, start, end, "diff --git") ||
        lineStartsWith(diffText, start, end, "index ") ||
        lineStartsWith(diffText, start, end, "--- ") ||
        lineStartsWith(diffText, start, end, "+++ ")) {
      start = end + 1;
      continue;
    }
    if (isHunkHeader(diffText, start, end)) {
      inHunk = true;
      start = end + 1;
      continue;
    }
    if (inHunk || isNumberedFallbackRow(diffText, start, end)) count++;
    start = end + 1;
  }
  return count;
}

function lineStartsWith(text, start, end, prefix) {
  return end - start >= prefix.length && text.startsWith(prefix, start);
}

function isHunkHeader(text, start, end) {
  let i = start;
  if (!lineStartsWith(text, i, end, "@@ -")) return false;
  i += 4;
  i = skipDigits(text, i, end);
  if (i === start + 4) return false;
  if (text.charCodeAt(i) === 44) {
    const countStart = ++i;
    i = skipDigits(text, i, end);
    if (i === countStart) return false;
  }
  if (text.charCodeAt(i++) !== 32 || text.charCodeAt(i++) !== 43) return false;
  const newStart = i;
  i = skipDigits(text, i, end);
  if (i === newStart) return false;
  if (text.charCodeAt(i) === 44) {
    const countStart = ++i;
    i = skipDigits(text, i, end);
    if (i === countStart) return false;
  }
  return text.charCodeAt(i++) === 32 && text.charCodeAt(i++) === 64 && text.charCodeAt(i) === 64;
}

function skipDigits(text, i, end) {
  while (i < end) {
    const code = text.charCodeAt(i);
    if (code < 48 || code > 57) break;
    i++;
  }
  return i;
}

function isNumberedFallbackRow(text, start, end) {
  let i = start;
  while (i < end && isWhitespace(text.charCodeAt(i))) i++;
  const numberStart = i;
  i = skipDigits(text, i, end);
  if (i === numberStart) return false;
  const whitespaceStart = i;
  while (i < end && isWhitespace(text.charCodeAt(i))) i++;
  const whitespaceCount = i - whitespaceStart;
  if (whitespaceCount === 0) return false;
  if ((text.charCodeAt(i) === 43 || text.charCodeAt(i) === 45) &&
      isWhitespace(text.charCodeAt(i + 1))) return true;
  return whitespaceCount >= 3;
}

function isWhitespace(code) {
  return code === 9 || code === 11 || code === 12 || code === 13 || code === 32;
}
