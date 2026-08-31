# Plan: skills invocables y `/reload`

Dos features que se tocan: si una skill invocable no vive en el system prompt,
crearla no exige recargar nada. Se planifican juntas y se implementan en ese
orden.

Nada está implementado. Lo que hay en la rama `feat/reload` es un primer intento
con un fallo demostrado que se rehará sobre este documento.

---

# Parte 1 — Skills invocables

## El problema

**HECHO**: hoy no hay forma de ejecutar una skill. La lista de `/` es una
constante hardcodeada de 14 comandos
(`pkg/serve/frontend/src/data/composer-suggest.js:10`) y las skills no están.
Solo el modelo decide cargarlas, a partir del índice del system prompt.

Dos consecuencias:

1. El dueño no puede invocar una skill cuando quiere.
2. Toda skill paga espacio en el system prompt aunque se use una vez al mes.

## El estándar ya resuelve esto — se adopta, no se inventa

**HECHO** (docs de Claude Code, que sigue el estándar abierto agentskills.io):
existen dos campos de frontmatter que son exactamente los dos ejes que hacían
falta:

| Campo | Efecto |
|---|---|
| `disable-model-invocation: true` | solo el usuario la invoca; *"removes the skill from Claude's context entirely"* → **no ocupa system prompt** |
| `user-invocable: false` | solo el modelo la carga; no aparece en `/` |
| (ninguno) | ambos pueden invocarla — comportamiento por defecto |

El caso de la skill de "landing de producto" es `disable-model-invocation: true`:
coste cero en tokens, invocable cuando el dueño quiera.

moa ya usa el formato `SKILL.md`, así que adoptar estos nombres nos hace
compatibles con skills escritas para otros harnesses en vez de divergentes.

## Invocar = cargar en contexto (no ejecutar aparte)

**HECHO**: al invocar una skill, el cuerpo del `SKILL.md` entra en la
conversación como un mensaje y **permanece** en los turnos siguientes. No se
re-lee en turnos posteriores.

La ejecución aislada es un modo aparte (`context: fork`): corre en un subagente
sin el historial y devuelve el resultado. **Propuesta: no implementarlo ahora.**
moa ya tiene subagentes; si hace falta, se añade después.

## Argumentos: sí

**HECHO**: `$ARGUMENTS` para el texto completo, `$1`/`$2` por posición, y
argumentos con nombre. Si se pasan argumentos y la skill no tiene ningún
placeholder, se añade `ARGUMENTS: <texto>` al final del contenido.

**Propuesta**: implementar `$ARGUMENTS` y el fallback. Los indexados, después si
se echan en falta.

## Colisión de nombres — la única decisión genuinamente nuestra

**HECHO**: Claude Code hace que **la skill gane** al comando propio.

**Propuesta contraria**: que **el comando gane**. Que una skill llamada `compact`
tape `/compact` es peligroso, y son 14 nombres reservados
(`secret, clear, compact, handoff, model, thinking, permissions, goal, tasks,
verify, undo, path, rename, schedule`). Una skill que colisione aparece en el
menú como `skill:nombre`, y el nombre desnudo sigue siendo el comando.

El prefijo `skill:` no se usa siempre: solo para desambiguar cuando hay choque.
En el menú de `/` se ven ambas, como pidió el dueño.

## Qué hay que construir

1. **Parseo de frontmatter** en `pkg/skill` (**HECHO**: hoy no hay ninguno,
   `pkg/skill/skill.go:38` solo lee nombre, título y descripción del markdown).
2. **`FormatIndex` excluye** las `disable-model-invocation: true`.
3. **El servidor expone las skills invocables al frontend**. **HECHO**: hoy la
   lista de comandos es estática en el bundle → hace falta un endpoint o un campo
   en la info de sesión. Es la fontanería real de esta parte.
4. **El menú de `/`** las muestra junto a los comandos, con su descripción.
5. **Invocar** `/nombre` inserta el cuerpo renderizado en la conversación.

---

# Parte 2 — `/reload`

## Qué queda tras la parte 1

**HECHO**: `skill.Load` hace `os.ReadFile` en cada llamada → editar el cuerpo de
una skill **ya funciona en vivo**.

Con las skills no indexadas, crear una tampoco toca el system prompt. Queda:
`AGENTS.md`, el índice de memorias, y el índice de skills al crear/borrar una
que sí esté indexada.

