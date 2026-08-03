# Estado del harness de Atenea y cómo potenciar el LLM

**Fecha:** 2026-08-02  
**Alcance:** Go core y TUI standalone. Se ignoraron `docs/`, `.okf/`, desktop, frontend y paquetes Wails.  
**Método:** cuatro análisis paralelos —loop, contexto/memoria, tools/subagentes y tendencias externas—, contraste manual del código y validación de fuentes primarias oficiales.

## Resumen ejecutivo

Atenea ya tiene un harness por encima de la media en sus fundamentos: log durable de eventos, reconstrucción de cada turno desde estado persistido, protección por epoch, compactación estructurada, tools concurrentes, permisos por efectos, subagentes supervisados, memoria con procedencia, prompt caching observable y capability negotiation por adapter/modelo.

El mayor retorno no vendrá de añadir más autonomía indiscriminadamente. El orden recomendado es:

1. **Cerrar cuatro fallos de corrección del loop:** overflow reactivo, retries sin límite de progreso, cancelación del stream cuando falla persistencia y permisos fail-closed.
2. **Construir trazas y evals reproducibles sobre los eventos que ya existen.** Sin esto no se puede saber si un cambio de prompt, contexto, tool o modelo potencia realmente al LLM.
3. **Añadir structured outputs provider-neutral.** Compactación e informes de subagentes hoy dependen de “JSON only” más validación posterior.
4. **Convertir el manejo de contexto en un budget planner explícito**, incluyendo recuperación accionable de outputs truncados y compactación verificable.
5. **Mejorar delegación, memoria y MCP sólo después de medirlos:** budgets de subagentes, trust labeling, FTS/BM25 con procedencia y hardening remoto.

La tendencia 2025–2026 es clara: contexto mínimo de alta señal, contratos estructurados, ejecución paralela acotada, delegación aislada, memoria explícita, capabilities negociadas y evaluación de trayectorias completas. No es “más agentes”; es mejor control y feedback.

---

## 1. Arquitectura actual

### Flujo de un turno

1. `Run` prioriza mensajes `steer`, luego cola normal, y repara tool calls interrumpidas antes de continuar (`internal/session/runner/run.go:20`, `internal/session/runner/run.go:35`, `internal/session/runner/run.go:48`).
2. Cada intento lee el epoch y reconstruye mensajes desde el store durable (`internal/session/runner/turn.go:67`, `internal/session/runner/turn.go:81`).
3. Selecciona modo, prompt, permisos, tools y snapshot del provider/modelo (`internal/session/runner/turn.go:93`, `internal/session/runner/turn.go:106`, `internal/session/runner/turn.go:108`).
4. Ejecuta compactación preventiva y vuelve a preparar el request si cambió el contexto (`internal/session/runner/turn.go:127`, `internal/session/runner/turn.go:140`).
5. Drena y persiste el stream completo; después ejecuta tool calls locales concurrentemente (`internal/session/runner/turn.go:165`, `internal/session/runner/turn.go:190`).
6. Publica resultados como mensajes `tool`, permitiendo que el siguiente turno reconstruya el round trip por call ID (`internal/session/runner/publish.go:181`, `internal/session/runner/publish.go:205`).

### Fortalezas que conviene preservar

