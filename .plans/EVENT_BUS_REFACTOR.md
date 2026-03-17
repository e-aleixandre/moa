# Plan: Refactor a Arquitectura Event Bus

**Fecha**: 2026-03-16  
**Objetivo**: Reemplazar el cableado directo TUI↔Agent y Serve↔Agent por un bus de eventos tipado con commands/events, manteniendo la topología actual (terminal = proceso independiente, serve = proceso con multi-sesión + HTTP).

**Principio rector**: El código de dominio (agent loop, tools, permissions, compaction, planmode, subagent, MCP, session, checkpoint, skills) NO se toca. Solo cambia cómo se conecta a los frontends.

---

## Estado Actual (lo que hay que cambiar)

### Cómo el TUI habla con el Agent

El TUI (`pkg/tui/app*.go`) llama directamente a métodos del agent:

```
m.agent.Send(ctx, text)              → ejecutar prompt
m.agent.SendWithContent(ctx, content) → ejecutar con imagen
m.agent.SendWithCustom(ctx, text, m)  → ejecutar con metadata
m.agent.Steer(text)                   → inyectar mensaje mid-run
m.agent.Abort()                       → cancelar run
m.agent.Reconfigure(prov, model, lvl) → cambiar modelo/thinking
m.agent.SetPermissionCheck(fn)        → cambiar permisos
m.agent.SetSystemPrompt(prompt)       → cambiar system prompt
m.agent.Reset()                       → limpiar conversación
m.agent.Compact(ctx)                  → forzar compaction
m.agent.Messages()                    → leer estado
m.agent.Model()                       → leer modelo
m.agent.ThinkingLevel()               → leer thinking
m.agent.CompactionEpoch()             → leer epoch
m.agent.AppendMessage(msg)            → persistir timeline event
```

El TUI recibe eventos via `ag.Subscribe(fn)` → canal `taggedEvent` → `handleAgentEvent()`.

### Cómo Serve habla con el Agent

Serve (`pkg/serve/`) hace lo mismo pero por HTTP:
- `session_lifecycle.go`: `bootstrap.BuildSession()` → crea agent, subscribe → `broadcastAgentEvent()`
- `commands.go`: llama `sess.runtime.agent.Reset()`, `.Compact()`, `.Reconfigure()`, etc.
- `manager.go`: `sess.startRun()` → `agent.Send()` en goroutine
- `broadcastAgentEvent()`: traduce `core.AgentEvent` → `serve.Event` → fan-out a WebSocket subscribers

### Los problemas concretos

1. **`core.AgentEvent` es un struct con 15 campos opcionales** — el Type es un string, los campos son todos opcionales según el tipo. Sin type safety.

2. **Dos traducciones de eventos**: el TUI tiene `handleAgentEvent()` con un switch de 12 cases. Serve tiene `broadcastAgentEvent()` con otro switch de 12 cases. Duplicación.

3. **Commands son llamadas directas** — no hay abstracción. Cada frontend reimplementa la lógica de "qué hacer cuando el usuario cambia de modelo" (`Reconfigure` + rebuild system prompt + persist metadata + update status bar).

4. **Serve tiene sus propios event types** (`serve.Event` con Data tipado) que son mejores que `core.AgentEvent` pero no están compartidos con el TUI.

---

## Arquitectura Objetivo

```
┌──────────────────────────────────────────────┐
│                  pkg/bus                      │
│                                               │
│  Publish(event)     Subscribe[T](fn)          │
│  Execute(command)   OnCommand[T](handler)     │
│                                               │
│  Events:  tipados, un struct por tipo         │
│  Commands: tipados, un struct por tipo        │
│  Transporte: in-process (channels)            │
│  Futuro: WebSocket (opción B)                 │
└──────┬──────────────┬──────────────┬──────────┘
       │              │              │
  ┌────▼───┐    ┌─────▼─────┐  ┌────▼──────┐
  │  TUI   │    │  Serve/WS │  │  Headless │
  │(client)│    │  (client) │  │  (client) │
  └────────┘    └───────────┘  └───────────┘
```

### Bus Contract

**Events** (bus → frontends): notificaciones de cosas que han pasado.
```go
bus.Publish(TextDelta{SessionID: "x", Delta: "hello"})
bus.Publish(ToolExecEnd{SessionID: "x", ToolCallID: "t1", ...})
```

**Commands** (frontends → bus → handlers): peticiones de acciones.
```go
bus.Execute(SendPrompt{SessionID: "x", Text: "hola"})
bus.Execute(SwitchModel{SessionID: "x", ModelSpec: "opus"})
bus.Execute(AbortRun{SessionID: "x"})
```

**Queries** (frontends → bus → handlers): lecturas síncronas.
```go
msgs := bus.Query(GetMessages{SessionID: "x"})
model := bus.Query(GetModel{SessionID: "x"})
```

---

## Fases de Implementación

### Fase 0: Preparación (sin cambios funcionales)

**Objetivo**: Definir los tipos y crear el paquete bus sin conectarlo a nada.

**Ficheros nuevos**:
- `pkg/bus/bus.go` — interfaz Bus, implementación in-process
- `pkg/bus/events.go` — todos los event types
- `pkg/bus/commands.go` — todos los command types
- `pkg/bus/queries.go` — todos los query types

