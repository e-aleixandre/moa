// What an owner's message to this session says, in one line.
//
// The owner writes assignments, and an assignment is long: context, criteria,
// what not to touch. Painted whole it buries the conversation it was sent
// into — on a phone one message is the whole screen. So the transcript folds
// it and shows this instead.
//
// The line has to say WHAT THE ASSIGNMENT WAS, not that an assignment exists:
// "Message from the owner" is a label, not a summary, and a reader scrolling
// back through a day of work needs to tell one encargo from another without
// opening either.

// Markdown decoration carries no meaning once the text is one line: a heading
// hash, a bullet, a quote mark and the emphasis pairs are all removed, and a
// link keeps its text and drops its target. Inline code keeps its content —
// `design/ambient-migration` IS the subject of half these messages.
//
// Only PAIRED delimiters are unwrapped. Deleting every `*`, `_` and backtick
// was shorter and wrong: these assignments are made of paths and identifiers,
// so `data/foo_bar.js` became `data/foobar.js` and `__init__` became `init` —
// the summary corrupting exactly the token it exists to show.
function stripDecoration(line) {
  // Code spans are masked before the emphasis pass and restored verbatim
  // after: inside backticks `__init__` is a name, not bold. Masking rather
  // than splitting, because a bold span may WRAP a code span
  // ("**Arregla `pkg/x_test.go`**") and its two delimiters would otherwise
  // land on opposite sides of the split and never pair up.
  const code = [];
  const masked = line.replace(/`([^`]+)`/g, (_, inner) => `\u0000${code.push(inner) - 1}\u0000`);
  return masked
    .replace(/^\s*(?:[#>]+|[-*+]|\d+[.)])\s+/, "")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/__([^_]+)__/g, "$1")
    .replace(/(^|[\s(])\*([^*\s][^*]*)\*(?=$|[\s).,;:!?])/g, "$1$2")
    .replace(/(^|[\s(])_([^_\s][^_]*)_(?=$|[\s).,;:!?])/g, "$1$2")
    .replace(/\u0000(\d+)\u0000/g, (_, i) => code[Number(i)])
    .replace(/\s+/g, " ")
    .trim();
}

// A first line that only announces what follows ("Contexto:", "Dos cosas:")
// summarises nothing. When it is short AND opens something — it ends in a
// colon, or is bare enough to be a title — the line after it is pulled up
// behind it, which is how these messages actually read.
const OPENER_MAX = 40;

function isOpener(line) {
  return line.length <= OPENER_MAX && /[:：]$/.test(line);
}

// What the FIRST line of a real encargo says is usually where to work and what
// not to do: "Trabajas en <worktree>, rama <x>. NO mergear, NO desplegar."
// Boilerplate, identical across assignments, useless for telling one from
// another. The subject lands a paragraph later — and it lands in bold, because
// that is how the owner writes an ask ("Encargo: **la skill book-init**",
// "**Hazlo colapsable.**"). Checked against the real owner messages in this
// project's transcripts, not guessed.
//
// So: the first line carrying bold, and only if none does, the first line. The
// search gives up early, because a bold word deep inside the criteria is a
// detail of the assignment rather than its subject.
const BOLD_SEARCH_LINES = 12;

function hasBold(line) {
  return /\*\*[^\s*][^*]*\*\*/.test(line) || /__[^\s_][^_]*__/.test(line);
}

export function ownerMessageSummary(text = "") {
  const raw = String(text).split("\n").filter((line) => line.trim());
  const lines = raw.map(stripDecoration).filter(Boolean);
  if (lines.length === 0) return "";
  const bold = raw.findIndex(hasBold);
  if (bold > 0 && bold < BOLD_SEARCH_LINES) {
    const summary = stripDecoration(raw[bold]);
    if (summary) return summary;
  }
  const first = lines[0];
  if (isOpener(first) && lines[1]) return `${first} ${lines[1]}`;
  return first;
}

// Below this, folding hides nothing worth a tap: the summary already shows
// about as much as the message says, and a chevron over two lines of text is
// ceremony. Measured on the plain text, not the markup.
export const OWNER_MESSAGE_FOLD_CHARS = 200;

export function ownerMessageFolds(text = "") {
  return String(text).trim().length > OWNER_MESSAGE_FOLD_CHARS;
}