- **Fuente de verdad durable:** mensajes y tools se proyectan desde un log append-only, no desde estado efímero (`internal/session/projection.go:5`).
- **Requests consistentes:** el doble control de epoch evita enviar contexto obsoleto (`internal/session/runner/turn.go:140`).
- **Interfaz LLM profunda y pequeña:** `Provider` sólo necesita `Stream`, con contrato explícito de bracketing, cancelación y concurrencia (`agentcore/llm/provider.go:8`, `agentcore/llm/provider.go:26`).
- **Integridad de tool calls:** conserva argumentos JSON crudos, IDs y errores al reproyectar historial (`internal/session/runner/turn.go:245`).
- **Paralelismo correcto:** persiste primero el mensaje que declaró las tools y luego asienta llamadas concurrentes (`internal/session/runner/turn.go:165`).
- **Compactación transaccional:** resumen estructurado, referencias exactas a secuencias y commit condicionado al epoch (`internal/session/compaction.go:119`).
- **Prompt cache-aware:** secciones estables antes del entorno dinámico y `SessionKey` estable por sesión (`internal/session/prompt/prompt.go:66`, `internal/session/runner/turn.go:113`, `internal/session/runner/turn.go:160`).
- **Usage rico:** input, output, reasoning, cache read/write y denominador cacheable (`agentcore/llm/event.go:56`).
- **Tools deterministas:** definitions ordenadas y settlement cerrado sobre el set materializado (`internal/tool/registry.go:124`).
- **Memoria prudente:** hechos explícitos con proyecto, fuente y fecha; nunca se inyectan silenciosamente como verdad (`agentcore/memory/memory.go:14`, `agentcore/memory/memory.go:24`).
- **Subagentes maduros:** límites de profundidad y concurrencia, timeout, ejecución detached, cancelación, worktrees opcionales y schema de salida (`internal/session/subagent/subagent.go:24`, `internal/session/subagent/subagent.go:206`, `internal/session/subagent/subagent.go:278`).
- **MCP con controles importantes:** sensitivity, allowed tools, sampling opt-in, timeouts y supervisión (`internal/mcpclient/manager.go:48`, `internal/mcpclient/manager.go:64`, `internal/mcpclient/manager.go:174`).

---

## 2. Problemas prioritarios del estado actual

### P0 — Overflow reactivo pierde su tipo y no compacta

El contrato permite que `StepFailed` lleve un error tipado (`agentcore/llm/event.go:40`). Existe `ContextOverflowError` (`internal/llm/context.go:10`), pero `consume` lo reemplaza por `ProviderError{Message: ev.Text}` y descarta `ev.Err` (`internal/session/runner/turn.go:177`). Como resultado, un overflow real dentro del stream termina como fallo durable en vez de activar compactación.

Además, cuando la actividad actual sola no cabe, `ErrNoCompactableHistory` se ignora y el request se envía igualmente (`internal/session/runner/turn.go:131`), aunque `ErrActivityTooLarge` ya está declarado (`internal/session/compaction.go:15`).

**Recomendación:** preservar `Event.Err`; clasificar overflow; compactar una vez con razón `overflow`; reconstruir; si no hay prefijo compactable, devolver `ErrActivityTooLarge` con uso estimado, ventana y reserva de salida.

### P0 — Rebuild y recompaction no tienen límite de progreso

`runTurnWithFinal` reintenta indefinidamente para señales internas (`internal/session/runner/turn.go:50`). Un epoch que cambia continuamente o una compactación que no reduce el request podría crear un loop sin llegar al provider.

**Recomendación:** presupuesto interno pequeño de rebuilds/compactaciones, con comprobación de progreso sobre epoch, baseline y tokens antes/después. No debe ser un retry genérico de errores del provider.

### P0 — Un fallo de persistencia puede dejar bloqueado al productor

Si `Publisher.Publish` falla, `consume` retorna inmediatamente (`internal/session/runner/turn.go:182`) sin cancelar un contexto hijo del provider ni drenar su canal. Un adapter que siga produciendo puede quedar bloqueado enviando eventos.

**Recomendación:** crear un contexto cancelable por stream; ante fallo durable, cancelar productor y esperar su cierre antes de retornar. Aplicar deadline corto al cleanup creado con `context.WithoutCancel` (`internal/session/runner/turn.go:173`).

### P0 — Permisos fail-open por omisión de wiring

Si `gate` o `policy` son `nil`, el middleware ejecuta la tool sin clasificación (`internal/tool/registry.go:168`). TaskTool repite este patrón para hijos (`internal/session/subagent/subagent.go:399`). El wiring productivo puede configurarlo bien, pero el constructor no hace seguro el estado inválido.

