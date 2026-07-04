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
  el log estandar a un archivo temporal (no pintar sobre la pantalla alternativa)
  y corre `tea.NewProgram` con alt-screen. Sin logica testeable propia.
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
- `internal/tui/model.go` + `fold.go` + `view.go` — el Model de Bubble Tea.
  `fold.go` proyecta los `SessionEvent` durables a entradas de conversacion
  (texto assistant en streaming, mensajes user, tool calls con estado, permisos
  pendientes, errores); `model.go` maneja teclado y la bomba de eventos del
  canal; `view.go` renderiza con viewport de alto acotado (sigue la cola,
  PgUp/PgDn), linea de estado de trabajo, la caja del composer con borde
  redondeado y el pie con agente/modelo, todo con estilos lipgloss.
- `internal/wiring` — el ensamblado compartido extraido de `app.go`: registry de
  tools, skills y slash-commands, catalogo de subagentes con el gate propagado,
  system prompts (normal/plan/local) y el runner configurado. `App.wire` y
  `NewEngine` lo consumen; un cambio de tools/skills llega a ambos frontends.

## Contratos que la TUI fija con tests

- El fold es puro: los deltas de texto acumulan en un bloque vivo y `Step.Ended`
  cierra sin duplicar contra el Message coalescido; reasoning y tool-input no
  son transcript.
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
- El viewport respeta el alto de la terminal, sigue la cola con eventos nuevos y
  sobrevive terminales minusculas (0x0/1 linea: dimensiones acotadas a >= 0;
  panic real de bubbles/viewport encontrado en el smoke E2E bajo pty).
- La caja del composer mide el ancho de la terminal y nunca crece de 3 lineas
  (un prompt mas largo que el ancho scrollea horizontal dentro del input); el
  pie muestra `<agente> · <modelo>`: el modelo entra una sola vez via
  `WithStatus` y el agente refleja el modo activo (build/plan).

## Correr

```bash
go build -o build/bin/atenea-tui ./cmd/atenea-tui
./build/bin/atenea-tui          # demo sin red si no hay OPENROUTER_API_KEY
OPENROUTER_API_KEY=... ./build/bin/atenea-tui
```

## Pendientes conocidos (v1)

- Store en memoria: las sesiones de la TUI no persisten ni aparecen en la
  sidebar de la app (compartir el SQLite exige coordinar acceso concurrente).
- Plan-mode ya se alterna con Tab, pero sigue pendiente cambiar el MODELO desde
  la TUI (tampoco hay slash-commands ni @-menu en el composer): el pie muestra
  el modelo del entorno, fijo por corrida. Tambien falta el flujo AcceptPlan
  (aprobar un plan presentado y ejecutarlo promoviendo el prompt de
  implementacion); hoy es manual: Tab a build y enviar.
- El indicador de trabajo es estatico (sin animacion de spinner).
- Un prompt nuevo mientras corre una actividad cancela la corrida anterior
  (mismo comportamiento que la app Wails hoy).