**Events** (migrados de `core.AgentEvent` + `serve.Event`):

```go
// Lifecycle
type AgentStarted struct{ SessionID string }
type AgentEnded struct{ SessionID string; Messages []core.AgentMessage }
type AgentError struct{ SessionID string; Err error }
type TurnStarted struct{ SessionID string }
type TurnEnded struct{ SessionID string }

// Streaming
type TextDelta struct{ SessionID string; Delta string }
type ThinkingDelta struct{ SessionID string; Delta string }
type MessageStarted struct{ SessionID string }
type MessageEnded struct{ SessionID string; Message core.AgentMessage }

// Tool execution
type ToolExecStarted struct{ SessionID string; ToolCallID string; ToolName string; Args map[string]any }
type ToolExecUpdate struct{ SessionID string; ToolCallID string; Content string }
type ToolExecEnded struct{ SessionID string; ToolCallID string; ToolName string; Result string; IsError bool; Rejected bool }

// Compaction
type CompactionStarted struct{ SessionID string }
type CompactionEnded struct{ SessionID string; Payload *core.CompactionPayload; Err error }

// Steering
type Steered struct{ SessionID string; Text string }

// Session state
type StateChanged struct{ SessionID string; State string; Error string }
type RunEnded struct{ SessionID string; FinalText string }
type ConfigChanged struct{ SessionID string; Model string; Thinking string; PermissionMode string; PathScope string }
type ContextUpdated struct{ SessionID string; Percent int }

// Plan mode
type PlanModeChanged struct{ SessionID string; Mode string; PlanFile string }

// Tasks
type TasksUpdated struct{ SessionID string; Tasks any }

// Subagent
type SubagentCountChanged struct{ SessionID string; Count int }
type SubagentCompleted struct{ SessionID string; JobID string; Task string; Status string; Text string }

// Permission
type PermissionRequested struct{ SessionID string; ID string; ToolName string; Args map[string]any }
type PermissionResolved struct{ SessionID string; ID string }

// Ask user
type AskUserRequested struct{ SessionID string; ID string; Questions []askuser.Question }
type AskUserResolved struct{ SessionID string; ID string }
```

**Commands**:

```go
// Agent interaction
type SendPrompt struct{ SessionID string; Text string }
type SendPromptWithContent struct{ SessionID string; Content []core.Content }
type SendPromptWithCustom struct{ SessionID string; Text string; Custom map[string]any }
type SteerAgent struct{ SessionID string; Text string }
type AbortRun struct{ SessionID string }

// Configuration
type SwitchModel struct{ SessionID string; ModelSpec string }
type SetThinking struct{ SessionID string; Level string }
type SetPermissionMode struct{ SessionID string; Mode string }
type SetPathScope struct{ SessionID string; Scope string }
type AddAllowedPath struct{ SessionID string; Path string }
type RemoveAllowedPath struct{ SessionID string; Path string }

// Session management
type ClearSession struct{ SessionID string }
type CompactSession struct{ SessionID string }
type UndoLastTurn struct{ SessionID string }

// Plan mode
type TogglePlanMode struct{ SessionID string }
type ExitPlanMode struct{ SessionID string }
type StartPlanExecution struct{ SessionID string }
type StartPlanReview struct{ SessionID string }
type RefinePlan struct{ SessionID string }

// Tasks
type MarkTaskDone struct{ SessionID string; TaskID int }
type ResetTasks struct{ SessionID string }

// Permissions
type ResolvePermission struct{ SessionID string; PermissionID string; Approved bool; Feedback string }

// Ask user
type ResolveAskUser struct{ SessionID string; AskID string; Answers []string }
```

**Queries**:

```go
type GetMessages struct{ SessionID string } // → []core.AgentMessage
type GetModel struct{ SessionID string }    // → core.Model
type GetThinking struct{ SessionID string } // → string
type GetState struct{ SessionID string }    // → SessionState
type GetContextUsage struct{ SessionID string } // → int (percent)
type GetTasks struct{ SessionID string }    // → []tasks.Task
type GetPlanMode struct{ SessionID string } // → string
type GetCheckpoints struct{ SessionID string } // → []checkpoint.Summary
```

**Bus implementation** (in-process):

```go
type Bus struct {
    // Event subscribers: map[reflect.Type][]func(any)
    // Command handlers: map[reflect.Type]func(any) error
    // Query handlers: map[reflect.Type]func(any) any
}

func (b *Bus) Publish(event any)                      // fan-out async a subscribers
func (b *Bus) Subscribe(fn any) func()                // type-inferred via reflection
func (b *Bus) Execute(command any) error              // dispatch a registered handler
func (b *Bus) Query(query any) any                    // dispatch a registered handler (sync)
```

**Tests**: Unit tests para bus (publish/subscribe, execute/handle, type safety).

**Nada se rompe**: el bus existe pero nadie lo usa todavía.

---

### Fase 1: Command Handlers (dominio escucha commands)