**Recomendación:** runtimes capaces de efectos deben construirse fail-closed. El bypass debe ser una política explícita, no dos `nil`. Aplicarlo igual a subagentes.

### P1 — No existe un ciclo sistemático de evals

Los eventos guardan deltas, retries, usage, tool settlements y atributos extensibles, pero el publisher no registra provider/model, attempt, latencia, request ID, trace ID ni relación padre-hijo (`internal/session/runner/publish.go:67`, `agentcore/session/event.go:121`). No hay un runner de evals de trayectorias.

Esto impide responder con datos preguntas básicas:

- ¿La nueva descripción de una tool mejora selección y argumentos?
- ¿La compactación conserva restricciones y decisiones?
- ¿Un modelo más barato mantiene calidad?
- ¿Los subagentes reducen tiempo o sólo multiplican tokens?
- ¿El caching mejora realmente hit rate y latencia?

### P1 — Structured outputs sólo se simulan con prompting

La compactación pide “Return JSON only” y valida al final (`internal/session/runner/compactor.go:122`, `internal/session/runner/compactor.go:153`). Los subagentes concatenan el schema al prompt y también validan después (`internal/session/subagent/subagent.go:396`, `internal/session/subagent/subagent.go:433`).

**Recomendación:** extender `llm.Request` con output estructurado provider-neutral y añadir capacidades de schema estricto/dialecto. Mantener validación local siempre.

### P1 — Outputs truncados no son recuperables por el modelo

`OutputStore` conserva el resultado completo, pero devuelve sólo el prefijo (`internal/tool/output.go:23`). `Result.Truncated` no llega al mensaje durable y no existe una tool visible para recuperar `Full(callID)` (`agentcore/tool/tool.go:45`, `internal/session/runner/publish.go:187`). El modelo puede perder justo el error final o un resumen importante; el corte por bytes puede romper UTF-8.

**Recomendación:** persistir metadata de truncado y ofrecer lectura paginada por call ID. Usar corte por runas y, para texto de diagnóstico, head + tail.

### P1 — Validación común de schemas incompleta

El registry repara antes de permisos, lo cual es correcto (`internal/tool/registry.go:155`), pero varias tools no cierran `additionalProperties` y el reparador no aplica todo JSON Schema. Esto debilita la correspondencia entre lo anunciado, lo aprobado y lo ejecutado.

**Recomendación:** validar el schema completo antes y después de reparar; homogeneizar `additionalProperties: false` salvo extensibilidad deliberada.

### P2 — Estimación de tokens y compactación son aproximadas

La estimación global usa aproximadamente tres bytes por token (`internal/llm/context.go:32`). Puede compactar demasiado pronto o demasiado tarde según modelo, JSON y Unicode. La llamada auxiliar de resumen tampoco verifica explícitamente que ella misma quepa en contexto (`internal/session/runner/compactor.go:107`).

El resumen valida forma pero no fidelidad ni cobertura. Todo el prefijo anterior al último user queda representado por nueve listas (`internal/session/runner/compactor.go:58`, `internal/session/compaction.go:25`).

**Recomendación:** un budget planner que calcule system + tools + mensajes + output reserve + margen; estimadores por adapter cuando existan; telemetría de error estimado contra usage real; resumen con referencias a seq/call ID y tail literal adicional; chunk/merge si la propia compactación no cabe.

### P2 — Memoria útil pero demasiado lexical

Recall usa substring case-insensitive y recencia. No busca por `Source`, no rankea relevancia y no soporta supersede/delete/deduplicate. Aun así, su modelo de confianza es correcto: explícita y con procedencia (`internal/tool/memory.go:59`).

**Recomendación:** empezar con SQLite FTS/BM25 + filtros por fuente/fecha, no embeddings. Añadir actualización, supersede y borrado. Verificar con evals antes de introducir retrieval semántico.

### P2 — Delegación requiere más control de coste y confianza

