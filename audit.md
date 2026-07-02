# Auditoria de estado — atenea

> Fecha: 2026-06-27
> Metodo: 10 agentes en paralelo, un area cada uno, **solo lectura** (no se modifico
> codigo). Un subconjunto de hallazgos de severidad alta fue verificado leyendo el
> codigo directamente (marcados con `[verificado]`).
> Alcance: backend Go (`app.go`, `git.go`, `terminal.go`, `internal/...`) + frontend
> Vue/TS (`frontend/src/...`).

`atenea` es una app de escritorio Wails (Go + Vue/TS): un harness de agente de IA para
coding (loop de agente, tools de archivo/bash, subagentes, skills, sesiones
persistidas, terminal pty, integracion git, UI de chat).

---

## 1. Red de seguridad (estado real hoy)

| Gate | Comando | Resultado |
|---|---|---|
| Suite Go | `go test ./...` | OK, todos los paquetes pasan |
| Formato/estatica | `gofmt -l .` / `go vet ./...` | limpio |
| Suite frontend | `npm test` (Vitest) | OK, 42 archivos, 399 tests |

**Veredicto general:** proyecto maduro y sano en el camino feliz, sin blockers de
build ni de test. Los tests verdes NO cubren concurrencia real, bordes de red /
seguridad, ni paths no triviales: ahi viven los problemas. No hay blocker de
compilacion, pero si varios riesgos altos: un crash latente, cuelgues, corrupcion
silenciosa de edits, y dos bypass de controles de seguridad.

### Conteo aproximado de hallazgos
- Alta: ~13
- Media: ~20
- Baja: ~18
- Info: ~15

---

## 2. Hallazgos verificados a mano

Estos 7 hallazgos de severidad alta se confirmaron leyendo el codigo real, no solo
por reporte del agente:

1. `ListCommands` lee `a.commands` sin lock (`app.go:595`) — accessor `currentCommands()` existe y se saltea.
2. Subagente ejecuta bash sin gate — `subagent.go:164` hace `NewRunner` sin `SetPermissionGate` (lo hallaron 2 agentes).
3. WebFetch SSRF por redirect — `webfetch.go:51` crea el client sin `CheckRedirect`; `checkSSRF` solo valida el host inicial.
4. Cliente OpenAI sin timeout — `openai.go:43` sin `WithRequestTimeout`; `git.go:167` y `app.go:452` usan `context.Background()`.
5. `ApplyEdits` no valida rangos — `apply.go` no chequea `Range.End`/`Anchor <= len`; `insertsAt[99]` nunca se emite (contenido perdido).
6. Git renames/paths unicode rompen el diff — `git.go:80` deja `Path = "viejo -> nuevo"` literal; el propio comentario `git.go:63` lo admite como MVP.
7. Data race sobre `Snapshot.Seen` — `turn.go:160` corre tools en paralelo; `patcher.go` lee `snap.Seen` sin lock mientras `RecordSeenLines` escribe bajo lock (panic `concurrent map read and map write`).

---

## 3. Lo mas importante (severidad alta), agrupado por impacto

### Seguridad — controles que se pueden evadir
- **Subagentes ejecutan bash SIN gate de permisos** `[verificado]`. El `ask-before-run`
  solo se cablea en el runner principal (`app.go:162`). El runner hijo
  (`subagent.go:164`) nunca recibe `SetPermissionGate`, asi que `r.gate==nil` y
  `turn.go:166` no pide aprobacion. El modelo puede hacer `task -> subagente "general"
  -> bash` con comando arbitrario sin confirmacion del usuario. Derrota por completo
  el control del chat principal.
- **WebFetch: bypass SSRF por redirect / DNS-rebinding** `[verificado]`. `checkSSRF`
  valida solo el host inicial y el client (`webfetch.go:51`) no define `CheckRedirect`
  (sigue hasta 10 saltos sin re-validar). Una URL publica puede responder `302` hacia
  `169.254.169.254` (metadata cloud) o `127.0.0.1`.

### Crash / cuelgues
- **Data race sobre `Snapshot.Seen` -> panic `concurrent map read and map write`**
  `[verificado]`. El runner corre cada tool-call del turno en su goroutine
  (`turn.go:160`); el Patcher lee `snap.Seen` fuera de lock mientras `RecordSeenLines`
  lo escribe bajo lock. Si el modelo emite en un mismo turno `read+edit` o `grep+edit`
  sobre el mismo archivo, el proceso paniquea y muere. Es lo mas cercano a un blocker
  real.
- **Cliente OpenAI sin timeout -> turnos colgados** `[verificado]`. `NewOpenAIProvider`
  (`openai.go:43`) sin `WithRequestTimeout`. Peor: titulo y mensaje de commit usan
  `context.Background()` sin deadline (`git.go:167`, `app.go:452`). Un SSE colgado deja
  la goroutine viva para siempre con el body abierto.

### Corrupcion / perdida de datos
- **`ApplyEdits` no valida rangos/anclas -> contenido descartado en silencio**
  `[verificado]`. Sin chequeo de `Range.End`/`Anchor <= len(lines)`. Un `INS.POST 99`
  sobre 3 lineas cae en `insertsAt[99]`, que el loop de emision nunca recorre: la
  insercion desaparece sin error. Un `SWAP` con `End` enorme trunca la cola del archivo.
