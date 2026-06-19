# Roadmap: loop principal del agente de Atenea

Plan para construir el loop descrito en `docs/atenea-agent-loop.md`. Cada hito es
pequeno, testeable y se ataca con el ciclo TDD de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

Orden por dependencias: cada hito se apoya en el anterior y deja algo verificable
con `go test`. No se toca Wails ni un proveedor real hasta tener el loop verde
contra fakes.

## Principios del plan

- Construir de adentro hacia afuera: tipos -> store -> provider -> publisher ->
  tools -> turno -> loop -> control -> fallos -> Wails.
- Todo lo concurrente (`runTurn`, settle de tools) se prueba con `-race`.
- El proveedor y el store empiezan como fakes en memoria; lo real (Anthropic,
  SQLite) llega al final, cuando el loop ya esta verde.
- Un hito no se cierra sin su tabla `TDD Cycle Evidence`.

## Vista de hitos

```text
M0 Scaffolding
M1 Tipos + Store en memoria
M2 Provider + fake scriptable
M3 Publisher (eventos)
M4 Tool registry + settle
M5 Un turno (runTurn) feliz
M6 Loop externo (Run) + MaxSteps
M7 Senales de control (rebuild / compaction)
M8 Interrupcion + manejo de fallos
M9 Cableado Wails (EventBus + SendPrompt)
M10 Store SQLite + provider real
```

M1..M8 son el corazon y se hacen 100% con tests, sin UI. M9 y M10 cambian
fakes por implementaciones reales sin tocar la logica del loop.

---

## M0 — Scaffolding

**Meta**: estructura de paquetes y dependencias listas.

- Crear `internal/{session,session/runner,llm,tool,event}`.
- `go get golang.org/x/sync/errgroup`.
- Un test trivial por paquete para fijar el nombre de paquete.

**Done when**: `go test ./...` y `go vet ./...` pasan limpio; `gofmt -l .` vacio.

## M1 — Tipos + Store en memoria

**Meta**: backbone durable. `Session`, `Message`, `Seq`, `SessionEvent` y un
`Store` en memoria que agrega eventos y reproyecta mensajes.

- RED: `AppendEvent` asigna `Seq` monotonico y `Messages(sinceSeq)` devuelve en
  orden.
- TRIANGULATE: sesion inexistente; `sinceSeq` mayor que el ultimo; concurrencia
  de appends (`-race`).

**Done when**: el store es la unica fuente de verdad y se puede reconstruir el
historial desde cero.

## M2 — Provider + fake scriptable

**Meta**: interface `Provider` y un fake que emite una secuencia de `llm.Event`
por channel (texto, reasoning, tool-call, step-finish).

- RED: `Stream` devuelve los eventos guionados y **cierra** el channel al
  terminar.
- TRIANGULATE: cancelar `ctx` corta el stream antes de terminar; stream vacio.

**Done when**: el loop podra correr contra escenarios deterministas sin red.

## M3 — Publisher (eventos)

**Meta**: `publish.go` traduce `llm.Event` a `SessionEvent` durables y
bufferiza deltas.

- RED: una secuencia `Text.Started/Delta/Delta/Ended` produce los eventos y un
  evento final con el texto completo concatenado.
- TRIANGULATE: reasoning igual que texto; `step-finish` -> `Step.Ended` con
  tokens; tool input deltas -> `Tool.Input.*`.
- Mantiene `assistantMessageID` del turno y mapa de tool calls por `callID`.

**Done when**: dado un stream del fake, los eventos persistidos coinciden 1:1 con
los nombres del contrato (`Step.* / Text.* / Reasoning.* / Tool.*`).

## M4 — Tool registry + settle

**Meta**: `Registry.Materialize(perms)` devuelve `Definitions` y `Settle`.

- RED: `Settle` de una tool conocida ejecuta y devuelve `Result`.
- TRIANGULATE: tool denegada por permisos no aparece en `Definitions`; tool
  desconocida/stale devuelve error **sin** efectos laterales; output grande se
  acota via `ToolOutputStore`.
- Primer builtin simple (p.ej. `echo` o `read`) para tener algo ejecutable.

**Done when**: el registry valida contra el set anunciado antes de actuar.

## M5 — Un turno (`runTurn`) feliz