Task mide requests, tokens, duración y tools (`internal/session/subagent/subagent.go:370`), pero sólo impone timeout opcional y `Steps`; no hay budgets host-side de tokens/requests. Worktree es elección del modelo, no consecuencia de efectos (`internal/session/subagent/subagent.go:299`). El informe del hijo vuelve al padre como texto sin etiqueta explícita de contenido no confiable.

**Recomendación:** budgets máximos host-side, aislamiento por defecto para hijos con escritura/comandos, y envelope de resultado que marque el reporte como datos no confiables con procedencia, modelo, tools y workspace.

### P2 — Multimodalidad está declarada pero no atraviesa la sesión

`llm.Message` soporta Parts y existe capacidad `Vision`, pero `session.Message`, inbox y proyección durable son text-only (`agentcore/llm/provider.go:72`, `internal/session/runner/turn.go:245`).

**Recomendación:** sólo priorizarlo si hay casos de producto/evals concretos. Requiere cambio completo de contrato durable, no un parche en adapters.

---

## 3. Roadmap recomendado

### Fase 0 — Correctness del loop

1. Overflow reactivo tipado y `ErrActivityTooLarge`.
2. Límite de rebuild/compaction con prueba de progreso.
3. Cancelación y join del stream ante fallo de persistencia.
4. Cleanup con deadline.
5. Permisos fail-closed en raíz y subagentes.
6. Semántica completa para `ProviderExecuted`, evitando calls pendientes después de un step exitoso.

**Criterios de éxito:** reproducciones end-to-end de overflow, store failure, cancelación y runtime sin gate; ninguna goroutine colgada; log durable coherente.

### Fase 1 — Trazas y evals antes de optimizar

Construir primero una representación local, simple y vendor-neutral; JSONL exportable es suficiente antes de adoptar OpenTelemetry.

Campos mínimos:

- trace/session/turn/span y parent span;
- provider, endpoint, model y snapshot de capacidades;
- versión/hash de prompt y tool definitions;
- attempts, retries, latencia, tokens, caché y coste cuando sea conocido;
- tool, argumentos redactados, efectos, decisión de permiso, resultado y truncado;
- compaction reason, tokens antes/después y cobertura;
- subagent type, modelo, presupuesto, worktree y settlement;
- resultado observable: tests, build, diff y errores.

Suite inicial de evals:

1. task success y pruebas finales;
2. selección de tool y argumentos válidos;
3. respeto de scope y permisos;
4. recuperación tras tool error;
5. fidelidad de compactación;
6. continuidad tras reanudación;
7. coste/latencia/cache hit rate;
8. beneficio neto de subagentes frente a agente único;
9. resistencia a prompt injection desde web, skills, MCP y reportes hijos;
10. no fuga de secretos.

**Criterio de éxito:** cada cambio de prompt, modelo o harness puede compararse contra un baseline reproducible por trayectoria, no sólo por respuesta final.

### Fase 2 — Contratos estructurados y mejor feedback de tools

1. `StructuredOutput` en `llm.Request`.
2. Capabilities para strict output, dialecto/subset de schema y refusal.
3. Usarlo en compaction y task reports.
4. Validación JSON Schema completa local.
5. Recuperación paginada de tool outputs truncados.
6. Descripciones específicas para `task_status`, `task_wait` y `task_cancel` en vez de una descripción genérica (`internal/session/subagent/supervisor.go:153`).

**Criterio de éxito:** cero ciclos de reparación por JSON malformado en providers compatibles y error explícito/degradación conocida en los demás.

### Fase 3 — Context budget planner

1. Presupuesto único para system, tools, historial, output y margen.
2. Medición de estimación frente a usage real por adapter/modelo.
3. Compactación verificable con referencias y tail literal.
4. Compactación chunked si la llamada de resumen no cabe.
5. Recuperación de memoria FTS/BM25 bajo demanda y con procedencia.
6. Política de prefijo estable basada en métricas reales de caché.

**Criterio de éxito:** menos overflow, menos compactaciones innecesarias y mejor conservación de restricciones en evals largos.

### Fase 4 — Subagentes y MCP endurecidos