- **`DeleteSession` no espera la corrida en vuelo -> la sesion borrada resucita**.
  `App.DeleteSession` cancela y borra, pero no espera la goroutine de `Run`; un
  `AppendEvent` tardio re-crea la sesion con un log parcial que reaparece en la sidebar.

### Funcionalidad rota
- **Git: archivos renombrados rompen el diff** `[verificado]`. `gitStatus` deja
  `Path = "viejo -> nuevo"` literal; al abrir el diff `os.ReadFile` sobre ese path
  inexistente falla y el side-by-side nunca abre.
- **Git: paths con espacios / acentos / unicode rompen status->diff** `[verificado]`.
  `git status --porcelain` cita y escapa esos paths; se reenvian con comillas a
  `git diff`/`ReadFile` y fallan. Critico para el publico de esta app (proyectos en
  espanol con nombres acentuados). Fix: `--porcelain -z` (o `core.quotepath=false`) y
  partir por NUL resuelve renames y unicode juntos.

### Concurrencia
- **`ListCommands` lee `a.commands` sin mutex** `[verificado]` — data race contra
  `SetWorkspace`; `currentCommands()` existe justo para esto (`app.go:595`).
- **`shutdown` incompleto** — `OnShutdown` solo cierra terminales: no cancela runs, no
  espera goroutines, no cierra SQLite -> escrituras a medias / db sin flush limpio.
- **Posible orden invalido assistant<->tool en el turno** — el `Message` del assistant
  (con `tool_calls`) y el `Tool.Success` se publican desde goroutines distintas sin
  orden garantizado; con tool rapida/local el `Seq` del tool result puede quedar antes
  del assistant -> la API rechaza el historial (HTTP 400).
- **`context.Canceled` se muestra como error en la UI** — Stop / cambio de workspace /
  follow-up cancelan la corrida y el `context.Canceled` crudo se pinta como fallo rojo.
- **Frontend: race en `loadSession`** — `await SessionHistory(id)` sin re-chequear que
  `sessionID.value===id` tras el await; doble clic A->B mezcla el historial de dos chats.

> El **steering del loop es codigo muerto** tal como lo cablea la app hoy: enviar un
> segundo mensaje mientras el agente trabaja aborta el turno en vez de encolarlo/
> steerearlo (se pierde trabajo). `run.go` implementa steer pero `SendPrompt` siempre
> cancela.

---

## 4. Hallazgos detallados por area

Formato de cada hallazgo: **titulo** — `severidad` / `categoria` — `ubicacion`.
Descripcion y recomendacion debajo.

### Area 1 — Wails app boundary y ciclo de vida
Archivos: `app.go`, `main.go`, `dotenv.go`, `terminal.go`, `wails.json`, bindings
generados en `frontend/wailsjs/`.
Proposito: puente Go<->JS; expone bindings que la UI invoca, arranca/cierra la app,
construye el wiring del agente anclado al workspace, reenvia eventos via `EventsEmit`,
carga secretos de `.env` en dev.
Estado: media-alta. Wiring mutable publicado bajo `mu`, gate de permisos correcto,
errores propagados sin tragar. Gaps reales acotados.

- **ListCommands lee a.commands sin el mutex (data race vs SetWorkspace)** — `high` /
  `concurrency` — `app.go:595` (vs accessor `app.go:193-197`, swap `app.go:170-175`).
  `[verificado]`. Devuelve `a.commands.List()` directo mientras el resto usa
  `currentCommands()` bajo `a.mu` y `wire()` reemplaza el puntero con el lock tomado.
  Fix: `return a.currentCommands().List(), nil`.
- **shutdown no cancela runs, no espera goroutines, ni cierra SQLite** — `high` /
  `error-handling` — `terminal.go:14`, `app.go:317-324`, `app.go:645-656`. `OnShutdown`
  solo hace `term.CloseAll()`. Goroutines de `Run` siguen escribiendo en el store; la
  conexion SQLite (`Close()` en `sqlitestore.go:78`) nunca se cierra. Fix: cancelar
  runs, `wg.Wait()` acotado, cerrar el store.
- **a.ctx se escribe en startup y se lee en EmitFunc desde goroutines sin sincronizar**
  — `medium` / `concurrency` — `app.go:370`, `app.go:292-294`, `terminal.go:42`.
  Benigno por orden temporal, pero campo compartido sin barrera. Fix: proteger con
  `a.mu`/atomic.
- **FileDiff arma diff de archivo nuevo con filepath.Join(root, path) sin validar
  traversal** — `medium` / `security` — `git.go:108-145`, expuesto `git.go:189`.
  `os.ReadFile(filepath.Join(root, path))` sin `Clean` ni chequeo de pertenencia a
  root. Fix: validar con `filepath.Rel` que siga bajo root.
- **loadDotEnv muta el entorno global con os.Setenv sin validar claves** — `low` /
  `security` — `dotenv.go:51-62`, `main.go:17`. Hereda a todos los subprocesos
  (git, pty, bash). `.env` en un dir no confiable puede alterar `PATH`. Fix: allowlist
  de claves; rechazar `PATH`/`LD_*`/`DYLD_*`.
- **StartPty/ResizePty no validan cols/rows (0 -> pty de tamano cero)** — `low` /
  `behavior` — `terminal.go:39-45`, `terminal.go:57-62`. Fix: normalizar a 80x24 si <1.
