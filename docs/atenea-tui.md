# atenea-tui: la interfaz de terminal

`atenea-tui` es el segundo frontend del agente: una TUI estilo Claude Code que
corre en la terminal. Reutiliza el MISMO loop del agente que la app Wails (el
runner, las tools, el ask-before-run, las skills y los subagentes); lo unico que
cambia es la frontera de presentacion.

```
wails app:  runner -> EmittingStore -> Bus -> EmitFunc(runtime.EventsEmit) -> frontend web
atenea-tui: runner -> EmittingStore -> Bus -> EmitFunc(chan tea.Msg)       -> Model Bubble Tea
```

## Piezas

- `cmd/atenea-tui/main.go` — la frontera delgada (equivalente al `main.go` de
  Wails): carga `.env`, elige el provider desde el entorno (`OPENROUTER_API_KEY`
  presente = OpenRouter con `OPENROUTER_MODEL`; ausente = demo sin red), desvia
  el log estandar a un archivo temporal (no pintar sobre la pantalla alternativa),
  abre el SQLite COMPARTIDO con la app via `session.OpenDefault` (fallback en
  memoria si falla, con `Close` al salir) y corre `tea.NewProgram` con
  alt-screen. Sin logica testeable propia.
- `internal/tui/engine.go` — el ensamblado headless del agente. Arma
  inbox/gate/snapshots en memoria, decora el store con `EmittingStore` sobre un
  `event.Bus` cuya `EmitFunc` puentea cada `session.SessionEvent` al canal de la
  TUI, y delega el cableado del runner en `wiring.Build` (la misma fuente de
  verdad que la app). Guarda el modo por sesion (`modes` + `modeFor`, el hook
  `Mode` de `wiring.Build` que el runner consulta cada turno): `SendPrompt`
  fija modo normal y `SendPlanPrompt` fija plan-mode antes de encolar, espejo
  de `App.SendPrompt`/`App.SendPlanPrompt`. Ambos admiten en el inbox y corren
  `Run` en una goroutine cancelable por sesion (espejo de `App.start`); al
  terminar publica `RunDoneMsg`. Satisface la interface `Agent` del Model.
- `internal/tui/model.go` + `fold.go` + `view.go` + `reveal.go` — el Model de
  Bubble Tea. `fold.go` proyecta los `SessionEvent` durables a entradas de
  conversacion (texto assistant en streaming, bloques de pensamiento
  colapsables, mensajes user, tool calls con estado, permisos pendientes,
  errores); `model.go` maneja teclado y la bomba de eventos del canal;
  `view.go` renderiza con viewport de alto acotado (sigue la cola, PgUp/PgDn),
  linea de estado de trabajo, la caja del composer con borde redondeado y el
  pie con agente/modelo, todo con estilos lipgloss; `reveal.go` es el smooth
  streaming del texto que llega por deltas, assistant y pensamiento (paridad
  con `frontend/src/lib/reveal.ts`): la vista revela un prefijo por runas que
  avanza con un loop de ticks, con catch-up proporcional al backlog.
- `internal/wiring` — el ensamblado compartido extraido de `app.go`: registry de
  tools, skills y slash-commands, catalogo de subagentes con el gate propagado,
  system prompts (normal/plan/local) y el runner configurado. `App.wire` y
  `NewEngine` lo consumen; un cambio de tools/skills llega a ambos frontends.

## Contratos que la TUI fija con tests

- El fold es puro: los deltas de texto acumulan en un bloque vivo y `Step.Ended`
  cierra sin duplicar contra el Message coalescido; tool-input no es transcript.
- El reasoning folda a un bloque de pensamiento propio (paridad con el
  ThinkingBlock del escritorio): mientras fluye muestra la cabecera
  `[pensando]` y las ultimas 4 lineas no vacias del texto revelado (ventana
  deslizante, con el mismo smooth reveal del assistant); cerrado y drenado
  colapsa a la linea `[penso <duracion>]`. `Text.Started` y `Step.Ended`
  cierran defensivamente un pensamiento que siga en vivo (el runner puede no
  emitir `Reasoning.Ended`).
- `Tool.Permission.Requested` muestra la solicitud y se limpia con el
  `Tool.Success`/`Tool.Failed` del mismo `CallID` (no hay evento de resolucion).
  La tecla y/n resuelve via el gate con el `SessionID` del EVENTO (una solicitud
  surfaceada de un subagente se resuelve con el id del hijo).
- Enter envia por el camino del modo activo (`Agent.SendPrompt` en build,
  `Agent.SendPlanPrompt` en plan); Ctrl+C corta y sale; Esc solo corta;
  `RunDoneMsg` apaga el indicador de trabajo.