1. Budgets host-side de requests/tokens/tool calls/duración.
2. Worktree automático según efectos.
3. Trust labeling y sanitización de reportes hijos.
4. Rate limits y redacción centralizada.
5. MCP: registrar versión/capabilities negociadas, reaccionar a `list_changed`, revisar `AllowedTools: ["*"]`, endurecer auth y SSRF remoto.
6. Evaluar locks por recurso para tool calls paralelos conflictivos.

**Criterio de éxito:** el multiagente sólo se activa donde mejora score o tiempo con un coste aceptable; no amplía permisos ni mezcla cambios concurrentes de forma insegura.

---

## 4. Tendencias 2025–2026 y aplicabilidad

### Prácticas maduras que sí conviene adoptar

#### Contexto mínimo y recuperación just-in-time

Anthropic recomienda seleccionar “the smallest possible set of high-signal tokens”, compactar cerca del límite, conservar decisiones/errores y recuperar detalles bajo demanda.

**Aplicación:** profundizar el compactor y output retrieval; no aumentar indiscriminadamente el system prompt.

Fuente: Anthropic, *Effective context engineering for AI agents*, 2025-09-29.  
https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents

#### Prompt caching por prefijo exacto

Anthropic cachea en orden `tools → system → messages`; cualquier cambio anterior al breakpoint invalida lo posterior. TTL documentado: cinco minutos o una hora.

**Aplicación:** Atenea ya ordena tools y estabiliza prompt. Antes de rediseñar, medir invalidaciones por modo, skills y MCP. No mantener tools prohibidas sólo por ganar caché si eso degrada seguridad.

Fuente: Anthropic, *Prompt caching*.  
https://platform.claude.com/docs/en/build-with-claude/prompt-caching

#### Structured Outputs

OpenAI distingue JSON mode —sólo JSON válido— de Structured Outputs con adhesión al subset soportado de JSON Schema. Aun así, refusal, truncado y corrección semántica deben tratarse por separado.

**Aplicación:** contrato opcional por capability + validación local; compaction y subagent reports son los primeros consumidores.

Fuente: OpenAI, *Structured model outputs*.  
https://developers.openai.com/api/docs/guides/structured-outputs

#### Evals de trayectorias

Trace grading evalúa decisiones, tool calls y pasos completos, permitiendo localizar dónde falla el workflow en lugar de puntuar sólo la respuesta final.

**Aplicación:** aprovechar el event log existente como base de spans y datasets locales.

Fuente: OpenAI, *Trace grading*.  
https://developers.openai.com/api/docs/guides/trace-grading

#### Multiagente selectivo

Anthropic reporta +90,2% frente a agente único en un eval interno de investigación, pero aproximadamente 15× tokens frente a chat y advierte que tareas con contexto compartido o muchas dependencias —incluyendo gran parte de coding— son peor encaje.

**Aplicación:** usar subagentes para investigación, exploración, tests y revisión independientes; medir contra agente único. No convertir cada tarea en un grafo.

Fuente: Anthropic, *How we built our multi-agent research system*, 2025-06-13.  
https://www.anthropic.com/engineering/multi-agent-research-system

#### MCP como frontera de confianza

La spec exige tratar annotations como no confiables salvo servidor confiable y recomienda human-in-the-loop, inputs visibles, confirmación, timeouts, validación y auditoría.

**Aplicación:** la base de Atenea es buena; reforzar remote auth, dynamic changes, rate limiting y trust del servidor.

Fuente: MCP Specification, *Tools*, versión 2025-11-25.  
https://modelcontextprotocol.io/specification/2025-11-25/server/tools

### Tendencias útiles pero todavía opcionales

- **Compaction nativa/opaca del proveedor:** puede reducir coste, pero pierde portabilidad y auditabilidad. Mantener detrás de capability y nunca como única representación durable.
- **Embeddings para memoria:** no justificarlos antes de medir FTS/BM25. El problema actual es retrieval limitado, no necesariamente falta de vectores.
- **Ejecución anticipada de tools durante streaming:** reduce latencia, pero complica orden durable y cancelación. Evaluarla sólo después de correctness y trazas.
- **Routing automático de modelos:** usar políticas basadas en evals y capabilities; evitar que el propio agente elija libremente modelo/coste sin límites.