- **Gaps de tests: SetWorkspace concurrente con ListCommands, shutdown, dotenv parser**
  — `medium` / `missing-test` — `app_commands_test.go`, `app_workspace_test.go`,
  `dotenv_test.go`. Fix: test `-race` concurrente; test de shutdown; endurecer dotenv.
- **SetWorkspace cancela runs pero no las espera antes de recablear (TOCTOU benigno
  documentado)** — `info` / `concurrency` — `app.go:493-504`, `app.go:527-533`.
  Intencional (ponytail). Sin accion requerida.

### Area 2 — Session runner / loop del agente
Archivos: `internal/session/runner/{runner,run,turn,publish}.go`,
`internal/session/{session,epoch,mode,event}.go`.
Proposito: loop central de turnos; drena el inbox, arma cada Request desde el historial
durable, llama al Provider, traduce el stream a SessionEvents durables, asienta tool
calls en paralelo (errgroup), decide continuacion/limite/abort/epoch/modo.
Estado: bien estructurado, buena cobertura (incluido -race) en caminos felices. Riesgos
en ordenamiento concurrente y en la interaccion app<->loop alrededor del abort.

- **Orden invalido entre el Message del assistant (Step.Ended) y el Tool.Success del
  turno** — `high` / `concurrency` — `turn.go:142-210`, `publish.go:62-90` y `169-187`.
  El candado del Publisher serializa appends pero NO impone orden; una tool rapida/local
  puede persistir su `Tool.Success` con `Seq` menor que el assistant. La proyeccion
  ordena por `Seq` -> emite el tool result antes del assistant con `tool_calls` ->
  API rechaza (HTTP 400). Los tests solo afirman `Tool.Called < Tool.Success`, nunca
  `Step.Ended < Tool.Success`. Fix: persistir el assistant antes de cualquier
  Tool.Success del turno; test que afirme el orden con tool instantanea.
- **Run devuelve context.Canceled crudo en cada abort y la app lo muestra como error**
  — `high` / `ux` — `turn.go:194-208`, `run.go:73-76`, `app.go:649-651`, `bus.go:32-37`,
  `chat.ts:389-392`. Cancelacion deliberada presentada como fallo. Fix: tratar
  `context.Canceled`/`DeadlineExceeded` como cierre limpio (`errors.Is`).
- **start relanza un Run nuevo sin esperar al viejo: dos loops solapados duplican
  Tool.Failed** — `medium` / `concurrency` — `app.go:634-656`, `run.go:106-126`,
  `turn.go:200-208`. La vieja sigue escribiendo en cleanup (`WithoutCancel`); la nueva
  lee `PendingToolCalls` y puede escribir un segundo `Tool.Failed` para el mismo callID
  -> dos `Message{Role:tool}` -> API rechaza. Fix: serializar corridas por sesion;
  insercion idempotente por callID.
- **Goroutines de settle sin supervisar si Publish falla a mitad (no se llama g.Wait)**
  — `medium` / `concurrency` — `turn.go:154-156`, `turn.go:160-189`. `return false, err`
  sin `g.Wait()`; goroutines lanzadas (bash sleep) siguen y escriben post-retorno. Fix:
  cancelar y drenar el grupo antes de retornar.
- **Error no-ctx del PermissionGate deja la tool sin resolver y el turno continua** —
  `medium` / `error-handling` — `turn.go:170-177` y `194-208`. Si `Ask` falla por causa
  != cancelacion, retorna nil sin asentar; queda `Tool.Called` colgado y el loop encadena
  otro turno. Fix: publicar `Tool.Failed` ante fallo no-ctx del gate.
- **El loop nunca usa DeliverySteer: el follow-up cancela en vez de steerear (steer es
  codigo muerto)** — `medium` / `behavior` — `app.go:395-404`, `app.go:538-548`,
  `run.go:39,59,77,80-83`. Enviar un segundo mensaje aborta el turno en curso. Fix:
  decidir contrato (steer vs encolar) y no abortar por input nuevo.
- **El retry loop de runTurn no acota rebuilds ni chequea ctx: riesgo de spin con epoch
  real (M10)** — `info` / `maintainability` — `turn.go:56-67`, `epoch.go:17-22`,
  `memstore.go:168-176`. Hoy inerte; con el driver real podria girar. Fix: cota de
  reintentos + `ctx.Err()` al tope del for.
- **failInterruptedTools traga ErrSessionNotFound pero el flujo asume historial
  proyectable** — `info` / `missing-test` — `run.go:106-126`, `run.go:38-64`. Arista
  fragil (force sin input). Fix: cubrir con test el contrato.

### Area 3 — Proveedor LLM (OpenAI-compatible)
Archivos: `internal/llm/{openai,provider,tool,fake,doc}.go`.
Proposito: frontera con el modelo via SSE OpenAI-compatible (OpenRouter); parsea deltas
texto/razonamiento/tool_calls a `llm.Event` bracketeados, ensambla tool calls por index,
mapea historial/roles/system, reporta fallos como `StepFailed`.
Estado: razonablemente solido, bien testeado en camino feliz. Riesgos en bordes que los
tests no tocan.

- **Cliente OpenAI sin timeout: un turno puede colgarse indefinidamente** — `high` /
  `concurrency` — `openai.go:43-49`. `[verificado]`. Sin `WithRequestTimeout`; los
  callers de titulo/commit usan `context.Background()` (`app.go:452`, `git.go:167`).
  Fix: timeout por request y/o `context.WithTimeout` en esos callers; read-timeout de
  stream.
