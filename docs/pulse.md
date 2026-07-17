# Moa Pulse

> Definición de producto canónica: [`PULSE.md`][pulse-spec] del repositorio iOS.
> Este documento resume el contrato de Serve que la hace posible.

[pulse-spec]: https://github.com/e-aleixandre/moa-companion-ios/blob/feat/pulse-openai-realtime/PULSE.md

## Qué es

Pulse es el cliente iOS y futuro cliente CarPlay de `moa serve`: el intermediario
por voz del propietario con todas sus conversaciones. Permite conocer el estado
de las sesiones, leer conversaciones y actividad, y actuar sobre ellas desde
lenguaje natural.

El vertical inicial es una llamada continua y manos libres con OpenAI Realtime;
el feed visual narrativo de sesiones y conversaciones es una fase posterior.

## Límites de arquitectura

- Moa conserva la realidad canónica de sesiones, mensajes, eventos y acciones.
- La API de Moa es genérica y la comparten Serve web y Pulse. No hay una
  proyección, endpoint de operaciones ni WebSocket específicos de Pulse.
- Las únicas piezas Pulse-aware en Moa son el pairing de dispositivos y el
  broker de client secrets Realtime.
- El audio viaja directamente entre iPhone y OpenAI Realtime. Moa no lo recibe,
  no lo proxya y no lo persiste.
- Las tools de Realtime se ejecutan en la app Swift mediante llamadas tipadas a
  la API genérica de Moa; el modelo no recibe la credencial de Moa ni HTTP libre.

## Acceso y contexto

Un dispositivo emparejado representa al propietario y puede usar la superficie
API genérica completa de Serve, salvo administrar pairing. El modelo puede leer
bajo demanda mensajes de usuario/asistente y actividad de tools de cualquier
sesión; el propietario acepta que ese contexto llegue a OpenAI.

El contrato de lectura prioriza presupuesto, no censura:

- los mensajes visibles se entregan completos, con límites defensivos;
- la actividad de tools entrega de inicio `tool`, `action`, `target`, estado y
  tiempo, con argumentos reales compactados en `target` a un máximo de 512 B,
  pero sin salida completa. `bash` conserva el comando completo,
  `fetch_content` la URL completa, los subagentes su `task`, y las tools
  desconocidas o MCP sus argumentos como JSON compacto; `action` identifica la
  tool (con `fetch_content` presentado como `fetch`);
- la salida de una tool se consulta explícitamente con
  `GET /api/sessions/{id}/messages?detail=full&item_id={tool-item-id}` y se
  devuelve como un tail acotado, nunca ilimitado. El mismo `detail=full` está
  disponible para tools de transcripts de subagente en
  `GET /api/sessions/{id}/subagents/{jobID}?detail=full&item_id={tool-item-id}`;
- el historial se recupera incrementalmente.

Los mensajes de agentes son contexto conversacional, no una afirmación
verificada de estado por sí mismos.

El frontend web usa esta proyección al abrir un subagente persistido; Pulse y
futuros clientes deben usar los campos aditivos `action` y `target`.

## Acciones

Pulse actúa directamente contra las rutas genéricas de Moa: enviar o dirigir un
mensaje, responder un `ask_user`, decidir un permiso, crear, retomar, cancelar
o archivar sesiones. No existe `prepare → review → confirm`: la conversación de
voz es el contexto de confianza. El modelo solo pregunta cuando el destino es
realmente ambiguo.

### Attention y permisos

`GET /api/attention` es una vista informativa de los elementos sin resolver de
todas las sesiones, no un protocolo de aprobación. Para un permiso, Pulse puede
leer `risk_level`, `risk_flags` y `verbatim` para informar al propietario y usa
el `session_id` y `ref_id` del elemento con la ruta genérica de decisión de
permisos. No hay una eco-confirmación que el cliente deba completar antes de
esa decisión. En particular, `requires_verbatim_confirm` ya no forma parte del
contrato de attention; Moa no tiene versionado formal de API y los clientes no
deben depender de ese campo.

## Fases

1. **Base servidor:** eliminar Ops/operations heredados, autorizar al
   dispositivo emparejado sobre la API genérica y exponer transcript con
   actividad de tools.
2. **Llamada usable:** tools Realtime, llamada continua con VAD, reconexión,
   audio de fondo y Bluetooth.
3. **Feed visual:** sesiones y conversaciones narrativas con las mismas fuentes
   de datos que usan las tools.
4. **CarPlay.**