**Objetivo**: Registrar handlers que ejecutan la lógica de dominio en respuesta a commands. Estos handlers encapsulan lo que hoy está disperso entre `app_commands.go`, `app_agent.go`, `app_plan.go`, `serve/commands.go`, y `serve/session_lifecycle.go`.

**Fichero nuevo**:
- `pkg/bus/handlers.go` — registra handlers de commands/queries contra el bus

**Cada handler**:
1. Recibe un command tipado
2. Ejecuta la lógica de dominio (llama al agent, planmode, checkpoint, etc.)
3. Publica events resultantes al bus

**Ejemplo**: `SwitchModel` handler

```go
func handleSwitchModel(ctx *SessionContext, cmd SwitchModel) error {
    newModel, ok := core.ResolveModel(cmd.ModelSpec)
    if !ok {
        return fmt.Errorf("unknown model: %s", cmd.ModelSpec)
    }
    newProvider, err := ctx.ProviderFactory(newModel)
    if err != nil {
        return err
    }
    if err := ctx.Agent.Reconfigure(newProvider, newModel, ctx.Agent.ThinkingLevel()); err != nil {
        return err
    }
    ctx.RebuildSystemPrompt()
    ctx.PersistMetadata()
    ctx.Bus.Publish(ConfigChanged{
        SessionID: cmd.SessionID,
        Model:     newModel.Name,
        Thinking:  ctx.Agent.ThinkingLevel(),
    })
    return nil
}
```

**Ejemplo**: `SendPrompt` handler

```go
func handleSendPrompt(ctx *SessionContext, cmd SendPrompt) error {
    ctx.Bus.Publish(StateChanged{SessionID: cmd.SessionID, State: "running"})
    go func() {
        msgs, err := ctx.Agent.Send(ctx.Ctx, cmd.Text)
        if err != nil {
            ctx.Bus.Publish(AgentError{SessionID: cmd.SessionID, Err: err})
        }
        ctx.Bus.Publish(RunEnded{SessionID: cmd.SessionID, FinalText: extractFinalText(msgs)})
        ctx.Bus.Publish(StateChanged{SessionID: cmd.SessionID, State: "idle"})
    }()
    return nil
}
```

**Agent event bridge**: El Agent sigue emitiendo via su Emitter actual. Un bridge subscriber traduce `core.AgentEvent` → bus events tipados:

```go
agent.Subscribe(func(e core.AgentEvent) {
    switch e.Type {
    case core.AgentEventMessageUpdate:
        if e.AssistantEvent != nil {
            switch e.AssistantEvent.Type {
            case core.ProviderEventTextDelta:
                bus.Publish(TextDelta{SessionID: id, Delta: e.AssistantEvent.Delta})
            case core.ProviderEventThinkingDelta:
                bus.Publish(ThinkingDelta{SessionID: id, Delta: e.AssistantEvent.Delta})
            }
        }
    case core.AgentEventToolExecStart:
        bus.Publish(ToolExecStarted{SessionID: id, ToolCallID: e.ToolCallID, ToolName: e.ToolName, Args: e.Args})
    // ... etc
    }
})
```

Este bridge es **el único punto** que conoce `core.AgentEvent`. Los frontends nunca ven el tipo antiguo.

**SessionContext**: Struct que agrupa las dependencias de una sesión (agent, planmode, checkpoint, taskstore, pathpolicy, etc.). Reemplaza la relación directa TUI→agent y serve→`ManagedSession.runtime`.

**Tests**: Cada handler testeado independientemente con un bus mock.

**Nada se rompe**: los handlers existen y están testeados pero TUI y serve siguen usando el cableado directo.

---

### Fase 2: Migrar Serve a Bus

**Objetivo**: Serve deja de llamar al agent directamente. Usa el bus.

**Análisis de acoplamiento actual** (lo que hay que migrar):

El serve tiene estos puntos de acoplamiento con el agent:

| Punto | Fichero | Descripción |
|-------|---------|-------------|
| `sess.runtime.agent.Send()` | `manager.go:480` | Launch agent run |
| `sess.runtime.agent.SendWithCustom()` | `manager.go:554` | Launch subagent notification run |
| `sess.runtime.agent.Steer()` | `manager.go:445` | Steer running agent |
| `sess.runtime.agent.Reset()` | `commands.go:66` | /clear |
| `sess.runtime.agent.Compact()` | `commands.go:82` | /compact |
| `sess.runtime.agent.Messages()` | `commands.go:86,92` | Read messages post-compact |
| `sess.runtime.agent.Reconfigure()` | `session_config.go:45` | /model, /thinking, PATCH config |
| `sess.runtime.agent.ThinkingLevel()` | `session_config.go:32`, `manager.go:280` | Read thinking level |
| `sess.runtime.agent.LoadState()` | `session_lifecycle.go:349` | Resume session |
| `sess.runtime.agent.Subscribe()` | `session_lifecycle.go:229` | Agent event bridge |
| `sess.broadcastAgentEvent()` | `manager.go:192-262` | Translate core.AgentEvent → WS Event |
| `sess.broadcast()` | 20+ call sites | Fan-out serve.Event to WS subscribers |
| `sess.save()` | `manager.go:507,577`, `session_config.go:60,113` | Persist after mutations |
| `sess.runtime.gate` | `session_config.go:95-115` | Permission mode changes |
| `sess.runtime.taskStore` | `commands.go:168-216` | Task operations |
| `sess.runtime.checkpoints` | `manager.go:470-493` | Undo tracking |
| `sess.runtime.planMode` | `commands.go:124-162` | Plan mode operations |
| `sess.runtime.pathPolicy` | `commands.go:270-342` | Path policy operations |