- **Fallo de stream a mitad de tool call deja el bracket ToolInput abierto** — `medium` /
  `error-handling` — `openai.go:218-231`. Si el stream falla entre args, se emite
  `StepFailed` y `return` sin recorrer `order`: la UI recibio `ToolInputStarted`/`Delta`
  pero nunca `ToolInputEnded`/`ToolCall`. Fix: emitir `ToolInputEnded` por bracket
  abierto antes de `StepFailed`, o documentar cierre en la UI; test con SSE cortado.
- **Tool call cuyo primer delta llega sin id produce CallID vacio y nunca emite
  ToolInputStarted** — `medium` / `bug` — `openai.go:131-159`, `223-231`. Gate
  `tc.ID != ""`; algunos gateways mandan args antes del id -> deltas descartados y
  `CallID` vacio que rompe el round-trip. Fix: usar el index como clave de bracket, no
  el id.
- **Errores de json.Unmarshal silenciados al mapear schema de tools y reasoning** —
  `low` / `error-handling` — `openai.go:326-329`, `253-263`. Tool sin parameters si el
  schema no parsea (`if err == nil`). Fix: loggear el fallo de schema.
- **El campo top-level 'reasoning' se envia a TODO endpoint, incluido OpenAI puro** —
  `low` / `behavior` — `openai.go:82-84`. Campo propio de OpenRouter hardcodeado; puede
  dar 400 en endpoints estrictos. Fix: condicional por flag/base URL.
- **API key se pasa en claro sin redaccion; riesgo de fuga en logs de error del SDK** —
  `low` / `security` — `openai.go:43-49`, `app.go:357-363`. `StepFailed` vuelca
  `err.Error()` crudo a un log durable. Fix: sanitizar/redactar antes de persistir.
- **Gap de tests: cancelacion de ctx a mitad de stream y usage ausente** — `info` /
  `missing-test` — `openai_test.go`. Fix: tests httptest que cancelen ctx, omitan usage,
  corten el SSE en tool args.

### Area 4 — Herramientas de archivo + hashline diff/patch
Archivos: `internal/tool/{read,write,edit,glob,grep,ripgrep,path}.go`,
`internal/tool/hashline/`.
Proposito: lectura/escritura/edicion con sandbox de rutas y motor de parches "hashline"
anclado a `[ruta#HASH]` y a lineas vistas (`Seen`).
Estado: diseno anti-drift bien pensado, pero el aplicador no valida limites, el hash es
debil, hay race sobre `Seen`, y las escrituras no son atomicas.

- **ApplyEdits no valida rangos/anclas contra el tamano: contenido descartado en
  silencio** — `high` / `bug` — `apply.go:44-75`, `patcher.go:100-103`. `[verificado]`.
  Insercion con ancla > len cae en `insertsAt[pos]` con pos>len y nunca se emite (verif.
  empirico: `INS.POST 99` sobre 3 lineas se descarta sin error). Fix: validar
  `Range.End <= len`, `1 <= Anchor <= len`; error accionable.
- **SWAP con End mayor al fin del archivo trunca la cola sin avisar** — `medium` / `bug`
  — `apply.go:28-36,67-75`. `SWAP 2.=999` sobre 3 lineas -> `a\nX` (perdio lineas
  reales). Fix: rechazar `Range.End > len`.
- **Carrera de datos sobre Snapshot.Seen: el Patcher lee el mapa sin lock** — `high` /
  `concurrency` — `patcher.go:67,95` + `firstUnseenAnchoredLine:147-165`,
  `snapshot.go:72-105`, `turn.go:157-189`. `[verificado]`. `ByHash` devuelve el puntero
  al `*Snapshot` vivo; itera `snap.Seen` fuera del mutex mientras `RecordSeenLines`
  escribe `Seen` bajo el mutex -> `concurrent map read and map write` (panic). Alcanzable
  con `read+edit` o `grep+edit` del mismo archivo en un turno. Fix: copia defensiva de
  `Seen` o consulta via metodo con lock.
- **Hash de ancla CRC32 truncado a 16 bits: colision trivial (demostrada)** — `medium` /
  `bug` — `hash.go:25-28`. 65536 valores; colision real hallada en muestra chica. Un edit
  anclado podria aplicarse sobre contenido equivocado sin disparar el guard. Fix:
  ampliar a 32+ bits.
- **Escrituras no atomicas y sin preservar el modo (pierde bit ejecutable; corrupcion
  ante fallo a mitad)** — `medium` / `bug` — `patcher.go:20-22,111`, `write.go:28-30,116`.
  `os.WriteFile` directo con perm fija 0644. Fix: temporal + `os.Rename`; `os.Stat` para
  preservar `FileMode`.
- **read entra en bucle improductivo si una sola linea supera MaxBytes** — `medium` /
  `behavior` — `read.go:173-191,149-163`. Devuelve `:from` apuntando a la misma linea;
  el modelo nunca avanza. Fix: emitir la linea truncada con notice o avanzar.
- **Replace/Insert solapados se aplican sin error y con resultado sorprendente** — `low`
  / `behavior` — `apply.go:26-62`. `SWAP 1.=2 +X` + `SWAP 2.=3 +Y` -> `X\nY\nd` sin
  aviso. Fix: detectar solapamiento y devolver error.
