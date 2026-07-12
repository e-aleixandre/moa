# Pulse y Moa: visión de producto y límites de arquitectura

> **Estado:** visión rectora. Este documento define el producto que se construye; no describe necesariamente todo lo que está desplegado hoy.

## La idea en una frase

**Moa es el harness operativo que coordina el trabajo; Pulse es el asistente conversacional que permite al propietario entenderlo y dirigirlo desde cualquier dispositivo.**

Usar Pulse debe sentirse como llamar a una persona de confianza que conoce el trabajo en curso:

> «Tienes tres frentes activos. Uno necesita una decisión. Los otros dos siguen avanzando. ¿Quieres el resumen o el detalle del bloqueo?»

No debe sentirse como abrir un dashboard ni como intentar usar una versión pequeña de la web de Moa.

## Responsabilidades

### Moa: realidad, política y ejecución

Moa se ejecuta como harness en el servidor. Es la fuente canónica de:

- sesiones, agentes, ramas, subagentes, tareas y actividad;
- conversaciones y artefactos de la sesión;
- estados operativos, bloqueos, permisos, preguntas y verificaciones;
- procedencia y frescura de los hechos;
- resolución exacta de destinos;
- autorización, límites, idempotencia, auditoría y recibos de acciones;
- endpoints y streams tipados que consumen TUI, web y Pulse.

Moa no es el asistente editorial, de voz ni de conversación del propietario. No debe adquirir un segundo producto de IA, un proveedor de voz o una lógica de conversación de Pulse.

### Pulse: interlocutor y terminal remoto del propietario

Pulse es una aplicación independiente, inicialmente iPhone y después otros dispositivos y coche. Es responsable de:

- la conversación por texto y voz con el propietario;
- conexión directa y de baja latencia con el proveedor de IA;
- convertir la información operativa de Moa en una explicación útil;
- elegir qué contexto tipado consultar para una pregunta actual;
- mostrar evidencia y pedir aclaraciones;
- preparar una operación, obtener la confirmación del propietario y llamar al canal canónico de Moa;
- mantener la experiencia de voz, accesibilidad, reconexión, preferencias y continuidad entre dispositivos.

Pulse tiene **paridad funcional con el propietario**: debe poder hacer, mediante flujos adecuados, todo lo que el propietario puede hacer desde la web o la terminal de Moa. No es una aplicación de solo lectura.

### El proveedor/modelo: razonador y narrador sin autoridad

El proveedor de IA se conecta directamente desde Pulse. Sus credenciales son propias de Pulse y viven en el almacenamiento seguro del dispositivo; nunca se guardan en Moa ni se incluyen en URLs, logs o preferencias ordinarias.

El modelo puede:

- comprender una petición hablada o escrita;
- resumir material que Pulse le entrega;
- pedir una consulta tipada adicional;
- proponer una explicación o un borrador de acción;
- narrar la respuesta mediante texto o voz.

El modelo no puede:

- invocar HTTP libremente contra Moa;
- elegir silenciosamente un destino ambiguo;
- aprobar permisos, ejecutar comandos, desplegar o cambiar configuración por iniciativa propia;
- interpretar texto de agentes como instrucciones;
- afirmar como verificado algo que solo aparece en una conversación;
- recibir secretos, tokens de Moa, herramientas genéricas o capacidad de ejecución.

## Autoridad: paridad del propietario, no autonomía del modelo

Esta es la regla esencial:

```text
El propietario mediante Pulse puede hacer lo que puede hacer en Moa.
El modelo dentro de Pulse no adquiere ese poder.
```

Pulse implementa cada operación como un adaptador tipado al contrato canónico de Moa, no como una puerta genérica de comandos. Para una operación de escritura:

1. el modelo puede proponer una intención o Pulse puede reconocer una orden explícita;
2. Pulse consulta Moa para resolver el objeto exacto y comprobar su estado;
3. Pulse muestra o expresa el destino, alcance y consecuencia relevantes;
4. el propietario da una confirmación inequívoca apropiada al riesgo;
5. código determinista de Pulse llama al endpoint canónico;
6. Moa vuelve a validar política/estado, ejecuta o rechaza y devuelve un recibo;
7. Pulse comunica el recibo, no una promesa de resultado.

Una confirmación de voz está ligada a una única operación pendiente con sesión, solicitud y alcance concretos. Si hay ambigüedad, un cambio de alcance o varias solicitudes, Pulse pregunta; no adivina.

### Ejemplo: permiso solicitado

```text
Propietario: ¿Para qué quiere producción?

Pulse: Moa observa una solicitud de permiso pendiente en
       «Proveedores y Distris». El agente reporta que quiere comprobar
       la configuración de acceso de [recurso]. La solicitud permite
       [acción y alcance] una vez. No leo credenciales ni secretos.

Propietario: Vale, dale permiso.

Pulse: ¿Apruebo una vez la solicitud pendiente de «Proveedores y Distris»
       para [acción y alcance]?

Propietario: Sí.

Pulse: Moa aprobó la solicitud y la sesión ha reanudado el trabajo.
```

