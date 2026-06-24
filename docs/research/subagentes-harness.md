# Subagentes: como construir el harness (oh-my-pi y opencode)

Investigado el 2026-06-23 sobre los repos oficiales `can1357/oh-my-pi` (omp) y
`anomalyco/opencode` (antes `sst/opencode`), mas el fork `az9713/oh-my-pi` que
documenta los internals del harness. El objetivo es entender como anaden
subagentes y como trasladar ese patron a Atenea (Go + Wails).

## La idea central (vale para los dos)

Un subagente **no es un agente distinto**: es el **mismo loop del agente corrido
otra vez**, con una terna distinta `(modelo, set de tools, system prompt)` y en
un **contexto aislado** (sesion propia, ventana de contexto propia). No es un
proceso del SO aparte: ambos lo corren **in-process**. El agente padre solo ve
el **resultado final**, no el contexto interno del hijo.

La invocacion es **una tool mas**: el LLM padre llama a una herramienta `task`
(en opencode) / `task` (en omp) con un `subagent_type` y un `prompt`. La
descripcion de esa tool se **genera dinamicamente** a partir del registro de
subagentes disponibles. Opcionalmente el usuario puede invocar uno directo con
`@nombre`.

## Patron comun destilado (los invariantes)

1. **Subagente = loop reentrante.** Reusa el mismo motor; cambia la terna
   `(modelo, tools, prompt)` y la sesion/contexto.
2. **Invocacion via tool `task`.** Parametros minimos: `subagent_type` +
   `prompt`. La descripcion de la tool lista los agentes disponibles.
3. **Aislamiento de contexto.** El hijo arranca una sesion nueva; el padre solo
   recibe el reporte final (ultimo texto o salida estructurada). Es **stateless**:
   el padre no puede mandar follow-ups; el hijo se comunica por un unico reporte.
4. **Control de recursion.** Contador de profundidad; al llegar al maximo se
   **quita la tool `task`** del set del hijo para que no anide infinito.
   - opencode fuerza `task: false` (y tambien `todowrite/todoread: false`).
   - omp: `task.maxRecursionDepth` (default 2); `childDepth = parentDepth + 1`;
     si `atMaxDepth`, se elimina `task`.
5. **Tools y permisos por agente.** Cada definicion declara su subset de tools y
   permisos. Los de solo lectura (`explore`, `plan`) deniegan `edit/write/bash`.
   A los subagentes se les apaga `todo` para no ensuciar la lista del padre.
6. **Definiciones de agente = frontmatter + cuerpo.** Markdown con frontmatter
   (`name`, `description`, `tools`, `model`, `mode`/`spawns`, `thinkingLevel`) y
   el cuerpo como system prompt. El nombre de archivo es el identificador.
   - opencode: `.opencode/agents/*.md` (+ JSON config).
   - omp: defs embebidas (`task`, `quick_task`) y `.md` (`explore`, `plan`,
     `designer`, `reviewer`, `librarian`, `oracle`).