- **grep busca dentro de archivos ocultos (--hidden), exponiendo .env/credenciales** —
  `low` / `security` — `ripgrep.go:218,222`. Diverge de glob (que respeta `.gitignore`).
  Fix: politica consistente; respetar `.gitignore` por defecto.
- **El selector ':' del read parte por el ULTIMO ':' y no soporta rutas con ':'** —
  `info` / `behavior` — `read.go:74-88`. Documentado como limitacion v1. Fix futuro:
  selector como parametro JSON aparte.
- **Snapshots por sesion crecen sin limite (sin Invalidate ni poda)** — `info` /
  `performance` — `snapshot.go:28-57`, `snapshots.go:23-46`. Cada version guarda el
  archivo entero; fuga lenta. Fix: acotar historial por path; liberar el store al cerrar
  la sesion.

### Area 5 — Herramientas de ejecucion y externas
Archivos: `internal/tool/{bash,bash_unix,bash_other,webfetch,skill,todo,present_plan,
echo,registry,output,snapshots}.go`, `internal/terminal/`, `terminal.go`.
Proposito: ejecucion de shell (kill de grupo, scrub de secretos), webfetch destilado con
guard SSRF, carga de skills, todos, plan, registro de tools con permisos, acotado de
output, terminal pty.
Estado: solido en general; dos agujeros de seguridad reales (subagente bash sin gate,
SSRF por redirect) y zombies de pty.

- **Los subagentes ejecutan bash sin el permission gate** — `high` / `security` —
  `subagent.go:164`, `app.go:162`, `builtins.go:24`. `[verificado]`. El runner hijo no
  recibe `SetPermissionGate`; el subagente "general" trae bash. Via directa de escalada.
  Fix: propagar gate+needsApproval al runner hijo; test que verifique
  `Tool.Permission.Requested`.
- **WebFetch: bypass de SSRF via redirect y DNS rebinding** — `high` / `security` —
  `webfetch.go:51-58`, `94-101`, `137-159`. `[verificado]`. Client sin `CheckRedirect`;
  `checkSSRF` solo valida el host inicial. `302 -> 169.254.169.254`/`127.0.0.1` pasa.
  Fix: `CheckRedirect` que re-valide cada salto, o `net.Dialer.Control` que valide la IP
  real; test con `302` a metadata.
- **La terminal pty nunca reapea el shell (cmd.Wait ausente): fuga de zombies** —
  `medium` / `concurrency` — `session.go:23-46,58-63`. `Close` hace `Kill`+`f.Close`
  pero nadie llama `cmd.Wait()`. Abrir/cerrar tabs acumula zombies. Fix: `go cmd.Wait()`
  tras Kill o reapear en la goroutine de lectura.
- **Scrub de secretos en bash depende del nombre; claves sin SECRET/TOKEN/PASSWORD/
  API_KEY se filtran** — `medium` / `security` — `bash.go:150-174`. `AWS_ACCESS_KEY_ID`,
  `DATABASE_URL`, `GH_PAT`, etc. quedan legibles. Fix: allowlist de env.
- **Buffer de salida de bash sin tope durante la ejecucion (OOM antes del acotado)** —
  `medium` / `performance` — `bash.go:102-114,179-188`. `bytes.Buffer` ilimitado hasta
  que `Run` retorna; cap post-hoc. Fix: writer acotado (ring buffer head+tail).
- **Doble acotado de bash y luego OutputStore.Cap con limites distintos** — `low` /
  `maintainability` — `bash.go:179-188`, `registry.go:131`, `output.go:26-34`. El segundo
  corte es head-only por bytes y parte UTF-8. Fix: unificar; corte seguro en frontera de
  runa.
- **listSkillFiles emite rutas ABSOLUTAS del host al modelo** — `low` / `security` —
  `skill.go:99-115`. Filtra estructura del filesystem (home, usuario). Fix: rutas
  relativas al dir base.
- **La terminal pty no se abre en el workspace seleccionado** — `low` / `behavior` —
  `session.go:24`, `terminal.go:39-45`. Sin `cmd.Dir`; arranca en el cwd de lanzamiento.
  Fix: pasar el workspace root como `cmd.Dir`.
- **WebFetch sube http->https silenciosamente; URLs solo-http fallan en vez de avisar** —
  `info` / `behavior` — `webfetch.go:117-131`. Intencional. Opcional: mensaje de error
  mas claro.

### Area 6 — Persistencia de sesion, permisos y event bus
Archivos: `internal/session/{sqlitestore,memstore,store,inbox,permission}.go`,
`internal/event/{bus,store}.go`.
Proposito: log de eventos por sesion (event-sourcing) sobre SQLite/memoria con
proyecciones; inbox (queue/steer FIFO); gate ask-before-run; event bus que reenvia al
frontend por canal `session:<id>`.
Estado: solido y bien testeado (contrato compartido, Seq monotonico). Huecos en bordes
de concurrencia/robustez.

- **DeleteSession no espera la corrida en vuelo: un append tardio resucita la sesion** —
  `high` / `concurrency` — `app.go:609-618`, `sqlitestore.go:432-449`, `app.go:646-655`.
  El INSERT posterior re-crea la sesion (reaparece en la sidebar con log parcial). Fix:
  esperar el fin de la corrida antes de borrar, o tombstone que rechace appends.