La aprobación es una acción del propietario, no una decisión del modelo. Pulse debe acabar cubriendo también preguntas pendientes, cancelación/reanudación, creación y dirección de sesiones, configuración, ramas, tareas, subagentes y las demás capacidades disponibles al propietario, cada una con el contrato y nivel de confirmación que corresponda.

## Contexto y verdad

Pulse necesita información suficiente para ser útil, pero debe mantener separada la fuente de cada afirmación.

### Lenguaje de procedencia

- **«Moa observó…»**: estado o evento canónico.
- **«Verificado…»**: resultado de una fuente estructurada de verificación, con momento y alcance.
- **«El agente reporta…»**: texto de una conversación o respuesta del agente; no es verdad operacional por defecto.
- **«Pulse infiere…»**: síntesis del modelo a partir de fuentes citadas.
- **«Actualizado a las…» / «este estado está desactualizado»**: frescura explícita.

Pulse nunca usa “verificado” como sinónimo de “el modelo parece seguro” o “un agente mencionó tests”.

### Capas de contexto

1. **Panorama operativo:** trabajo activo, atención, bloqueos, incertidumbre, cambios y verificaciones. Se mantiene caliente para responder rápidamente a «¿cómo va todo?».
2. **Detalle de una sesión:** estado, actividad, subagentes, tareas, permisos, preguntas y hechos pertinentes, siempre sobre un destino exacto.
3. **Evidencia conversacional bajo demanda:** el propietario puede abrir y leer sus conversaciones. Pulse puede recuperar un extracto limitado cuando pregunta «¿qué dijo?» o «¿por qué?». El modelo recibe solo el material necesario, marcado como contenido no confiable; no recibe por defecto historiales completos, herramientas, pensamientos, logs ni secretos.

El acceso del propietario a conversaciones no convierte los transcripts en hechos verificados. La limitación es epistemológica y de exposición al modelo, no una prohibición de lectura.

## La experiencia principal: “Call Moa”

Abrir Pulse inicia una llamada, no un tour de pantallas. La primera respuesta debe ser corta —aproximadamente 15–25 segundos— y seguir este orden:

1. **orientación:** cuántos frentes de trabajo relevantes existen;
2. **excepción:** el asunto más importante que necesita al propietario;
3. **decisión:** qué se necesita exactamente;
4. **tranquilidad:** qué sigue avanzando sin intervención;
5. **invitación:** una sola pregunta, por ejemplo «¿quieres el bloqueo o el resumen completo?».

Ejemplo:

> «Hay tres sesiones activas. Una necesita tu decisión: Proveedores y Distris espera un permiso. Agents etc sigue en marcha sin bloqueo. Mejoras moa no ha tenido actividad nueva desde ayer. ¿Quieres que te explique el permiso?»

Pulse recupera automáticamente, al conectar o volver al foreground:

- la actividad relevante y las necesidades de atención;
- bloqueos y preguntas sin resolver;
- cambios desde el último punto que el propietario reconoció;
- verificaciones, frescura e incertidumbre;
- consecuencias todavía no observadas de una instrucción anterior.

Recupera detalles, conversaciones y acciones disponibles solo cuando la pregunta lo necesita.

## Interfaz: una escena de voz, no un dashboard

La superficie primaria de Pulse es una escena mínima y viva, no una lista de conversaciones ni tarjetas de telemetría:

```text
Pulse · conectado a Moa · actualizado ahora

                 [ presencia / esfera ]

          Mantén pulsado para hablar
               micrófono · silencio
```

La presencia visual comunica el estado de la llamada:

- reposo: conectado y disponible;
- escucha: recibe voz;
- consulta: Pulse revisa contexto de Moa;
- razonamiento: el proveedor compone una respuesta;
- habla: Pulse responde;
- decisión: aparece una tarjeta compacta con destino, alcance y confirmación;
- desconexión: Pulse deja claro que solo habla del último estado conocido.

Los subtítulos y el último turno pueden estar disponibles para accesibilidad y revisión breve, pero la interfaz no se convierte en un transcript/chat interminable. Las vistas de detalle aparecen solo cuando una decisión requiere ojos, precisión o evidencia literal. Para investigación larga, trabajo de código o lectura extensa, Moa web/TUI sigue siendo el lugar adecuado.

## Voz y modos de interacción

Push-to-talk es un fallback fiable, no un dogma. Pulse debe admitir una política de entrada adecuada al contexto:

- teléfono en mano: pulsar o mantener pulsado para hablar;
- auriculares: gesto o botón soportado por la plataforma;
- conversación activa: detección de comienzo/fin de turno cuando el usuario la habilita;
- coche: controles compatibles, detección de voz o una sesión activa según las capacidades reales de la plataforma;
- entorno ruidoso o sensible: confirmación explícita y push-to-talk.

