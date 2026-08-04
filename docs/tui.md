# TUI Usage

The TUI is the default interactive interface. Launch with `moa`.

## Slash commands

Type `/` to open the command palette, or type a command directly:

| Command | Description |
|---------|-------------|
| `/model <spec>` | Switch model (or open model picker with no args) |
| `/models` | Open model picker and manage pinned models |
| `/thinking <level>` | Set thinking level (`off`/`low`/`medium`/`high`/`xhigh`) |
| `/permissions <mode>` | Set permission mode (`yolo`/`ask`/`auto`) |
| `/path [list\|add\|rm\|scope]` | View or manage path access scope (`remove` aliases `rm`) |
| `/plan` | Toggle plan mode |
| `/goal <objective> [flags]\|stop\|status` | Autonomous maker→verifier loop toward an objective |
| `/tasks [done\|reset\|show]` | View or manage implementation tasks |
| `/undo` | Revert files written/edited by the last agent turn (not bash, MCP, or subagent changes); skips any file changed since then to avoid clobbering it |
| `/branch` | Rewind to an earlier point and start a new conversation branch (alias `/back`) |
| `/mcp` | View MCP servers and enable/disable them |
| `/verify [dir]` | Run project verification checks — in `dir` to verify another repository or worktree |
| `/prompt <name>` | Insert a prompt template |
| `/rename <title>` | Rename the current session (marks the title manual so auto-titling won't overwrite it) |
| `/compact` | Force context compaction |
| `/handoff [--model <spec>] [--thinking <level>]` | Generate a curated handoff with the current model, then start and run a fresh session. Destination model and thinking inherit the current settings unless overridden. |
| `/prepare-compact` | Write a handoff note, then compact context |
| `/voice` | Toggle voice recording |
| `/settings` | Open settings menu |
| `/clear` | Clear conversation, start fresh session |
| `/exit` | Quit (alias `/quit`) |

`/handoff` is inspired by [Matt Pocock's handoff skill](https://github.com/mattpocock/skills/tree/main/skills/productivity/handoff): it references durable artifacts instead of duplicating them, and uses a fresh conversation to continue the live thread.

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
| `Ctrl+V` | Paste image from clipboard |
| `Ctrl+G` | Open the live-subagent picker / return from a subagent view |
| `Ctrl+B` | Promote a running synchronous subagent to the background |
| `↑` / `↓` | Recall input history (when idle, empty input) |
| `Tab` | Path completion (when no picker is active) |
| `Alt+Up` | Recall queued messages/commands back into the input |
| `PgUp` / `PgDn` | Scroll |
| `Ctrl+C` | Clear input / abort run / quit |
| `Ctrl+D` | Quit |

## Grok thinking and plan quota

With an xAI Grok model selected, the thinking selector opened by
`/thinking` or `Shift+Tab` offers only `low`, `medium`, and `high`; Grok does
not accept `off` or `xhigh`.

For an xAI OAuth login, the status line can show the account plan's reported
quota and any reported credit buckets. This is a best-effort read of a private
consumer endpoint, not a billing authority. It is unavailable for
`XAI_API_KEY`, whose separately metered developer API does not expose that
consumer plan meter here. A failed quota refresh only hides or leaves stale
status information; it never blocks inference.

## Queuing while the agent works

You can keep typing while a run is in flight — messages and slash commands are handled in **strict send order** (the order you sent them is the order the agent sees).

- **Messages** are steered onto a queue and delivered between steps of the current run (or start the next one if they arrive after it ends). A clipboard image staged with `Ctrl+V` rides along with the message.
- **Slash commands** are classified: `/compact`, `/clear`, `/model`, `/thinking`, `/verify`, and `/goal <objective>` **queue** as a barrier (shown with a `command` tag) and run at the next idle point in order; `/rename`, `/permissions`, `/path`, `/tasks`, `/goal status|stop` run **instantly**; `/handoff`, `/undo`, `/branch`, `/back`, `/plan` are **refused** while working — stop the run first. `/handoff` also needs an empty message queue so its brief contains every preceding message.
- `Alt+Up` pulls the whole queue back into the input for editing (commands keep their leading `/`); pressing `Ctrl+C`/`Esc` to abort also dumps the queue back into the input so nothing you lined up is lost. Queued images aren't restored — re-stage them if needed.

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
- `Ctrl+D` — delete selected session (press twice to confirm)
- `Esc` — exit the browser
- Type to filter by title/id

## Permission prompts

In `ask` or `auto` mode, tool calls prompt for approval:

- Number keys or arrows + enter to decide
- `Tab` to add feedback
- In `auto` mode: "Add rule" writes a new evaluator rule

## Voice input

Requires `openai-transcribe` login and `sox` (macOS) or `arecord` (Linux) installed. Toggle with `Ctrl+R` or `/voice`.

`Ctrl+R` also works while an agent question is open, where the transcript fills
the question's answer instead of the composer. Speech is appended to what the
answer already contains, and dictating over a highlighted option replaces it.

If it keeps mangling a name or a piece of jargon, add that word to
[`stt_vocabulary`](configuration.md#features). The model and the language hint
are configurable there too.