**Decisión: 3 sub-fases**, de menor a mayor riesgo, cada una compilable y testeable:

---

#### Fase 2a: Bus en ManagedSession + Agent Event Bridge (reemplaza broadcastAgentEvent)

**Objetivo**: Cada `ManagedSession` tiene un `bus.EventBus` y un `bus.SessionContext`. El `broadcastAgentEvent()` desaparece — los agent events llegan al bus y un adaptador los traduce a WS `Event`.

**Cambios**:

1. **`manager.go`**: Añadir `bus EventBus` y `sctx *bus.SessionContext` a `sessionRuntime`.

2. **`session_lifecycle.go`** (`buildManagedSession`):
   - Crear `bus.NewLocalBus()` para la sesión.
   - Crear `bus.SessionContext` con todas las dependencias.
   - Llamar `bus.Bridge(sctx, ag)` en lugar de `ag.Subscribe(broadcastAgentEvent)`.
   - Llamar `bus.RegisterHandlers(sctx)` (registra los handlers de Fase 1).
   - Configurar `SteerFilter` para suprimir subagent completions (reemplaza `subagentTexts` sync.Map).

3. **`manager.go`**: Nuevo `wsAdapter(sess)` que subscriba al bus y traduzca bus events → `sess.broadcast(Event{...})`:
   ```go
   func wsAdapter(sess *ManagedSession) {
       b := sess.runtime.bus
       b.Subscribe(func(e bus.TextDelta) {
           sess.broadcast(Event{Type: "text_delta", Data: DeltaData{Delta: e.Delta}})
       })
       b.Subscribe(func(e bus.ThinkingDelta) {
           sess.broadcast(Event{Type: "thinking_delta", Data: DeltaData{Delta: e.Delta}})
       })
       b.Subscribe(func(e bus.MessageEnded) {
           sess.broadcast(Event{Type: "message_end", Data: MessageEndData{Text: e.FullText}})
       })
       b.Subscribe(func(e bus.ToolExecStarted) {
           sess.broadcast(Event{Type: "tool_start", Data: ToolStartData{
               ToolCallID: e.ToolCallID, ToolName: e.ToolName, Args: e.Args,
           }})
       })
       b.Subscribe(func(e bus.ToolExecUpdate) {
           sess.broadcast(Event{Type: "tool_update", Data: ToolUpdateData{
               ToolCallID: e.ToolCallID, Delta: e.Delta,
           }})
       })
       b.Subscribe(func(e bus.ToolExecEnded) {
           sess.broadcast(Event{Type: "tool_end", Data: ToolEndData{
               ToolCallID: e.ToolCallID, ToolName: e.ToolName,
               IsError: e.IsError, Rejected: e.Rejected, Result: e.Result,
           }})
       })
       b.Subscribe(func(e bus.TasksUpdated) {
           sess.broadcast(Event{Type: "tasks_update", Data: TasksUpdateData{Tasks: e.Tasks}})
       })
       b.Subscribe(func(e bus.Steered) {
           sess.broadcast(Event{Type: "steer", Data: SteerData{Text: e.Text}})
       })
       b.Subscribe(func(e bus.CompactionStarted) {
           sess.broadcast(Event{Type: "compaction_start"})
       })
       b.Subscribe(func(e bus.CompactionEnded) {
           sess.broadcast(Event{Type: "compaction_end"})
       })
       // Lifecycle events (turn_start, turn_end, etc.) — pass through
       b.Subscribe(func(e bus.AgentStarted) { sess.broadcast(Event{Type: "agent_start"}) })
       b.Subscribe(func(e bus.AgentEnded) { sess.broadcast(Event{Type: "agent_end"}) })
       b.Subscribe(func(e bus.TurnStarted) { sess.broadcast(Event{Type: "turn_start"}) })
       b.Subscribe(func(e bus.TurnEnded) { sess.broadcast(Event{Type: "turn_end"}) })
   }
   ```

4. **`manager.go`**: Eliminar `broadcastAgentEvent()` y `subagentTexts` de `sessionRuntime`.

5. **`session_lifecycle.go`**: `Delete()` calls `sess.runtime.bus.Close()`.

**Lo que NO cambia en 2a**:
- `commands.go` sigue llamando `sess.runtime.agent.Reset()`, `.Compact()`, etc. directamente.
- `manager.go` `Send()` sigue llamando `sess.runtime.agent.Send()` directamente.
- `session_config.go` sigue llamando `sess.runtime.agent.Reconfigure()` directamente.
- `server.go` HTTP handlers no cambian.
- `sess.broadcast()` sigue existiendo como mecanismo de fan-out a WS clients.

**Tests**: Todos los tests existentes de serve deben seguir pasando. El WS protocol no cambia (mismo JSON, mismos event types).

**Verificación**: `go test -race ./pkg/serve/...` + `go build ./...`

