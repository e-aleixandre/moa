// fuzzy.js — pure subsequence fuzzy matching for the command palette.
//
// Ported verbatim (semantics) from the old SPA's CommandPalette.fuzzyMatch: a
// query matches a haystack when its characters appear, in order, somewhere in
// the haystack (a subsequence). No scoring — the palette ranks by MRU/section,
// not match quality (positional scoring is a NICE-TO-HAVE per PALETTE-SPEC §2).
//
// The caller is responsible for case: pass already-lowercased strings when a
// case-insensitive match is wanted (this is what the palette does), matching
// the old palette's `q.toLowerCase()` + lowercased haystack.

// fuzzyMatch reports whether `query` is a subsequence of `haystack`. An empty
// query matches everything (the palette shows the full list with no query).
export function fuzzyMatch(query, haystack) {
  if (!query) return true;
  if (!haystack) return false;
  let qi = 0;
  for (let i = 0; i < haystack.length && qi < query.length; i++) {
    if (haystack[i] === query[qi]) qi++;
  }
  return qi === query.length;
}

// fuzzyMatchIndices returns the haystack positions of the matched characters
// (greedy, left-to-right — the same walk fuzzyMatch does), or null when there
// is no match. Used to wrap matched characters in a highlight span. An empty
// query returns [] (a match with nothing to highlight).
export function fuzzyMatchIndices(query, haystack) {
  if (!query) return [];
  if (!haystack) return null;
  const out = [];
  let qi = 0;
  for (let i = 0; i < haystack.length && qi < query.length; i++) {
    if (haystack[i] === query[qi]) {
      out.push(i);
      qi++;
    }
  }
  return qi === query.length ? out : null;
}
