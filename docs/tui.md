# TUI Usage

The TUI is the default interactive interface. Launch with `moa`.

## Slash commands

Type `/` to open the command palette, or type a command directly:

| Command | Description |
|---------|-------------|
| `/model <spec>` | Switch model (or open model picker with no args) |
| `/models` | Open model picker and manage pinned models |
| `/thinking <level>` | Set thinking level (`off`/`minimal`/`low`/`medium`/`high`) |
| `/permissions <mode>` | Set permission mode (`yolo`/`ask`/`auto`) |
| `/path [list\|add\|rm\|scope]` | View or manage path access scope |
| `/plan` | Toggle plan mode |
| `/tasks [done\|reset\|show]` | View or manage implementation tasks |
| `/memory [edit\|clear\|path]` | View or manage project memory |
| `/undo` | Revert files changed in the last agent turn |
| `/verify` | Run project verification checks |
| `/prompt <name>` | Insert a prompt template |
| `/compact` | Force context compaction |
| `/voice` | Toggle voice recording |
| `/settings` | Open settings menu |
| `/clear` | Clear conversation, start fresh session |
| `/exit` | Quit |

## Keybindings

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+J` / `Alt+Enter` | Insert newline |
| `Ctrl+T` | Toggle thinking visibility |
| `Shift+Tab` | Cycle thinking level |
| `Ctrl+P` | Cycle pinned models |
| `Ctrl+Y` | Cycle permission mode |
| `Ctrl+E` | Expand/collapse tool output |
| `Ctrl+O` | Toggle transcript mode |
| `Ctrl+R` | Toggle voice recording |
| `PgUp` / `PgDn` | Scroll |
| `Ctrl+C` | Clear input / abort run / quit |
| `Ctrl+D` | Quit |

## Plan mode

Toggle with `/plan`. In plan mode:

1. The agent can only read, search, and write to a plan file — no code changes.
2. You review the plan and decide to execute, revise, or cancel.
3. On execution, the agent follows the plan with full tool access.

Tasks created during planning are tracked and shown in the status bar.

## Session browser

Open with `moa --resume`:

- `↑/↓` — navigate sessions
- `Enter` — open selected
- `Ctrl+N` — new session
- Type to filter by title/id

## Permission prompts

In `ask` or `auto` mode, tool calls prompt for approval:

- Number keys or arrows + enter to decide
- `Tab` to add feedback
- In `auto` mode: "Add rule" writes a new evaluator rule

## Voice input

Requires `openai-transcribe` login and `sox` (macOS) or `arecord` (Linux) installed. Toggle with `Ctrl+R` or `/voice`.