---

#### Fase 2b: Commands.go → bus.Execute (commands delegados al bus)

**Objetivo**: Los slash commands en `commands.go` delegan al bus en lugar de tocar el agent directamente. La lógica de dominio ya vive en los handlers de Fase 1.

**Cambios**:

1. **`commands.go`**: Cada command handler que tiene equivalente en el bus pasa a delegarle:

   ```go
   func cmdClear(_ *Manager, sess *ManagedSession, _ []string) (*CommandResult, error) {
       if err := requireIdle(sess); err != nil {
           return nil, err
       }
       if err := sess.runtime.bus.Execute(bus.ClearSession{SessionID: sess.ID}); err != nil {
           return &CommandResult{OK: false, Message: err.Error()}, nil
       }
       sess.mu.Lock()
       sess.messages = nil
       sess.mu.Unlock()
       sess.save()
       // CommandExecuted event emitted by handler → wsAdapter broadcasts it
       return &CommandResult{OK: true, Message: "conversation cleared"}, nil
   }
   ```

   Commands migrados:
   - `cmdClear` → `bus.Execute(ClearSession{})`
   - `cmdCompact` → `bus.Execute(CompactSession{})`
   - `cmdUndo` → `bus.Execute(UndoLastChange{})`
   - `cmdTasksDone` → `bus.Execute(MarkTaskDone{})`
   - `cmdTasksReset` → `bus.Execute(ResetTasks{})`

2. **`session_config.go`**: `ReconfigureSession` delegation:
   - Model change → `bus.Execute(SwitchModel{})` 
   - Thinking change → `bus.Execute(SetThinking{})`
   - **Nota**: `ReconfigureSession` actualmente mezcla model + thinking en un solo call, maneja `resolvedModel` update, y hace `save()` + `broadcastContextUpdate()`. Estos side-effects se mantienen en serve (no se mueven al handler) porque son serve-specific (persistence, WS context update).
   - El handler del bus cambia el agent. El serve wrapper añade los side-effects.

3. **`session_config.go`**: `Cancel` → `bus.Execute(AbortRun{})` (ya no necesita acceder a `runCancel` directamente; pero OJO: el actual `Cancel` cancela el context, no solo abort. Puede necesitar ajuste).

4. **WS adapter**: Añadir subscribers para `CommandExecuted` y `ConfigChanged`:
   ```go
   b.Subscribe(func(e bus.CommandExecuted) {
       data := CommandData{Command: e.Command}
       if e.Messages != nil {
           data.Messages = e.Messages
       }
       sess.broadcast(Event{Type: "command", Data: data})
   })
   b.Subscribe(func(e bus.ConfigChanged) {
       sess.broadcast(Event{Type: "config_change", Data: ConfigChangeData{
           Model: e.Model, Thinking: e.Thinking,
           PermissionMode: e.PermissionMode, PathScope: e.PathScope,
       }})
   })
   ```

**Lo que NO cambia en 2b**:
- `manager.go` `Send()` / `startNotificationRun()` — el run lifecycle es complejo (goroutine, checkpoints, state transitions, persistence). Se migra en 2c.
- `cmdPlan` — plan mode commands son complejos (deferred to Fase 2-3 boundary).
- `cmdPermissions` — permission mode management with gate creation/bridge wiring (deferred).
- `cmdPath` — path policy operations (deferred).
- `handleConfig` HTTP endpoint — still calls `ReconfigureSession` and `SetPermissionMode` directly.
- Persistence (`sess.save()`) — still called by serve after bus commands.

**Commands NOT migrated in 2b** (deferred):
| Command | Reason |
|---------|--------|
| `cmdPlan` | Requires EnterPlanMode/ExitPlanMode handlers (system prompt, tool switching) |
| `cmdPermissions` | Requires SetPermissionMode handler (gate creation, bridge wiring) |
| `cmdPath` | Requires path policy handlers (scope change, add/remove paths) |
| `Send` / `startNotificationRun` | Requires SendPrompt handler (run lifecycle, goroutine management) |

**Tests**: All existing serve tests pass. Commands produce same WS events.

**Verificación**: `go test -race ./pkg/serve/...` + `go build ./...`

---

#### Fase 2c: Send/Run lifecycle → bus (el run pasa por el bus)

**Objetivo**: `Manager.Send()` y `startNotificationRun()` delegan al bus. El run lifecycle (goroutine, checkpoint open/close, state transitions, persistence) se centraliza en handlers.

**Prerequisito**: Registrar handlers más complejos que no se hicieron en Fase 1:

1. **Nuevo handler `SendPrompt`** en `pkg/bus/handlers.go`:
   - Necesita: checkpoint Begin/Commit/Discard, run goroutine, state management.
   - El handler NO maneja state transitions (idle→running→idle/error) — eso sigue en serve porque es frontend-specific. El handler se limita a: validate, open checkpoint, call `Agent.Send()`, close checkpoint, publish events.
   - **Diseño**: El handler es síncrono — devuelve inmediatamente después de lanzar la goroutine. El resultado llega como `RunEnded` / `AgentError` events via bus.

   ```go
   b.OnCommand(func(cmd SendPrompt) error {
       // Validate agent is available
       // ...
       go func() {
           sctx.Bus.Publish(AgentStarted{SessionID: sctx.SessionID})
           if sctx.Checkpoints != nil {
               label := cmd.Text
               if len(label) > 60 { label = label[:60] + "…" }
               sctx.Checkpoints.Begin(label)
           }
           msgs, err := sctx.Agent.Send(sctx.SessionCtx, cmd.Text)
           if sctx.Checkpoints != nil {
               if err != nil && sctx.SessionCtx.Err() != nil {
                   sctx.Checkpoints.Discard()
               } else {
                   sctx.Checkpoints.Commit()
               }
           }
           if err != nil {
               sctx.Bus.Publish(AgentError{SessionID: sctx.SessionID, Err: err})
           }
           sctx.Bus.Publish(RunEnded{
               SessionID: sctx.SessionID,
               FinalText: extractFinalText(msgs),
           })
       }()
       return nil
   })
   ```

2. **Serve `Send()` se simplifica**:
   ```go
   func (m *Manager) Send(sessionID, text string) (string, error) {
       sess, ok := m.Get(sessionID)
       if !ok { return "", ErrNotFound }
       
       sess.mu.Lock()
       if sess.State == StateRunning || sess.State == StatePermission {
           sess.mu.Unlock()
           return "steer", sess.runtime.bus.Execute(bus.SteerAgent{Text: text})
       }
       sess.State = StateRunning
       // ... title logic ...
       sess.mu.Unlock()
       
       sess.broadcast(Event{Type: "state_change", Data: StateChangeData{State: "running"}})
       return "send", sess.runtime.bus.Execute(bus.SendPrompt{SessionID: sess.ID, Text: text})
   }
   ```

3. **State transitions via bus events**: Serve subscribes to `RunEnded` and `AgentError`:
   ```go
   b.Subscribe(func(e bus.RunEnded) {
       sess.mu.Lock()
       sess.messages = sess.runtime.sctx.Agent.Messages()
       sess.runCancel = nil
       sess.State = StateIdle
       sess.Error = ""
       sess.Updated = time.Now()
       sess.mu.Unlock()
       sess.save()
       sess.broadcast(Event{Type: "state_change", Data: StateChangeData{State: "idle"}})
       sess.broadcastContextUpdate()
       if e.FinalText != "" {
           sess.broadcast(Event{Type: "run_end", Data: RunEndData{Text: e.FinalText}})
       }
   })
   ```

4. **`startNotificationRun()`**: Same pattern — delegates to `SendPromptWithContent` or `SendPromptWithCustom` handler.

**Nota sobre Cancel/Abort**: Currently `Cancel()` calls `sess.runCancel()` which cancels the run context. The `AbortRun` bus command calls `agent.Abort()`. These are different mechanisms:
- `runCancel()` — cancels the context, run goroutine detects `ctx.Err()` and discards checkpoint
- `agent.Abort()` — signals the agent to stop at next gap

In Fase 2c, `Cancel` should do both: `bus.Execute(AbortRun{})` + cancel the run context. Or the SendPrompt handler manages its own cancelable context and exposes a cancel mechanism. **Decision**: Add `RunCtx context.Context` and `CancelRun context.CancelFunc` to `SessionContext`. The SendPrompt handler uses `RunCtx`. Cancel calls `CancelRun()`.

**Lo que aún NO cambia**:
- `cmdPlan`, `cmdPermissions`, `cmdPath` — deferred to Fase 3 boundary
- Permission/AskUser bridges — deferred (complex state management)
- `handleConfig` HTTP — still calls `ReconfigureSession` wrapper

**Tests**: Full serve test suite passes. Run lifecycle behavior unchanged.

**Verificación**: `go test -race ./pkg/serve/... ./pkg/bus/...` + `go build ./...`

---

#### Resumen Fase 2

| Sub-fase | Alcance | Riesgo | Esfuerzo |
|----------|---------|--------|----------|
| **2a**: Bus + Bridge + WS adapter | Replace broadcastAgentEvent, wire bus per session | Bajo | 0.5-1 día |
| **2b**: Commands → bus.Execute | Delegate slash commands to bus handlers | Bajo-medio | 0.5-1 día |
| **2c**: Send/Run lifecycle → bus | Centralize run goroutine in handler | Medio | 1-1.5 días |
| **Total** | | | **2-3.5 días** |

Cada sub-fase es un commit independiente, compilable, con todos los tests pasando.

---

### Fase 3: Migrar TUI a Bus

**Objetivo**: El TUI deja de tener `m.agent`. Usa el bus.

**Cambios en `pkg/tui/`**:

1. **`app.go`**: `appModel` ya no tiene campo `agent *agent.Agent`. Tiene `bus *bus.Bus` y `sessionID string`.

2. **`app_agent.go`**:
   
   Antes:
   ```go
   func (m appModel) launchAgentSend(text string, gen uint64) tea.Cmd {
       return func() tea.Msg {
           msgs, err := m.agent.Send(m.baseCtx, text)
           return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
       }
   }
   ```

   Después:
   ```go
   func (m appModel) launchAgentSend(text string, gen uint64) tea.Cmd {
       return func() tea.Msg {
           err := m.bus.Execute(SendPrompt{SessionID: m.sessionID, Text: text})
           return agentSendFiredMsg{Err: err, RunGen: gen}
       }
   }
   ```

   El resultado llega como event `RunEnded` a través del bus subscriber, no como return value.