**Meta**: ensamblar M1..M4 en un turno: construir `llm.Request` desde historial,
llamar `Stream` una vez, consumir, asentar tools concurrentes con `errgroup`,
devolver `needsContinuation`.

- RED: turno con solo texto -> persiste eventos y devuelve `needsContinuation =
  false`.
- TRIANGULATE: turno con una tool-call local -> registra `Tool.Called` antes de
  ejecutar, publica `Tool.Success`, devuelve `true`; **dos** tool calls corren
  concurrentes y el turno espera a ambas (`-race`); tool falla -> `Tool.Failed`.
- Provider-executed vs local: el provider-executed solo se persiste.

**Done when**: un turno aislado es correcto contra el fake, incluyendo
concurrencia de tools.

## M6 — Loop externo (`Run`) + MaxSteps

**Meta**: el `Inbox` (queue/steer) y el doble loop con `MaxSteps = 25`.

- RED: `Admit(queue)` + `Run` procesa un prompt y queda idle.
- TRIANGULATE: continua mientras haya tool calls; `steer` admitido durante la
  corrida entra en la siguiente continuacion; texto del asistente **no** continua
  solo; segundo `queue` abre nueva actividad; exceder pasos -> `StepLimitExceededError`.

**Done when**: el loop drena el inbox con la misma semantica del pseudocodigo de
la arquitectura.

## M7 — Senales de control

**Meta**: `errRebuildTurn` y `errContinueAfterCompaction` con `errors.Is`, mas
chequeos de `ContextEpoch`.

- RED: si el agente/modelo cambia entre preparar y llamar, el turno reconstruye
  (no llama al provider con request stale).
- TRIANGULATE: mismatch de epoch fuerza rebuild; overflow antes del mensaje del
  asistente compacta y reintenta una vez.

**Done when**: ningun request se ejecuta representando estado viejo de la sesion.

## M8 — Interrupcion + manejo de fallos

**Meta**: cancelacion por `context` y cierre limpio de estado ambiguo.

- RED: cancelar `ctx` a mitad de turno -> tools en vuelo se interrumpen y se
  marca `Tool.Failed`.
- TRIANGULATE: error de proveedor; tool marcada ejecutada por el provider que
  nunca resuelve; `failInterruptedTools` al inicio limpia restos de una corrida
  previa (reanudacion tras crash).

**Done when**: tras cualquier fallo, el historial no queda con tools colgadas.

## M9 — Cableado Wails

**Meta**: conectar el loop a la app real. La logica del loop **no cambia**.

- `internal/event`: `EventBus` reenvia `SessionEvent` con `runtime.EventsEmit`.
- `app.go`: `SendPrompt` hace `Admit(queue)` y arranca `Run` en goroutine;
  boton stop cancela el `ctx`.
- Frontend escucha `session:<id>` y mapea `Step.* / Text.* / Tool.*` en streaming.

**Done when**: un prompt desde la UI produce streaming visible; el runner se
testea contra un `EventBus` fake, no contra Wails.

## M10 — Store SQLite + provider real

**Meta**: cambiar fakes por implementaciones reales detras de las mismas
interfaces.

- `Store` SQLite en el directorio de datos de la app (reanudable tras reinicio).
- Adaptador `Provider` real (Claude / Anthropic) que mapea su stream a
  `llm.Event`.

**Done when**: los tests de M1..M8 siguen verdes con el store real; un prompt
end-to-end funciona contra el proveedor real.

---

## Camino critico

`M1 -> M2 -> M3 -> M5 -> M6` es el minimo para un loop que responde con texto y
tools contra fakes. M4 se necesita antes de M5 para las tool calls. M7 y M8
endurecen el loop. M9 lo hace visible. M10 lo hace real y persistente.

## Como medir avance

Cada hito cerrado deja:

- sus tests verdes (`go test ./...`, con `-race` donde aplica);
- `gofmt -l .` vacio y `go vet ./...` limpio;
- su tabla `TDD Cycle Evidence` en la respuesta/PR.

## Fuentes

- Arquitectura: `docs/atenea-agent-loop.md`
- Manera de trabajo: `AGENTS.md`
- Loop de referencia: `docs/opencode-agent-loop.md`