- Tab alterna el modo del agente build/plan: es pegajoso entre envios (cada
  Enter rutea por el camino del modo activo, sin resetearlo) e inerte con un
  permiso pendiente, y el pie del composer lo refleja en vivo. En plan-mode el
  runner anuncia `present_plan` sin `bash`/`write`; el siguiente `SendPrompt`
  devuelve la sesion a modo normal.
- Un `present_plan` exitoso agrega al final la oferta `[plan] plan presentado
  (y ejecutar / n seguir en plan)`; con la oferta pendiente el teclado no
  alimenta el input. `y` acepta via `Agent.AcceptPlan` (el Engine vuelve la
  sesion a modo normal y promueve el prompt fijo de implementacion, espejo de
  `App.AcceptPlan`), apaga el plan-mode y marca la corrida como trabajando;
  `n` descarta la oferta y el modo queda como esta. Un `present_plan` fallido
  no ofrece nada.
- El viewport respeta el alto de la terminal, sigue la cola con eventos nuevos y
  sobrevive terminales minusculas (0x0/1 linea: dimensiones acotadas a >= 0;
  panic real de bubbles/viewport encontrado en el smoke E2E bajo pty).
- La caja del composer mide el ancho de la terminal y nunca crece de 3 lineas
  (un prompt mas largo que el ancho scrollea horizontal dentro del input); el
  pie muestra `<agente> · <modelo>`: el modelo entra una sola vez via
  `WithStatus` y el agente refleja el modo activo (build/plan).
- Con el composer vacio, `Space` arma un leader de un segundo y `Space e` abre
  o cierra el panel `explorer`. El panel lista el workspace como arbol con
  iconos Nerd Font; `j`/Down y `k`/Up mueven el cursor, `l`/Enter expande una
  carpeta o abre un archivo en el visor, `h` colapsa o sube al padre, y
  Esc/`q` cierran sin insertar. Mientras el explorer esta abierto sus teclas
  no llegan al composer; permisos y aprobacion de plan conservan prioridad.
- El explorer ocupa una columna izquierda acotada y transcript, menus y
  composer se recalculan al ancho restante. Si `listFiles` falla o el workspace
  esta vacio, el panel sigue siendo usable y muestra el estado sin panic.

### Visor de archivos

- `Enter` sobre un archivo abre una vista de solo lectura en el area principal;
  no agrega `@ruta` ni cierra el explorer.
- `Esc` vuelve al chat y conserva cursor y scroll del explorer.
- `j`/Down, `k`/Up, PgUp y PgDn desplazan el archivo. La vista muestra ruta,
  numeros de linea y resaltado cuando Chroma reconoce el lenguaje.
- No permite editar ni guardar. Binarios, archivos mayores de 1 MiB, vacios o
  errores de lectura muestran un estado explicito.

## Persistencia compartida con la app

La TUI abre el MISMO SQLite que la app Wails via `session.OpenDefault`: la
ruta la resuelve `session.DefaultDBPath` (`ATENEA_DB` si esta seteada; si no
`<config>/atenea/atenea.db`). Los pragmas WAL + busy_timeout (por conexion,
via DSN) permiten los dos procesos a la vez sobre el mismo archivo: lectores
concurrentes y un escritor que espera el write-lock en vez de fallar con
SQLITE_BUSY; el Seq por sesion se asigna en un INSERT atomico (subquery
MAX(seq)+1 con RETURNING), asi dos procesos no racean la secuencia. Cada
sesion de la TUI graba `Session.Cwd` en su primer prompt y aparece en la
sidebar de la app agrupada por esa carpeta. La app se refresca sola: un
watcher sondea el `PRAGMA data_version` del store (cambia solo con escrituras
de OTRA conexion) y emite `sessions:changed`, y el frontend re-pide
`ListSessions` al recibirlo. Si abrir el SQLite falla, `OpenDefault` devuelve
un store en memoria usable junto al error: la TUI sigue funcionando, solo que
sin persistir.

## Correr

```bash
go build -o build/bin/atenea-tui ./cmd/atenea-tui
./build/bin/atenea-tui          # demo sin red si no hay OPENROUTER_API_KEY
OPENROUTER_API_KEY=... ./build/bin/atenea-tui
```

## Pendientes conocidos (v1)

- Plan-mode ya se alterna con Tab, el flujo AcceptPlan ya ejecuta el plan
  aprobado y el composer ya autocompleta slash-commands y @-archivos, pero
  sigue pendiente cambiar el MODELO desde la TUI: el pie muestra el modelo del
  entorno, fijo por corrida.
- Un prompt nuevo mientras corre una actividad cancela la corrida anterior
  (mismo comportamiento que la app Wails hoy).