### Tendencias que no deberían entrar aún en el núcleo

- **MCP Tasks:** la spec 2025-11-25 las marca experimentales.
- **Forks completos de contexto y delegación recursiva amplia:** alto coste, contaminación de contexto y coordinación difícil.
- **Memoria autónoma que se auto-consolida como verdad:** riesgo de obsolescencia y amplificación de errores.
- **Native compaction como sustituto del checkpoint local:** demasiado provider-specific y opaco.

---

## 5. Qué haría primero

Si sólo se financian tres inversiones:

1. **Correctness P0 del loop y permisos.** Potenciar un LLM sobre un loop que pierde overflow tipado o puede ejecutar fail-open multiplica riesgo, no capacidad.
2. **Trace/eval harness mínimo.** Es el multiplicador de todas las mejoras posteriores y evita optimizar por intuición.
3. **Structured outputs + output retrieval.** Mejora compactación, delegación y recuperación con una misma extensión profunda del contrato.

Después mediría dos experimentos:

- compactación actual frente a compactación con referencias/tail literal;
- memoria substring frente a SQLite FTS/BM25.

Sólo si los evals muestran una brecha real avanzaría a embeddings, native compaction, multimodalidad o routing autónomo.

---

## 6. Decisión arquitectónica sugerida

Crear un módulo profundo de **turn execution policy** detrás de una interfaz pequeña que concentre:

- budget de contexto y output;
- clasificación de overflow;
- límites de retry/compaction;
- capabilities efectivas;
- metadata de traza del intento.

Hoy estas decisiones están repartidas entre `turn.go`, `compactor.go`, `context.go` y capabilities. No conviene crear un framework genérico: una interfaz del estilo “planear request / observar resultado” debe ocultar esas reglas y devolver un plan inmutable al runner. El runner sigue siendo dueño del orden durable; el planner no toca Store ni ejecuta tools.

Para evals, usar otra seam: un exportador de eventos/trace derivado del log durable. No contaminar el contrato público del provider con OpenTelemetry ni tipos de un vendor.

---

## Fuentes primarias consultadas

1. Anthropic — *Effective context engineering for AI agents* (2025-09-29): https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
2. Anthropic — *Prompt caching*: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
3. Anthropic — *How we built our multi-agent research system* (2025-06-13): https://www.anthropic.com/engineering/multi-agent-research-system
4. Anthropic — *Memory tool*: https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool
5. OpenAI — *Structured model outputs*: https://developers.openai.com/api/docs/guides/structured-outputs
6. OpenAI — *Prompt caching*: https://developers.openai.com/api/docs/guides/prompt-caching
7. OpenAI — *Function calling*: https://developers.openai.com/api/docs/guides/function-calling
8. OpenAI — *Trace grading*: https://developers.openai.com/api/docs/guides/trace-grading
9. OpenAI — *Evaluate agent workflows*: https://developers.openai.com/api/docs/guides/agent-evals
10. OpenAI — *Compaction*: https://developers.openai.com/api/docs/guides/compaction
11. MCP Specification 2025-11-25 — *Tools*: https://modelcontextprotocol.io/specification/2025-11-25/server/tools
12. MCP Specification 2025-11-25 — *Lifecycle*: https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle
13. MCP Specification 2025-11-25 — *Tasks*: https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks

## Cobertura y límites

- Análisis estático; no se modificó ni ejecutó el harness.
- Se revisaron contratos y flujos del Go core, no UI desktop/frontend.
- Las recomendaciones externas se contrastaron con fuentes oficiales, no blogs secundarios.
- Los resultados cuantitativos de Anthropic proceden de sus evals internos y no deben asumirse como rendimiento esperado de Atenea; justifican medir, no copiar su arquitectura.