- **SQLite sin busy_timeout ni WAL: correcto intra-proceso, fragil con otro abridor** —
  `medium` / `concurrency` — `sqlitestore.go:48-75`, `app.go:317-342`. Una segunda
  instancia que abra `atenea.db` fallara con `SQLITE_BUSY` sin retry. Fix: anadir
  `busy_timeout` y `journal_mode=WAL` al DSN.
- **MemoryPermissionGate.Ask sobrescribe un Ask previo del mismo (sessionID,callID) y
  filtra su canal/goroutine** — `medium` / `concurrency` — `permission.go:56-81,87-98`.
  El segundo Ask pisa el canal del primero; el primero queda colgado. Fix: detectar
  colision y expulsar/errorear el viejo.
- **Sessions() ordena por MAX(rowid), que SQLite reutiliza tras DeleteSession** — `low` /
  `behavior` — `sqlitestore.go:224-271`. Tras borrar las filas de mayor rowid, una nueva
  sesion puede recibir rowids reciclados y aparecer mas abajo. Fix: ordenar por
  `MAX(seq)` + timestamp, o `AUTOINCREMENT`/`created_at`.
- **Bus.PublishError serializa err.Error() perdiendo el tipo (StepLimitExceededError)** —
  `low` / `ux` — `bus.go:32-37`, `app.go:649-651`. La UI no puede distinguir limite de
  pasos de fallo de proveedor. Fix: payload estructurado `{code, message}`.
- **EmittingStore solo serializa AppendEvent; proyecciones sin candado (correcto pero no
  testeado con SQLite real)** — `info` / `concurrency` — `store.go:31-78`. Correcto por
  `MaxOpenConns(1)`. Opcional: test concurrente bajo -race.

### Area 7 — Subagentes, agentes, skills, comandos y prompt
Archivos: `internal/session/subagent/subagent.go`, `internal/agent/`, `internal/skill/`,
`internal/command/command.go`, `internal/session/prompt/prompt.go`.
Proposito: descubrir y ensamblar agentes/skills/comandos; TaskTool levanta subagentes
hijos (runner aislado) con cap de profundidad/concurrencia; `prompt.Build` ensambla el
system prompt.
Estado: bien disenado y testeado (cap de recursion correcto, duplicados deterministas,
fallos de parseo aislados). Riesgo serio: el gate no se propaga al hijo.

- **El subagente ejecuta bash sin el permission gate del padre (ask-before-run bypass)**
  — `high` / `security` — `subagent.go:164`, `app.go:149,159,162`. `[verificado]`. Mismo
  hallazgo que Area 5 (lo encontraron 2 agentes). Fix: `r.SetPermissionGate(parentGate,
  parentNeedsApproval)` en el child runner; inyectar el gate en `NewTaskTool`.
- **Un subagente personalizado sin campo 'tools' queda sin ninguna herramienta, en
  silencio** — `medium` / `behavior` — `subagent.go:150-153`, `agent.go:58-64`. `Parse`
  no aplica default a Tools. Fix: rechazar/avisar en Discover, o default de solo lectura.
- **Nombres de skill/agente con espacios rompen el slash-command derivado** — `low` /
  `behavior` — `command.go:35-45,106-124`, `skill.go:73-78`. `parse` corta el nombre en
  el primer espacio -> comando ininvocable. Fix: validar/slugificar nombres.
- **Cap de concurrencia compartido podria interbloquear si se permite anidar 'task' via
  registry** — `low` / `concurrency` — `subagent.go:47,78-95,141-144`. Hoy seguro (child
  registry no registra `task`). Fix futuro: semaforo por nivel o liberar slot al esperar.
- **El reporte del subagente es el ultimo texto del asistente: puede salir vacio** —
  `low` / `error-handling` — `subagent.go:179-185`. Si el ultimo turno fue tool-call,
  `report` queda vacio sin senal. Fix: Output explicito cuando no hay texto.
- **Parse de frontmatter: el cuerpo se corta en el primer '\n---'** — `info` / `behavior`
  — `skill.go:34-39`, `agent.go:37-42`. Fragil si el frontmatter contiene `---`. Fix
  futuro: exigir `---` a inicio de linea, o parser YAML real.
- **Fallos al cargar skills/agentes se loguean pero se tragan** — `info` /
  `error-handling` — `skill.go:113-114`, `agent.go:101-102`, `app.go:122-124,136-137`.
  El autor no ve por que falta una skill. Fix: reportar a un panel de diagnostico.

### Area 8 — Integracion Git (backend + frontend)
Archivos: `git.go`, `frontend/src/stores/git.ts`, `frontend/src/lib/diff.ts`,
`DiffScreen.vue`, `DiffView.vue`.
Proposito: estado de git (porcelain), diffs unificados por archivo (staged/working/nuevo),
commit con mensaje del modelo, render side-by-side estilo VSCode.
Estado: nucleo bueno (separa staged/unstaged/untracked, sanitiza XSS con DOMPurify, sin
repo no es error). Huecos en paths no triviales que git devuelve.

- **Los archivos renombrados se parsean con 'viejo -> nuevo' y rompen el diff** — `high`
  / `bug` — `git.go:80`, `git.go:108-116`, `git.go:122-124`. `[verificado]`. `R orig ->
  renamed` deja `Path` literal; el diff cae a `newFileDiff` con `os.ReadFile` de un path
  inexistente. Fix: partir por ` -> `, quedarse con el path nuevo.