7. **Paralelismo.** El padre puede lanzar varias `task` a la vez; el harness las
   corre con un pool de concurrencia. opencode lo recomienda explicito en la
   descripcion de la tool ("launch multiple agents concurrently whenever
   possible").
8. **Progreso y eventos.** El hijo emite progreso hacia el padre, coalescido
   (omp: 150 ms), con eventos de ciclo de vida por un canal dedicado. Los
   resultados se agregan por indice.
9. **Salida estructurada (opcional pero util).** omp cierra con una tool
   `submit_result`/`yield` validada contra un schema (un fallo da outcome
   `schema_violation`). opencode devuelve el ultimo texto + un `metadata.summary`
   de las tool calls.
10. **Recursos compartidos.** El hijo reusa conexiones MCP, auth y registro de
    modelos del padre. Sin UI, corre en modo "yolo": el llamado a `task` del
    padre **es** la frontera de autorizacion.
11. **Presupuestos y abort.** omp: presupuesto blando de requests (default 90,
    abort a 1.5x), limite de wall-clock (`task.maxRuntimeMs`), razones de abort
    tipadas (`signal | terminate | timeout | budget`).

## opencode — como lo hace

Arquitectura cliente/servidor basada en Effect. Distingue **agentes primarios**
(`build`, `plan`) de **subagentes** (`general`, `explore`, `scout`). Esquema de
agente en `agent.ts` (`Info`): `mode` (`primary` | `subagent` | `all`), `model`,
`prompt`, `permission` (un `Permission.Ruleset`), `steps` (max iteraciones).

La tool `task`:

- Su descripcion se arma dinamicamente con la lista de subagentes no primarios.
- `execute(params)`:
  1. `Agent.get(params.subagent_type)` resuelve el agente.
  2. `Session.create(ctx.sessionID, ...)` crea una **sesion hija** ligada al
     padre (contexto aislado).
  3. `Session.prompt(...)` corre el prompt con `modelID/providerID` del agente,
     tools del agente con `todowrite/todoread/task: false` forzados (corta
     recursion) y el `prompt` del usuario como parte de texto.
  4. Devuelve el ultimo texto como `output` y resume las tool calls en
     `metadata.summary`.
- El loop del hijo es el mismo de cualquier agente: `Session.prompt` ->
  `streamText` (AI SDK), con `stopWhen` controlando el corte.

Permisos: merge profundo de defaults hardcodeados + defaults por agente + config
de usuario. Ejemplos de filtrado por agente: `plan` deniega todos los `edit`
salvo en `.opencode/plans/*.md`; `explore` es solo lectura
(`grep/glob/list/bash/read` + web); `general` deniega `todowrite`. Agentes
ocultos de sistema: `compaction`, `title`, `summary`.

## oh-my-pi (omp) — como lo hace

Monorepo TS + nucleos Rust (`pi-natives`). El loop vive en
`packages/agent/src/agent-loop.ts` (`agentLoop()`), con `Agent.prompt()`
encolando mensajes. Modos de concurrencia de tools: `shared` (paralelo, default)
y `exclusive` (corre sola). Mensajes en cola: **steering** (interrumpe tras el
batch de tools actual) y **follow-up** (continua cuando el agente no tiene mas
trabajo).

Subagentes en `packages/coding-agent/src/task/` (archivos reveladores:
`executor.ts`, `parallel.ts`, `isolation-runner.ts`, `worktree.ts`,
`subprocess-tool-registry.ts`, `agents.ts`, `discovery.ts`, `output-manager.ts`).

- **Spawn** (`runSubprocess(options): Promise<SingleResult>` en `executor.ts`):
  corre el subagente **in-process** en el hilo principal y reenvia eventos. La
  sesion se crea con `createAgentSession(buildSubagentSessionOptions(...))`; el
  `SessionManager` es file-backed (`SessionManager.open`) o
  `SessionManager.inMemory(cwd)`. Settings aislados con `createSubagentSettings`,
  notable `"tools.approvalMode": "yolo"` (no hay UI; el `task` del padre es la
  autorizacion).
- **Profundidad**: `maxRecursionDepth` (default 2); al maximo se quita la tool
  `task`. Tools del hijo derivadas de `agent.tools`; `irc` siempre se anade;
  `exec` se expande en `eval`/`bash`; las del padre como `todo` se filtran.
- **Resultado**: `driveSessionToYield(...)` manda el prompt, espera idle y
  recuerda hacer `yield` (hasta `MAX_YIELD_RETRIES = 3`). El payload se valida
  contra un schema opcional; si falta el yield hay fallback y warnings. Cancelado
  -> rescata el ultimo texto del asistente.
- **Progreso**: `createSubagentRunMonitor(...)` arma un `SubagentRunMonitor` que
  se suscribe a eventos de la sesion (tool counts, tokens, `Usage`) y emite por
  `onProgress` + `EventBus`, coalescido a 150 ms; lifecycle por
  `TASK_SUBAGENT_LIFECYCLE_CHANNEL`.
- **Definicion de agente**:
  ```ts
  interface AgentFrontmatter {
    name: string; description: string;
    tools?: string[]; spawns?: string;       // "*" = puede invocar a cualquiera
    model?: string | string[];
    thinkingLevel?: string; blocking?: boolean;
  }
  ```

**Paralelismo** (`parallel.ts`) — pool de workers, no batches fijos:

```ts
export async function mapWithConcurrencyLimit<T, R>(
  items: T[],
  concurrency: number,
  fn: (item: T, index: number, signal: AbortSignal) => Promise<R>,
  signal?: AbortSignal,
): Promise<ParallelResult<R>>
```

Clamp `max(1, min(concurrency, items.length))`, workers que tiran tareas de un
`nextIndex++` compartido, resultados mapeados por indice. **Fail-fast**: el primer
error dispara un `AbortController` interno (`AbortSignal.any([signal, internal])`)
y se propaga; abort no-error devuelve resultados parciales con `aborted: true`.
Tambien hay un `Semaphore` FIFO para trabajo no precolectado.

**Orquestacion avanzada** (`packages/swarm-extension`): defines un swarm en YAML;
el orquestador arma un DAG desde `waits_for`/`reports_to`, hace **topological
sort** y agrupa en **waves** (misma wave = paralelo; waves en secuencia). Modos:
`pipeline` (repite el grafo `target_count` veces), `sequential` (default),
`parallel`. La comunicacion entre agentes **no** pasa datos por el orquestador:
es por el **filesystem compartido** (signal files, structured output, tracking
files). Archivos: `dag.ts`, `executor.ts` (usa `runSubprocess`), `pipeline.ts`,
`state.ts` (estado en `.swarm_<name>/`). Aparte, soltar la palabra `orchestrate`
mete a omp en un contrato multi-fase de subagentes en paralelo, y la tool `irc`
permite mensajeria entre agentes dentro de una misma corrida.

## Diferencias clave

| Aspecto | opencode | oh-my-pi (omp) |
| --- | --- | --- |
| Base | Effect, cliente/servidor | Monorepo TS + Rust natives |
| Hijo | `Session.create(parent)` | `runSubprocess` in-process |
| Resultado | ultimo texto + `metadata.summary` | `yield`/`submit_result` con schema |
| Recursion | `task: false` en el hijo | quita `task` al `maxRecursionDepth` |
| Aislamiento extra | sesion hija | opcion de **git worktree** por agente |
| Inter-agente | via padre | tool `irc` + swarm (filesystem) |
| Orquestacion | delegacion simple | swarm-extension (DAG/waves en YAML) |

## Integracion en Atenea (mapeo a lo que ya existe)

Atenea ya tiene todas las piezas para esto (no hay que inventar un motor nuevo):

- `internal/session/runner`: el loop (`Run`, `runTurn`), `Inbox` (queue/steer),
  `MaxSteps`. **Un subagente = otro `Run` con su propia sesion.**
- `internal/tool`: `Registry.Materialize(perms) -> Definitions + Settle`,
  `ToolOutputStore`. **La tool `task` es un builtin mas.**
- `internal/session` Store event-sourced (`AppendEvent/Seq/Messages(sinceSeq)`,
  `inMemory`): la sesion hija usa un **Store en memoria propio** = contexto
  aislado, equivalente a `SessionManager.inMemory`.
- `internal/event`: `EventBus` + frontera Wails (`runtime.EventsEmit`). El hijo
  emite a un EventBus hijo; el padre reenvia un subset como progreso.
- `internal/skill`: ya discovery de skills `.md` con cuerpo on-demand. **Reusar
  ese patron** para descubrir definiciones de agente `.md` (frontmatter + body).
- Permisos: `PermissionGate`, `Tool.Permission.Requested`,
  `ResolveToolPermission`. El hijo puede correr con permisos auto (toolset ya
  acotado) o subir el request al gate del padre.

### Diseno propuesto del `task` tool

Un builtin en `internal/tool` cuyo `Settle`:

1. Resuelve la definicion por `subagent_type` (desde un `internal/agent` o
   extendiendo `internal/skill`).
2. Crea una **sesion hija**: nuevo `SessionID`, `Store` en memoria nuevo.
3. Materializa un `Registry` hijo con el subset de tools y permisos del agente;
   **si esta a `maxDepth`, no incluye `task`** (threadear `taskDepth` por
   `context`).
4. Resuelve modelo/provider del agente (default: el del padre).
5. Corre `runner.Run` hasta idle (o hasta un builtin `submit_result` con schema).
6. Devuelve el reporte final como `tool.Result` al padre.

### Lo que sale casi gratis

- **Paralelismo**: el roadmap M5 ya asienta varias tool calls concurrentes con
  `errgroup`. Si el LLM emite N `task` en un step, ya corren en paralelo. Solo
  falta un **cap de concurrencia** (equivalente a `mapWithConcurrencyLimit`).
- **Interrupcion/abort**: M8 ya cancela por `context`; el hijo hereda el `ctx`.
- **Eventos en streaming**: M3/M9 ya traducen `llm.Event -> SessionEvent`. Anadir
  `Task.Started/Progress/Ended` coalescidos para el padre.

### Decisiones a tomar (donde difieren omp y opencode)

- **Salida**: texto libre (opencode) vs `submit_result` con schema (omp). El
  schema es mas testeable y deterministico para Go -> recomendado.
- **Frontera de permisos**: hijo en "yolo" con toolset acotado (omp) vs proxiar
  cada permiso al gate del padre (mas seguro, mas ruido). Empezar con toolset
  acotado + auto-allow dentro de el.
- **Aislamiento de FS**: worktree por agente (omp `worktree.ts`) solo si los
  subagentes escriben en paralelo y chocarian; al principio no hace falta.

## Roadmap TDD propuesto (estilo AGENTS.md)

```text
S0 Tipos: AgentDef (frontmatter+prompt), TaskDepth en context
S1 Registro/discovery de agentes (.md) reusando internal/skill
S2 task tool: un hijo con Store en memoria, Settle devuelve reporte (fakes)
S3 Control de recursion: maxDepth quita task del set del hijo
S4 Tool/permiso por agente: read-only deniega edit/write/bash
S5 submit_result builtin + validacion de schema (schema_violation)
S6 Concurrencia: N task en paralelo con cap (errgroup + limite)
S7 Progreso: Task.Started/Progress/Ended coalescidos al EventBus padre
S8 Abort/budget: cancelacion por ctx, limite de pasos del hijo
S9 Cableado Wails: roster de subagentes visible en UI (como Ctrl+S de omp)
```

Cada hito 100% testeable contra fakes (Provider scriptable + EventBus fake),
con `-race` en lo concurrente (S6), antes de tocar la UI.

## Fuentes

- omp repo: https://github.com/can1357/oh-my-pi
- omp DEVELOPMENT.md: https://github.com/can1357/oh-my-pi/blob/main/packages/coding-agent/DEVELOPMENT.md
- omp `src/task/` (executor, parallel, agents, worktree, isolation-runner)
- omp swarm-extension README: https://github.com/can1357/oh-my-pi/tree/main/packages/swarm-extension
- Fork con internals: https://github.com/az9713/oh-my-pi/blob/main/docs/ARCHITECTURE.md
- opencode repo: https://github.com/anomalyco/opencode
- opencode agent system (DeepWiki): https://deepwiki.com/sst/opencode/3.2-agent-system
- Deep-dive subagentes: https://cefboud.com/posts/coding-agents-internals-opencode-deepdive/
- Issue origen subagentes: https://github.com/sst/opencode/issues/1293
- Contexto interno: `docs/atenea-agent-loop.md`, `docs/atenea-agent-loop-roadmap.md`,
  `docs/opencode-arquitectura.md`
