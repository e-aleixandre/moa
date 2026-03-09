# TUI Usage

## Slash commands

- `/model <spec>` — switch model
- `/models` — open model picker
- `/thinking <level>` — set thinking level
- `/permissions <mode>` — set permission mode (`yolo|ask|auto`)
- `/compact` — force compaction
- `/clear` — clear conversation and start fresh session
- `/exit` or `/quit` — quit

## Keybindings

- `Enter` — send
- `Ctrl+J` (and `Alt+Enter` on compatible terminals) — newline
- `Ctrl+T` — toggle thinking visibility
- `Shift+Tab` — cycle thinking level
- `Ctrl+P` — cycle pinned models
- `Ctrl+Y` — cycle permission mode (`yolo -> ask -> auto`)
- `Ctrl+E` — expand/collapse tool output
- `Ctrl+O` — transcript mode toggle
- `PgUp` / `PgDn` — scroll
- `Ctrl+C` — clear input / abort run / quit (depends on state)
- `Ctrl+D` — quit

## Session browser

Open with:

```bash
moa --resume
```

Inside browser:

- `↑/↓` navigate sessions
- `Enter` open selected session
- `Ctrl+N` create new session
- typing filters by title/id
- `Backspace` edits filter

## Permission prompts

When permissions mode is `ask` or `auto`, the input area is replaced by a selection prompt.

- number keys / arrows + enter to decide
- `Tab` to amend feedback text
- in `auto` mode: "Add rule" writes a new evaluator rule
