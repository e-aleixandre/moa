# Book tidy

Reorganize the book so sessions find what is in it, without losing anything. This is the owner's work, done on a copy, and it is deliberately conservative: moving text is safe, summarizing or deleting it is not.

Why it exists: sessions get the first 8192 bytes of `PROJECT.md` (the rest is cut, and the cut is marked); `read` returns at most 64 KiB of a file; `search` skips files larger than 64 KiB entirely. A book past those limits keeps the information on disk and loses it for everyone who matters. Rewriting an index without checking where each fact went has already lost real facts once, even though no file was deleted.

Four rules hold it up.

**Reorganize first; summarize and delete nothing.** Split, move and reorder. Every byte of the original stays, verbatim, in a place that `search` and `read` can reach.

**An average hides a loss.** One fact that no longer has a reachable home is a loss, however good the rest looks. Reject any single regression.

**You never approve your own deletion.** Whatever would be deleted or summarized is *proposed* by you and confirmed by the user with one tap, as a concrete plan. The tap covers that plan on that version of the book, not the next one. Approval of a reorganization does not authorize a later prune. Without that tap, nothing is removed.

**Decisions are never pruned.** Each keeps its date and its why. If a date or reason is missing in the source, write "unknown"; do not infer it.

Do not edit `OWNER.md` or any instruction file of the user, and do not touch another owner's book.

## 1. Prepare and freeze

1. Read `work/limpieza-del-libro.md` if it exists, and every decision about cleaning the book. Fix the scope with the user: which files, which goal.
2. Check that no session is working on the book (sessions list). A change made concurrently invalidates the plan: re-snapshot and start the plan again.
3. Take a snapshot (below) and keep its path. It is the way back, not an answer for sessions: a fact that exists only in a snapshot is unreachable.

### Snapshots

They live inside moa's own config directory, next to the book and not inside it (so they never enter a search, and uninstalling moa removes them): `<config>/codebases/<id>/snapshots/<UTC>-<digest>/`, directories 0700, files 0600, with `MANIFEST.sha256` (one sha256 per file) and a restore rehearsal. The owner's codebase directory is the one in `${MOA_CONFIG_DIR:-$HOME/.config/moa}/codebases/*/` whose `owner.json` has `root` equal to the project root; it holds `book/`. Set `CB` to it and run:

```bash snapshot
set -euo pipefail
umask 077
SNAPS="$CB/snapshots"
mkdir -p "$SNAPS"; chmod 700 "$SNAPS"
TMP=$(mktemp -d "$SNAPS/.tmp.XXXXXX")
cp -a "$CB/book" "$TMP/book"
(cd "$TMP/book" && LC_ALL=C find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$TMP/MANIFEST.sha256"
DIGEST=$(sha256sum "$TMP/MANIFEST.sha256" | cut -c1-12)
DEST="$SNAPS/$(date -u +%Y%m%dT%H%M%SZ)-$DIGEST"
find "$TMP" -type d -exec chmod 700 {} +
find "$TMP" -type f -exec chmod 600 {} +
mv "$TMP" "$DEST"
R=$(mktemp -d)
cp -a "$DEST/book/." "$R/"
if (cd "$R" && sha256sum --quiet -c "$DEST/MANIFEST.sha256") \
  && [ "$(cd "$R" && find . -type f | wc -l)" = "$(wc -l < "$DEST/MANIFEST.sha256")" ]; then
  rm -rf "$R"; echo "snapshot OK: $DEST"
else
  rm -rf "$R"; echo "RESTORE REHEARSAL FAILED: $DEST" >&2; exit 1
fi
```

Restoring is copying `<snapshot>/book/.` over the book after checking `MANIFEST.sha256` the same way. A snapshot that failed its rehearsal does not count.

Retention: always keep the snapshot taken before a cleanup until the user has accepted the cleanup. Beyond that, keep the last 10 and anything younger than 90 days. Before purging any, list what would go (names, dates, sizes) and tell the user; never purge silently, and never purge the only copy of something the book no longer holds. It is the same disk as the book: it protects against a bad edit, not against losing the machine, and you say so if asked.

## 2. Inventory

List the facts that matter, each with its qualifiers: date, reason, scope, source. Keep apart what was *observed*, a *temporary authorization*, a *historical decision* and a *recommendation not yet decided*; never turn "pending QA" into "closed" or "do not migrate" into a topic name. Have a different reader (a cheap subagent that reads only) look for facts you missed; check a sample and every delicate exception by hand.

## 3. Reorganize a copy

Work in a scratch directory outside the book, never in the live book.

1. Shorten `PROJECT.md` to an index: stable paths, one line per area or decision, state, what to read next. Keep it under 7 KiB. Before removing a sentence from it, check that the complete fact exists at a path `search` finds and `read` returns whole.
2. Split every file over 48 KiB into parts of at most 24 KiB at paragraph or heading boundaries, keeping the bytes **literal** (no rewording, no reflowing). Each part gets a short header saying where it came from and what the neighbours are; the old path becomes a map to the parts. Parts are an access step, not a verified sheet of an area.
3. Do not move every integrated `work/` file to `areas/` mechanically. A merged branch does not prove that no decision, debt or open QA is left in it.
4. Write a movement map (original range → destination) and a sha256 for each original and each part. Concatenating the parts must reproduce the original byte for byte; check it.

## 4. Evaluate before proposing anything

Write recovery questions from real use: "what did we decide about X and why", "what is the state of Y", "who asked for Z". Answer them using only what a session has: the first 8192 bytes of the index, `search`, and `read` (cut at 64 KiB). Compare each question before and after, clause by clause. Any answer that gets worse, or that cannot be found, is a regression: fix it or stop. Report what was not tested; a good average authorizes nothing.

Where the source contradicts itself on what is current, do not invent the current rule: mark the conflict and ask the user.

## 5. Propose, then apply

1. Show the user the diff and the movement map. Applying a reorganization means replacing book files through the `book` tool (or a plain copy of the checked tree) after re-checking that the book still matches the snapshot's hashes; if it changed, start again from section 1.
2. If you want to remove or summarize anything, give a list: file, range, bytes, why, and where any fact mixed into it will live. Nothing is selected by pattern; no id, refusal, exception or risk counts as disposable on its own. The user confirms with one tap that exact plan.
3. After the change, run the questions again against the live book and compare with the snapshot. Record in `work/limpieza-del-libro.md` what was done, the snapshot path, and what was not verified.

This skill does not make the apply step atomic or safe against concurrent writers; that is why section 1 stops other work first.