## El mecanismo, validado contra la API real

**HECHO** (llamadas reales a `api.anthropic.com`, no payloads construidos):

- `system` tras un `assistant` → **400**: *"role 'system' must follow a 'user'
  message or an 'assistant' message ending in a server tool result"*.
- `system` tras un `user` → **aceptado en Sonnet 5 y Opus 5**. Probado con una
  palabra secreta: el modelo responde la del mensaje de sistema, no la del
  historial. **Obedece.**

Esto refuta la objeción de Fable (que la API lo rechazaría) y la de que en
Sonnet sería una mentira funcional. Su desconfianza estaba justificada: la
primera llamada falló de verdad.

**Consecuencia de diseño**: la posición importa — el aviso se inserta tras un
turno de usuario.

## Los dos momentos

1. **Entre compactaciones** → mensaje de sistema. No toca el prefijo. Coste ~0.
2. **Al compactar** → se reconstruye el system prompt desde disco.

**HECHO (medido)**: el coste extra del rebuild al compactar es solo reescribir el
system prompt — `tools` sigue HIT y `messages` se pierde igual con rebuild o sin
él, porque compactar sustituye el historial.

**HECHO**: el aviso **no** sobrevive a la compactación (`renderMessage` solo
trata user/assistant/tool_result/summary), así que el rebuild ahí no es una
optimización sino un requisito.

## Concurrencia

**HECHO**: el autocompact ocurre **dentro** del run (`pkg/agent/loop.go:247`) y
`SetSystemPrompt` **rechaza mientras el agente corre**
(`pkg/agent/agent.go:1109`); el loop usa `cfg.systemPrompt` capturado al
arrancar.

Solución con maquinaria existente: `RefreshBaseSystemPrompt`
(`pkg/bus/runtime.go:393`, que MCP ya usa para lo mismo) ejecutada como
**barrera en la cola** (`pkg/bus/queue_pump.go`), que corre en los puntos idle
donde ya se encolan `/compact` y `/model`.

## Agujeros a cerrar

- **`load_skill` congela el mapa** y solo se registra si había skills al arrancar
  (`pkg/bootstrap/bootstrap.go:474`). El dueño no tiene ninguna: crearía la
  primera y la herramienta no existiría. **Arreglo probado**: resolver del disco
  en cada llamada y registrarla siempre.
- **Subagentes** capturan las fuentes al bootstrap
  (`pkg/subagent/subagent.go:1493`): leer de las mismas fuentes mutables.
- **`BuildBasePrompt` captura las fuentes en una clausura**
  (`pkg/bootstrap/bootstrap.go:576`): hacerlas mutables con lock. Es *el*
  trabajo, no un flanco — mismo bug que el `compact_at` de esta mañana.
- **Idempotencia engañosa** (Fable, correcto): `cmdReload` actualizaba
  `promptSources` tras encolar el aviso; si no surtió efecto, un segundo
  `/reload` diría "nada cambió". Actualizar solo cuando se aplicó.
- Un rol nuevo persistido toca más sitios que los conversores: render del
  frontend, snapshots WS, estimación de tokens.

## Alcance

**Alcance: todas las sesiones (decidido).** `/reload` aplica a **todas** las
sesiones vivas, no solo a aquella donde se escribe. Las tres fuentes son globales
y las memorias las escribe el propio agente, así que la staleness duele sobre
todo en las **otras** sesiones abiertas.

**Una sesión ocupada no falla.** **HECHO** (`pkg/bus/queue_policy.go:14-28`):
hay tres políticas de comando. `/reload` es `PolicyQueue`, como `/compact` y
`/model`: si la sesión está corriendo, se **encola como barrera** y se aplica en
el siguiente punto idle. No interrumpe el run, no corrompe historial, no da
error.

Requisitos derivados:

- El resultado dice qué pasó **en cada sesión**: aplicado, encolado o sin
  cambios. Un "OK" agregado escondería una sesión que no recargó.
- Cada sesión afectada ve en su propio transcript que se recargó, para que la
  configuración nueva no aparezca de la nada.

---

# Orden de trabajo

1. **Skills invocables** — reduce el alcance de `/reload` y es lo que más se echa
   en falta.
2. **Fuentes mutables + `load_skill` del disco** — base compartida.
3. **`/reload`** — mensaje de sistema tras turno de usuario, rebuild al compactar
   como barrera.

Cada parte, su rama y su review.
