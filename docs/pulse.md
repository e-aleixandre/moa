# Moa Pulse

> Canonical product definition: [`PULSE.md`][pulse-spec] in the iOS repository.
> This document summarizes the Serve contract that makes it possible.

[pulse-spec]: https://github.com/e-aleixandre/moa-companion-ios/blob/feat/pulse-openai-realtime/PULSE.md

## What it is

Pulse is the iOS client and future CarPlay client for `moa serve`: the owner's
voice intermediary with all their conversations. It lets them check the state
of sessions, read conversations and activity, and act on them using natural
language.

The initial vertical is a continuous, hands-free call with OpenAI Realtime; the
narrative visual feed of sessions and conversations is a later phase.

## Architecture boundaries

- Moa retains the canonical reality of sessions, messages, events, and actions.
- Moa's API is generic and shared by Serve web and Pulse. There is no
  Pulse-specific operations projection or endpoint; reads and actions use the
  generic routes.
- The Pulse-aware pieces in Moa are device pairing, the Realtime client secrets
  broker, the guardian channel (`GET /api/pulse/guardian/ws`), and the session
  brief (`pkg/pulsebrief` + `pkg/serve/brief.go`).
- Audio travels directly between the iPhone and OpenAI Realtime. Moa does not
  receive, proxy, or persist it.
- Realtime tools run in the Swift app through typed calls to Moa's generic API;
  the model receives neither the Moa credential nor unrestricted HTTP.

## Access and context

A paired device represents the owner and can use Serve's full generic API
surface, except to administer pairing. The model can read user/assistant
messages and tool activity from any session on demand; the owner accepts that
this context reaches OpenAI.

The read contract prioritizes budget, not censorship:

- visible messages are delivered in full, with defensive limits;
- tool activity initially delivers `tool`, `action`, `target`, status, and
  time, with real arguments compacted into `target` to a maximum of 512 B, but
  without full output. `bash` retains the full command, `fetch_content` the
  full URL, subagents their `task`, and unknown or MCP tools their arguments as
  compact JSON; `action` identifies the tool (with `fetch_content` presented
  as `fetch`);
- a tool's output is explicitly queried with
  `GET /api/sessions/{id}/messages?detail=full&item_id={tool-item-id}` and
  returned as a bounded tail, never unlimited. The same `detail=full` is
  available for subagent transcript tools at
  `GET /api/sessions/{id}/subagents/{jobID}?detail=full&item_id={tool-item-id}`;
- history is retrieved incrementally.

Agent messages are conversational context, not by themselves a verified claim
of state.

The web frontend uses this projection when opening a persisted subagent; Pulse
and future clients must use the additive `action` and `target` fields.

## Actions

Pulse acts directly against Moa's generic routes: send or steer a message,
answer an `ask_user`, decide a permission, create, resume, cancel, or close
sessions. There is no `prepare → review → confirm`: the voice conversation is
the trust context. The model asks only when the target is genuinely ambiguous.

### Attention and permissions

`GET /api/attention` is an informational view of unresolved items across all
sessions, not an approval protocol. For a permission, Pulse can read
`risk_level`, `risk_flags`, and `verbatim` to inform the owner and uses the
item's `session_id` and `ref_id` with the generic permission-decision route.
There is no echo confirmation for the client to complete before that decision.
In particular, `requires_verbatim_confirm` is no longer part of the attention
contract; Moa has no formal API versioning and clients must not depend on that
field.

### Guardian channel

In addition to `GET /api/attention` (informational pull), there is a
Pulse-specific push channel: `GET /api/pulse/guardian/ws`
(`handleGuardianWebSocket` in `pkg/serve/guardian_ws.go`). Unlike Serve's
generic WebSockets, it requires a paired device: a network token or owner
cannot impersonate a revocable handset.

It is a **single active client** channel over the Attention Service
(`pkg/attention`): `SetActiveClient` installs the sink and immediately sends
it an authoritative `init`; a new client displaces the previous one. The
client acknowledges items with `ack` (`AckForClient`) and run terminations with
`ack_termination` (`AckTerminationForClient`), both valid only while it remains
the active client; `get_status` re-requests the snapshot. The sink never blocks
the attention actor: a saturated peer loses the connection and recovers with
the next `init`. This channel is the basis for voice narration and the locked
screen controls of Guardian mode.

## Phases

1. **Server foundation** *(done):* remove legacy Ops/operations, authorize the
   paired device over the generic API, and expose a transcript with tool
   activity.
2. **Usable call** *(done):* Realtime tools, continuous call with VAD,
   reconnection, background audio, and Bluetooth.
3. **Guardian mode v2** *(implemented on the server):* Attention Service with a
   prioritized queue (`pkg/attention`), single-active-client guardian channel
   (`GET /api/pulse/guardian/ws`), session brief (`pkg/pulsebrief`), and the
   narration/termination support that feeds the locked-screen controls.
4. **Visual feed** *(pending):* narrative sessions and conversations using the
   same data sources used by the tools.
5. **CarPlay** *(pending).*