3. **`handleAgentEvent()`** se reemplaza por subscribers tipados:

   Antes:
   ```go
   func (m *appModel) handleAgentEvent(e core.AgentEvent) {
       switch e.Type {
       case core.AgentEventMessageUpdate:
           // ...
       case core.AgentEventToolExecStart:
           // ...
       }
   }
   ```

   Después: Cada event type tiene su propio subscriber registrado al crear el TUI:
   ```go
   bus.Subscribe(func(e TextDelta) {
       eventCh <- tuiEvent{textDelta: &e}
   })
   bus.Subscribe(func(e ToolExecStarted) {
       eventCh <- tuiEvent{toolStart: &e}
   })
   ```

4. **`app_commands.go`**: Todos los slash commands se convierten en `bus.Execute(...)`:

   Antes:
   ```go
   // /model sonnet
   if err := m.agent.Reconfigure(newProvider, newModel, thinkingLevel); err != nil { ... }
   m.rebuildSystemPrompt()
   m.statusBar.UpdateModelSegment(...)
   ```

   Después:
   ```go
   // /model sonnet
   if err := m.bus.Execute(SwitchModel{SessionID: m.sessionID, ModelSpec: "sonnet"}); err != nil { ... }
   // El status bar se actualiza automáticamente via el subscriber de ConfigChanged
   ```

5. **Queries para estado**: Donde el TUI lee `m.agent.Model()`, `m.agent.ThinkingLevel()`, etc., usa queries:
   ```go
   model := m.bus.Query(GetModel{SessionID: m.sessionID}).(core.Model)
   ```

   O alternativamente mantiene un cache local que se actualiza via events (`ConfigChanged`).

6. **`app_plan.go`**: Los toggles de plan mode se convierten en commands (`TogglePlanMode`, `StartPlanExecution`, `RefinePlan`).

**Tests**: Los tests de TUI existentes siguen pasando. Los nuevos tests validan que los commands correctos se emiten en respuesta a input del usuario.

---

### Fase 4: Migrar Headless CLI a Bus

**Objetivo**: El modo headless (`cmd/agent/main.go` con `-p`) usa el bus.

Es el cambio más pequeño. Hoy el headless subscriber es:
```go
ag.Subscribe(func(e core.AgentEvent) {
    switch e.Type {
    case core.AgentEventMessageUpdate:
        fmt.Print(e.AssistantEvent.Delta)
    case core.AgentEventToolExecStart:
        fmt.Fprintf(os.Stderr, "[%s]", e.ToolName)
    }
})
```

Pasa a:
```go
bus.Subscribe(func(e TextDelta) { fmt.Print(e.Delta) })
bus.Subscribe(func(e ToolExecStarted) { fmt.Fprintf(os.Stderr, "[%s]", e.ToolName) })
```

Y `agentRef.Send(ctx, promptContent)` pasa a `bus.Execute(SendPrompt{...})`.

---

### Fase 5: Limpieza

**Objetivo**: Eliminar código muerto y simplificar.

1. **`core.AgentEvent`**: El struct con 15 campos opcionales sigue existiendo porque el agent loop lo usa internamente. Pero es un detalle de implementación del bridge — ningún frontend lo importa.

2. **`serve.broadcastAgentEvent()`**: Eliminado (reemplazado por bus subscribers).

3. **`serve/events.go`**: Los tipos `DeltaData`, `ToolStartData`, etc. pueden migrar a `pkg/bus/events.go` o mantenerse como adaptadores de serialización WebSocket.

4. **`bootstrap.BuildSession`**: Refactorizado para devolver un `SessionContext` + bus pre-configurado en vez del struct `Session` actual con todas las piezas sueltas.

5. **`cmd/agent/main.go`**: Simplificado — ya no cablea channels intermedios para subagent notifications. El bus los gestiona.

---

## Lo que NO cambia

| Paquete | Cambio |
|---------|--------|
| `pkg/core` (types, message, tool, config, models) | ❌ Ninguno |
| `pkg/agent/loop.go` (agent loop, tool scheduler) | ❌ Ninguno |
| `pkg/agent/agent.go` (Agent struct, Send/Steer/Abort) | ❌ Ninguno — el bridge lo wrappea |
| `pkg/agent/emit.go` (Emitter) | ❌ Ninguno — el bridge lo consume |
| `pkg/tool/*` | ❌ Ninguno |
| `pkg/permission/*` | ❌ Ninguno |
| `pkg/compaction/*` | ❌ Ninguno |
| `pkg/checkpoint/*` | ❌ Ninguno |
| `pkg/planmode/*` | ❌ Ninguno |
| `pkg/subagent/*` | ❌ Ninguno |
| `pkg/skill/*` | ❌ Ninguno |
| `pkg/session/*` | ❌ Ninguno |
| `pkg/mcp/*` | ❌ Ninguno |
| `pkg/auth/*` | ❌ Ninguno |
| `pkg/provider/*` | ❌ Ninguno |
| `pkg/verify/*` | ❌ Ninguno |
| `pkg/tasks/*` | ❌ Ninguno |
| `pkg/git/*` | ❌ Ninguno |
| `pkg/askuser/*` | ❌ Ninguno |
| `pkg/prompt/*` | ❌ Ninguno |
| `pkg/context/*` | ❌ Ninguno |
| `pkg/extension/*` | ❌ Ninguno |