- **Paths con espacios, unicode o caracteres especiales llegan citados/escapados** —
  `high` / `bug` — `git.go:64-93`, `git.go:108-124`. `[verificado]`. `--porcelain` cita
  y escapa (octal) los paths no-ASCII; el path con comillas no matchea `git diff` ni
  `ReadFile`. Critico para proyectos en espanol. Fix: `--porcelain -z` o
  `core.quotepath=false`, partir por NUL.
- **Archivos binarios: el side-by-side queda vacio o renderiza bytes crudos como
  adiciones** — `medium` / `behavior` — `git.go:108-145`, `diff.ts:69-123`,
  `DiffScreen.vue:87-91`. `git diff` de binario no trae `@@`; `buildSideBySide` devuelve
  0 filas ("Sin cambios"). Untracked vuelca bytes crudos como `+`. Fix: detectar binario
  (NUL/utf8.Valid) y emitir placeholder.
- **loadStatus no limpia error en exito: un fallo previo queda pegado en el panel** —
  `low` / `ux` — `git.ts:41-48`. Fix: `error.value=''` al inicio del try.
- **openDiff no setea estado de carga; un FileDiff lento congela la interaccion** — `low`
  / `ux` — `git.ts:109-122`. Fix opcional: flag `loadingDiff`, o acotar tamano del diff.
- **GitStatus/FileDiff no testeadas a nivel App con SetWorkspace concurrente** — `info` /
  `missing-test` — `git.go:185-212`, `app.go:178-183`. Cambio de carpeta entre listar y
  abrir -> diff vacio/error. Fix opcional: pasar root junto al path.
- **El regex de hunk en buildSideBySide tolera el sufijo de funcion pero conviene fijarlo**
  — `info` / `maintainability` — `diff.ts:97-99`. Sin bug. Opcional: test con sufijo de
  funcion tras el segundo `@@`.

### Area 9 — Estado frontend: store de chat y sesiones
Archivos: `frontend/src/stores/{chat,tabs,ui}.ts`, `frontend/src/lib/{sessions,
contextWindow,mcps,terminalSession}.ts`.
Proposito: traduce eventos durables de Wails (`session:<id>`) a un log reactivo de items;
gestiona suscripcion, persistencia de workspace, sidebar, tabs, registro de terminales
pty fuera del arbol de componentes.
Estado: maduro y muy bien testeado. Riesgos restantes de concurrencia entre acciones
async del usuario.

- **Race en loadSession: el replay async puede contaminar la sesion equivocada** —
  `high` / `concurrency` — `chat.ts:544-560`. Sin guarda de que `sessionID.value===id`
  tras `await SessionHistory(id)`; doble clic A->B mezcla historiales en el mismo
  `items.value`. Fix: `if (sessionID.value !== id) return` antes del replay; test async.
- **Estado optimista (running=true) puede ser pisado por un evento tardio del turno
  anterior** — `medium` / `concurrency` — `chat.ts:562-578,349-366`. Un `Step.Ended`
  tardio apaga `running` justo tras encenderlo. Fix: correlacionar `running` con un id de
  turno/step.
- **attach() async sin guarda de desmontaje puede arrancar/cablear un pty tras unmount**
  — `medium` / `concurrency` — `terminalSession.ts:45-62`, `TerminalPanel.vue:14-24`.
  `detach` corre mientras `attach` sigue en vuelo. Fix: re-validar `container.isConnected`
  tras el await, o flag `mounted`.
- **mcpIcon construye SVG con entry.accent/name sin escapar: XSS latente si el catalogo
  deja de ser estatico** — `low` / `security` — `mcps.ts:70-85`. Hoy estatico/confiable.
  Fix: al pasar a dinamico, validar accent (hex) y XML-escape del texto.
- **Step.Failed/applyError no apagan los punteros de streaming: item queda 'streaming'
  para siempre** — `low` / `behavior` — `chat.ts:363-366,389-392,222-224`. Cursor de
  "escribiendo" colgado; el siguiente Delta se anexa al item viejo. Fix: cerrar streaming
  en vuelo en `Step.Failed`.
- **loadSessions/loadWorkspace/loadModel ultimo-en-resolver gana sin guarda** — `low` /
  `concurrency` — `chat.ts:434-436,562-578,367-371`. Parpadeo momentaneo de la sidebar.
  Fix opcional: versionar refrescos.
- **send() no captura errores del binding: un rechazo deja running=true colgado** — `low`
  / `error-handling` — `chat.ts:562-578,593-601,605-615`. Sin try/catch; spinner gira
  para siempre. Fix: try/catch que reponga `running=false` y `errorText`.
- **Tabs persistidos re-crean terminales pero pueden quedar adjuntadas a ptys muertos** —
  `info` / `behavior` — `tabs.ts:13-67`, `terminalSession.ts:21-87`. Verificar en Go que
  `StartPty(id)` tras reinicio no choque ni deje ptys huerfanos.
- **Persistencia: cambio de forma de 'workspace' en localStorage sin migracion/validacion**
  — `info` / `maintainability` — `chat.ts:699-705`. Fix opcional: validar que sea string
  no vacio antes de `SetWorkspace`.