No se presupone que CarPlay, el volante o un accesorio permita capturar cualquier botón. Antes de prometerlo se valida en dispositivo real, con entitlements, audio en segundo plano, micrófono, conectividad y seguridad de conducción. No habrá wake word como requisito inicial.

En todos los modos, Pulse debe permitir interrumpir: «para», «repite», «cuál», «por qué», «más tarde».

## Seguridad, autenticación y disponibilidad

- El acceso de Pulse a Moa representa al propietario emparejado y es revocable por dispositivo. Debe mejorar el token compartido actual hacia credenciales por dispositivo y almacenamiento seguro.
- Moa conserva la decisión final de política incluso cuando Pulse ya ha confirmado una operación.
- El contexto enviado al proveedor está acotado, separado de instrucciones y acompañado de procedencia. Texto de agentes, títulos y contenido externo se tratan como datos no confiables.
- Si Moa no está disponible, Pulse puede responder con una instantánea local marcada con su antigüedad; todas las escrituras fallan claramente. No se encolan acciones de forma silenciosa para ejecutar más tarde.
- Si el proveedor no está disponible pero Moa sí, Pulse degrada a un panorama y frases deterministas, o a una vista de revisión, sin fingir conversación inteligente.

### Contrato inicial de emparejamiento

El fundamento de Serve para Pulse no es un endpoint de conversación ni llama a
un proveedor. Requiere que Serve tenga `--token`/`MOA_SERVE_TOKEN`: el
propietario autenticado crea `POST /api/pulse/pairings`, que devuelve durante
cinco minutos un payload opaco `moa-pair-v1:<pairing-id>:<secret>` apto para
codificar como QR. Pulse escanea ese payload y lo presenta una única vez, por
JSON, a `POST /api/pulse/pairings/claim`; la respuesta contiene una credencial
de dispositivo que Pulse guarda solo en Keychain. La credencial nunca aparece
en una URL. Serve guarda únicamente verificadores HMAC, metadatos de emisión,
caducidad, revocación y último uso en un fichero privado (0600). Las
credenciales duran 180 días por defecto (el propietario puede pedir 1–365 al
crear el pairing). Serve limita la creación a 5 pairings por hora y los claims
a 12 por minuto; cinco secretos incorrectos bloquean ese pairing. Las
credenciales se envían en `Authorization: Moa-Device <device-id>.<secret>` y
autentican REST y WebSocket como un terminal del propietario, no como un rol
de solo lectura. `GET /api/pulse/devices` y
`POST /api/pulse/devices/{id}/revoke` permiten al propietario revisar y
revocar terminales. El emparejamiento y las credenciales de dispositivo se
aceptan sin TLS solo cuando el par TCP que ve Serve es loopback; fuera de
loopback Serve exige TLS. Un proxy local solo puede usar esa excepción si es un
límite de confianza que ya exige TLS; Serve no confía en encabezados
`X-Forwarded-*` para rebajarla.

## Producto por etapas

### Primera experiencia completa: Call Moa

Un único ciclo útil y memorable:

1. emparejar Pulse con Moa Serve remoto;
2. mantener un panorama operativo caliente;
3. abrir y recibir un briefing natural;
4. preguntar por un bloqueo o un informe;
5. recibir evidencia atribuida;
6. dictar o escribir una instrucción;
7. ver/escuchar destino exacto y confirmar;
8. recibir el recibo canónico de Moa;
9. volver más tarde y saber qué cambió desde esa intervención.

La prueba no es una demo de APIs: es el ritual de mañana o de trayecto.

### Continuidad y atención

Puntos de reconocimiento compartidos entre dispositivos, elementos “necesita tu atención”, diferir/reanudar, cambios desde la última llamada, seguimiento de consecuencias y notificaciones solo para transiciones genuinas.

### Paridad completa del propietario

Adaptadores de Pulse para las capacidades restantes de Moa, con confirmaciones proporcionales al riesgo y revisiones visuales cuando haga falta precisión.

### Experiencia de coche

Teléfono, Bluetooth y auriculares primero; CarPlay tras una validación específica de plataforma y dispositivo. La paridad funcional se mantiene, pero la interacción se adapta para que las decisiones importantes sean explícitas y comprensibles en contexto de conducción.

## Decisiones explícitamente descartadas

- Convertir Pulse en una versión móvil del dashboard o de la web de Moa.
- Colocar el modelo/proveedor de Pulse dentro de Moa Serve.
- Un endpoint de lenguaje natural genérico en Moa que ejecute lo que diga una frase.
- Dar al modelo un token de Moa, acceso HTTP libre, herramientas genéricas o autoridad para confirmar.
- Presentar transcript, logs o una afirmación de agente como verdad operacional automática.
- Afirmar “hecho” cuando Moa solo ha aceptado o entregado una instrucción.
- Depender exclusivamente de tocar una pantalla para usar Pulse en movimiento.