---

## Orden y Dependencias

```
Fase 0 (bus types + impl) ✅
   │
   ▼
Fase 1 (command handlers + agent bridge) ✅
   │
   ▼
Fase 2a (bus per session + WS adapter)  ◀── siguiente
   │
   ▼
Fase 2b (commands → bus.Execute)
   │
   ▼
Fase 2c (send/run lifecycle → bus)
   │
   ├──▶ Fase 3 (TUI → bus)
   │
   ├──▶ Fase 4 (headless → bus)
   │
   └──▶ Fase 5 (limpieza)
```

Fase 2 se divide en 3 sub-fases secuenciales (cada una depende de la anterior).
Fases 3, 4 y 5 son independientes entre sí tras completar Fase 2.
La recomendación es TUI (Fase 3) antes que Headless (Fase 4) para validar el patrón en el frontend más complejo.

---

## Estimación

| Fase | Esfuerzo | Riesgo | Estado |
|------|----------|--------|--------|
| Fase 0: Bus types + impl | 1 día | Bajo | ✅ Done |
| Fase 1: Command handlers + bridge | 2-3 días | Bajo | ✅ Done |
| Fase 2a: Bus + Bridge + WS adapter | 0.5-1 día | Bajo | Pendiente |
| Fase 2b: Commands → bus.Execute | 0.5-1 día | Bajo-medio | Pendiente |
| Fase 2c: Send/Run lifecycle → bus | 1-1.5 días | Medio | Pendiente |
| Fase 3: TUI → bus | 3-4 días | Medio-alto (Bubble Tea async model) | Pendiente |
| Fase 4: Headless → bus | 0.5 días | Bajo | Pendiente |
| Fase 5: Limpieza | 1 día | Bajo | Pendiente |
| **Total** | **~10-12 días** | | |

---

## Decisiones de Diseño

### ¿Generics o reflection para type dispatch?

Go 1.25 tiene generics. Usarlos para `Subscribe[T](bus, func(T))` requiere que cada tipo sea un type parameter. Con reflection podemos hacer `bus.Subscribe(func(e TextDelta) {...})` y que el bus infiera el tipo automáticamente. La API es más limpia con reflection, el rendimiento es irrelevante (events son O(100/s) no O(1M/s)).

**Decisión**: Reflection para dispatch, generics para type safety en helpers donde sea posible.

### ¿SessionID en cada event/command?

Sí. Aunque el TUI mono-sesión solo tiene un SessionID, el bus es agnóstico al número de sesiones. Serve necesita SessionID para routear a la sesión correcta. Y prepara el terreno para opción B (terminal conectada a serve).

### ¿El Agent sigue con su Emitter?

Sí. No tocamos `agent.go` ni `loop.go`. El bridge subscriber traduce `core.AgentEvent` → bus events tipados. El Emitter es un detalle de implementación del agent loop.

### ¿El bus es síncrono o asíncrono?

**Events**: asíncronos (fan-out via goroutines o channels bufferizados, como el Emitter actual).  
**Commands**: síncronos (el caller espera el resultado). `SendPrompt` es una excepción — el handler lanza una goroutine y devuelve inmediatamente, el resultado llega como event.  
**Queries**: síncronos (lectura directa).

### ¿Transporte abstraído?

El bus tiene una interfaz. La implementación in-process usa channels de Go. Una futura implementación WebSocket implementaría la misma interfaz, serializando events/commands como JSON. El TUI no sabría la diferencia.

```go
type EventBus interface {
    Publish(event any)
    Subscribe(handler any) func()    // func(ConcreteType)
    Execute(command any) error
    Query(query any) any
}
```

---

## Consideración Futura: Opción B (Terminal → Serve)

Con este bus, la opción B (terminal conecta al serve existente) requiere:

1. Al arrancar `moa`, comprobar si hay lockfile con puerto de serve
2. Si existe → crear bus WebSocket que conecta al serve
3. Si no existe → crear bus in-process (standalone)
4. El TUI no cambia — usa la interfaz `EventBus` en ambos casos

Esto es un `if` de 20 líneas en `cmd/agent/main.go`. No requiere cambios en el bus, los handlers, el TUI, ni serve.

---

## Criterios de Éxito

1. ✅ Todos los tests existentes (64 ficheros) pasan sin modificación
2. ✅ El TUI no importa `pkg/agent` directamente
3. ✅ Serve no importa `pkg/agent` directamente (excepto bootstrap)  
4. ✅ Ningún frontend ve `core.AgentEvent`
5. ✅ Añadir un nuevo event/command es: definir tipo + registrar handler
6. ✅ La lógica de "cambiar modelo" existe en UN solo lugar (handler), no en TUI + serve + headless