### Area 10 — UI frontend: vistas y componentes
Archivos: `frontend/src/views/ChatView.vue`, `components/{ChatComposer,DevToolsPanel,
DevEventPanel,MessageList,ToolCall,MarkdownContent,AssistantMessage,AppSidebar,
WorkspacePicker,MentionMenu,CommandMenu}.vue`, `lib/{markdown,mention,command,reveal,
useSmoothText}.ts`.
Proposito: presentacion del harness (markdown de la IA con stream visual, tools, thinking,
planes, diffs), composer con @-menciones y /-comandos, sidebar, selector de workspace,
panel dev (git + terminal).
Estado: maduro y bien testeado; DOMPurify en los tres puntos de v-html; menus con
`mousedown.prevent`. Sin XSS evidente ni bugs bloqueantes en el render.

- **Enlaces del markdown de la IA pueden navegar el webview completo (sin target/rel ni
  apertura externa)** — `medium` / `security` — `markdown.ts:19-22`,
  `MarkdownContent.vue:62`, `main.css:152`. Un `<a>` del modelo reemplaza el SPA (se
  pierde el chat); superficie de phishing en el webview. Fix: hook DOMPurify
  `afterSanitizeAttributes` con `target=_blank`+`rel=noopener noreferrer nofollow`, o
  listener delegado que abra via `BrowserOpenURL`; restringir `ALLOWED_URI_REGEXP` a
  http/https/mailto.
- **Fuga de vnodes de Vue (render manual) y de setTimeout en los botones de copiar** —
  `low` / `performance` — `MarkdownContent.vue:20-22,35-38,41-55,58`. No se llama
  `render(null, btn)` al re-decorar; `setTimeout` toca nodos huerfanos. Fix: desmontar
  iconos viejos o usar SVG estatico; limpiar el timeout en `onScopeDispose`.
- **tail de MessageList compara solo status:output del ultimo tool; cambios de input/
  diff/error no disparan auto-scroll** — `low` / `ux` — `MessageList.vue:37-49`. La UI de
  aprobacion (pending) puede quedar fuera de vista. Fix: incluir `status:output:error:
  diff?length` en la firma.
- **Foco perdido y sin trampa de teclado en el borrado en dos pasos de la sidebar** —
  `low` / `ux` — `AppSidebar.vue:129-159`. Foco cae al body; Escape no cancela. Fix:
  enfocar confirmar (`nextTick`); `@keydown.escape`.
- **Listeners de pointermove/pointerup del resize del DevToolsPanel pueden quedar
  colgados si el panel se desmonta a mitad de arrastre** — `low` / `concurrency` —
  `DevToolsPanel.vue:68-81`. Fix: `setPointerCapture` o cleanup en `onUnmounted`.
- **Sin cobertura de test del comportamiento de XSS/enlaces ni del leak de copy** —
  `info` / `missing-test` — `markdown.test.ts:13-16`, `MarkdownContent.test.ts`. Fix:
  casos para `href=javascript:`, atributos `on*`, target/rel; test que cambie `text`
  verificando que no se acumulan vnodes/timeouts.
- **DOMPurify se invoca por separado en tres componentes sin politica central** — `info`
  / `maintainability` — `markdown.ts:21`, `DiffView.vue:40`, `DiffScreen.vue:34-37`. Fix:
  centralizar en un helper `sanitize()` con hooks/allowlist.

---

## 5. Patron transversal (raiz comun de varios highs)

Varios bugs nacen del mismo sitio: **la app cancela la corrida vieja pero no la espera**
antes de arrancar/borrar/recablear (`SetWorkspace`, `DeleteSession`, segundo
`SendPrompt`). De ahi salen: sesion resucitada, runs solapados que duplican `Tool.Failed`,
gate pisado, y `context.Canceled` visible. **Serializar las corridas por sesion** (una
goroutine por sesion drenando el inbox, o esperar el done antes de relanzar) cierra varios
de una vez.

---

## 6. Recomendacion de priorizacion (sin aplicar nada todavia)

1. **Seguridad primero:** gate de bash en subagentes y `CheckRedirect`/SSRF en webfetch.
   Son evasiones de controles existentes.
2. **Crash/cuelgue:** copia defensiva de `Snapshot.Seen` (panic) y timeouts en el
   provider/callers.
3. **Integridad de edits/datos:** validar rangos en `ApplyEdits`; esperar la corrida
   antes de `DeleteSession`; ampliar el hash a 32 bits; escrituras atomicas.
4. **Git usable en espanol:** `--porcelain -z` resuelve renames y unicode juntos.
5. **Pulido alto valor / bajo costo:** `ListCommands` con lock (one-liner);
   `context.Canceled` como cierre limpio; guarda de `sessionID` en `loadSession`; enlaces
   externos en el markdown.
6. **Decision de contrato:** que hace un follow-up mientras el agente trabaja (steer vs
   encolar vs abortar) — hoy aborta y pierde trabajo.

### Cobertura de tests sugerida (donde el verde no atrapa nada hoy)
- `go test -race ./...` en CI.
- Test que cruce `Apply` con `RecordSeenLines` sobre el mismo path bajo `-race`.
- `SetWorkspace` concurrente con `ListCommands`/`ListProjectFiles`.
- SSRF con `302 -> 169.254.169.254`.
- Subagente con bash que deba disparar el gate.
- `gitStatus` con rename y con nombre acentuado.
- `loadSession('A')` + `loadSession('B')` async sin await intermedio.
- OpenAI: cancelacion de ctx a mitad de stream; chunk sin usage; SSE cortado en tool args.
