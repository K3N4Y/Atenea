---
updated_at: 2026-08-03
summary: Pinned source analysis of oh-my-pi's edit tool, with the pre-migration Atenea comparison retained as historical context.
---

# Investigación de la tool `edit` de oh-my-pi

## Alcance y versión examinada

Este informe describe exclusivamente la implementación upstream observada en el repositorio `can1357/oh-my-pi`, incluyendo el paquete compartido `@oh-my-pi/hashline` que la tool usa realmente. Se inspeccionó el commit **`5af71dc9cf132538e072806424f71f43f734d9ae`**. Todos los enlaces siguientes son permanentes a ese commit; la rama móvil correspondiente es <https://github.com/can1357/oh-my-pi/tree/main/packages/coding-agent/src/edit>.

**Convención:** “Hecho” significa conducta afirmada directamente por source, prompt o tests upstream. “Inferencia” identifica conclusiones comparativas o arquitectónicas derivadas de esas fuentes. Las referencias al antiguo “port Go” en la sección histórica describen el estado anterior a la migración y no el contrato actual de Atenea.

## 1. Factory, contrato y selección de modo

**Hechos.** `EditTool` no es una tool hashline aislada, sino una fachada dinámica con cuatro modos: `replace`, `patch`, `hashline` y `apply_patch`. La factory normal registra `edit: s => new EditTool(s)`; el bridge de Cursor fija explícitamente `replace`, porque sus frames llevan `old_string/new_string`. La instancia se declara `essential`, estricta, de concurrencia exclusiva y con aprobación de escritura (o lectura para URLs internas). La ruta usada para aprobación se extrae del primer header hashline/apply-patch o del campo `path`.

Fuentes: [factory de tools](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/tools/index.ts#L418), [bridge Cursor](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/cursor-bridge-tools.ts#L45-L58), [clase/factory interna, metadata y aprobación](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/index.ts#L367-L468).

El modo por defecto es `hashline`. La precedencia de resolución es: variante por modelo configurada en settings; `PI_EDIT_VARIANT`; `edit.mode`; default. Si el resultado es hashline y `PI_STRICT_EDIT_MODE` no está activo, modelos cuyo nombre contiene `kimi`, `mimo`, `deepseek-v4-flash` o `step-3.7-flash` caen a `replace`. El constructor también acepta un modo fijado y procesa `PI_EDIT_FUZZY`/`PI_EDIT_FUZZY_THRESHOLD`; estos dos ajustes conciernen a replace/patch, no al anclaje exacto hashline.

En Atenea, esos valores se componen en cada turno desde el `edit` tipado de la configuración local `providers.json` (`mode`, `model_variants`, `fuzzy`, `fuzzy_threshold`, `enforce_seen_lines`) y luego desde `PI_EDIT_VARIANT`, `PI_STRICT_EDIT_MODE`, `PI_EDIT_FUZZY` y `PI_EDIT_FUZZY_THRESHOLD`. La misma configuración alimenta las composiciones CLI, TUI y desktop; no existe otro subsistema genérico de settings persistidos.

Fuentes: [resolución de modo](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/utils/edit-mode.ts), [constructor y getters dinámicos](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/index.ts#L402-L456), [tests de precedencia y exclusiones](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit-mode.test.ts#L34-L111).

El schema hashline entregado al modelo sólo declara `{ input: string }`; deliberadamente tolera claves extra, pero no acepta `_input` como alias ni hace `path` model-facing. Para proveedores con custom tools también publica la gramática Lark; `apply_patch` cambia además el wire name a `apply_patch`, mientras hashline conserva `edit`. La gramática formal exige secciones `[filename#4HEX]`, aunque el parser runtime es más tolerante que esa gramática.

Fuentes: [schema](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/params.ts), [tests del schema](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/core/hashline.test.ts#L293-L337), [gramática Lark](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/grammar.lark), [exposición de custom format](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/index.ts#L447-L468).

## 2. Descripción que recibe el modelo y lenguaje hashline actual

**Hecho.** La descripción se importa directamente de `packages/hashline/src/prompt.md` y se renderiza en tiempo de acceso. El protocolo actual **no** usa los verbos SWAP/DEL/INS: usa:

- `PUT N.=M:` para reemplazar un rango inclusivo y `PUT N*:` para reemplazar el bloque sintáctico que comienza en N;
- `PUT <N:` / `PUT >N:` para insertar antes/después, `PUT <1:` para head y `PUT >$:` para tail;
- `PUT >N*:` para insertar después del bloque;
- `CUT N.=M` / `CUT N*` para capturar y borrar;
- `PUT ... @name` para pegar registros capturados, incluso entre llamadas/secciones;
- `REM` para borrar el archivo y `MV DEST` para moverlo.

Cada body row empieza por `+`; el rango siempre refiere líneas originales y su tamaño no depende del body. El prompt exige tag de cuatro hex de un `read/search`, prohíbe crear (remite a `write`), tocar líneas no mostradas, ampliar rangos sobre keepers, operar dentro de elisiones y reusar números/tag tras un edit. Explica explícitamente Markdown, decoradores, bloques y antipatterns.

Fuentes: [prompt completo](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/prompt.md), [selección de descripción](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/index.ts#L510-L680).

## 3. Parser y semántica de aplicación

**Hechos.** `Patch.parse` divide secciones y un tokenizer más un `Executor` stateful convierte tokens en edits bajos (`insert`, `delete`, `cut`, `paste`, `block`) y una operación de archivo. Valida enteros seguros positivos, rangos no invertidos y un máximo de 100.000 líneas expandidas. Rechaza contaminación `apply_patch`/unified-diff con mensajes específicos, bodies en operaciones bodyless, `PUT` de registro con `:`, operaciones de archivo incompatibles y solapamientos ambiguos. Normaliza solapamientos exactos en ciertos casos y conserva orden de patch.

El runtime contiene recuperación tolerante de errores habituales que no aparece en la Lark estricta: bare bodies y bullets Markdown auto-prefijados con warning, filas copiadas de output `N:text`, rangos bare inferidos como PUT, `CUT` con colon ignorado, descarte de viejas filas `-` cuando hay nuevas filas explícitas, y elisiones/read metadata ignoradas. Aun así rechaza formas ambiguas, inserts sin body y filas `-` que parecen diff. El parser streaming sólo materializa la operación final cuando ya es suficientemente completa.

Fuentes: [parser](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/parser.ts), [tokenizer](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/tokenizer.ts), [split/input](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/input.ts), [mensajes de validación](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/messages.ts), [tests de contratos](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/test/core-contracts.test.ts), [tests de tolerancia](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/test/leniency.test.ts).

La aplicación trabaja contra líneas originales, no contra índices ya desplazados; valida referencias y corrige ciertos aterrizajes de delimitadores/indentación. Los block ops se resuelven mediante el resolver nativo tree-sitter del coding-agent: sólo valen bloques multilínea. Un replace/cut no resoluble falla; un insert/paste “after block” no resoluble degrada a after-line con warning. Los bloques Markdown incluyen secciones por heading.

Fuentes: [applier](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/apply.ts), [expansión de bloques](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/block.ts), [resolver coding-agent](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/block-resolver.ts), [tests de bloques](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/test/block.test.ts).

## 4. Snapshots, integración con `read`/search y hash

**Hechos.** El tag es un fingerprint de 16 bits, cuatro hex mayúsculas: xxHash32 del texto completo tras eliminar espacios/tabs/CR al final de cada línea. No es identidad suficiente por sí solo: el store conserva también texto completo. `read`, `grep/search` y `write` registran snapshots; `read` y búsqueda agregan las líneas efectivamente mostradas. Lecturas de rango mintean el tag del archivo completo, pero sólo marcan el rango visible; resúmenes colapsados marcan límites, no interior. La clave se canonicaliza por `realpath` para fusionar symlinks y variantes macOS. Archivos mayores de 4 MiB no reciben header.

Fuentes: [hash y formato](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/format.ts#L105-L139), [adapter de snapshots](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/file-snapshot-store.ts), [callsites de read](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/tools/read.ts#L1800-L1822), [callsites de grep](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/tools/grep.ts#L1450-L1529), [write devuelve nuevo header](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/tools/write.ts#L1260-L1312).

El store es por sesión, LRU de 30 paths, cuatro versiones por path y presupuesto global de 64 MiB (medido en code units). Deduplica sólo hash **y texto**, conserva textos distintos que colisionan, y `byHash` elige la versión retenida más reciente con ese tag. Mover relocaliza historial y provenance; borrar lo invalida.

Fuentes: [SnapshotStore/InMemorySnapshotStore](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/snapshots.ts), [tests, incluidas colisiones](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/test/snapshots.test.ts).

## 5. Validación, stale recovery y atomicidad lógica

**Hechos.** Cada sección necesita tag y el archivo debe existir; creación se rechaza indicando usar `write`. `prepare` lee, normaliza, valida política, resuelve bloques y aplica en memoria. Para varias secciones prepara **todas antes de escribir**, evitando aterrizar un batch con error de parse/hash/aplicación. También rechaza dos secciones que canonicalicen al mismo archivo. Los commits posteriores son secuenciales y no transaccionales: un fallo de escritura intermedio deja el prefijo ya escrito y el error enumera lo escrito/no escrito.

Fuentes: [Patcher prepare/apply/commit](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/patcher.ts), [orquestación coding-agent](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/execute.ts), [tests de preflight y targets duplicados](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/core/hashline.test.ts#L184-L291).

Si el hash live coincide, aplica directamente. Si hay drift y sólo existen inserts head/tail, aplica al live con warning porque la posición es estable. Para anclas normales, `Recovery` busca el snapshot histórico, mapea todas las anclas por líneas no cambiadas y contexto, exige un offset consistente y rechaza destinos cambiados, borrados, partidos o ambiguos. Si el path escrito no existe, puede recuperarlo por combinación única basename+tag; el adapter sólo permite redirección dentro del workspace o sandbox local, nunca desde URL interna ni hacia vault/out-of-tree. Un tag no registrado se identifica como “not from this session”; un stale reconocido dice que el archivo cambió y devuelve contexto alrededor de anclas, pidiendo nuevo header/re-read.

Fuentes: [recovery](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/recovery.ts), [MismatchError](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/mismatch.ts), [path recovery y apply recovery](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/patcher.ts#L350-L460), [restricción de redirect](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/filesystem.ts#L86-L109), [tests recovery chain](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/test/recovery-session-chain.test.ts), [tests de integración y path recovery](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/core/hashline.test.ts#L339-L578).

El guard de líneas vistas es configurable (`edit.enforceSeenLines`) y está **desactivado por defecto en coding-agent**, aunque el Patcher genérico default sea true. Cuando está activo, revela hasta 40 anclas no vistas y 512 columnas; si puede revelarlas completas, las incorpora al provenance para que el retry idéntico ya sea válido; si trunca, obliga a re-read. Las líneas con clipping normal de `read` sí cuentan como vistas.

Fuentes: [implementación del guard](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/patcher.ts#L597-L654), [setting conectado](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/execute.ts#L169-L175), [tests read/search y default](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit/seen-line-guard.test.ts).

## 6. Escritura y normalización

**Hechos.** Al preparar se separa BOM, se detecta el primer estilo de newline y se normaliza a LF. Al commit se restauran BOM y line endings. Los notebooks se editan como vista textual de celdas y se serializan otra vez a JSON. Antes de editar se rechazan archivos autogenerados; se aplican gates de plan mode. El write pasa por ACP bridge si corresponde o por LSP writethrough, pudiendo formatear y obtener diagnostics; invalida caches y notifica deletes/moves al LSP.

Si el cliente/formatter transforma el contenido al guardar, el nuevo snapshot se calcula a partir de lo que realmente quedó en disco, no de lo solicitado. El diff visible conserva el cambio pretendido y se agrega warning de drift, evitando convertir format-on-save en un diff gigante. `REM` invalida snapshots; `MV` los relocaliza. Una sección no-op devuelve hint; tres repeticiones consecutivas del mismo payload/path escalan a `ToolError`.

Fuentes: [Patcher normalización/commit](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/patcher.ts#L392-L428), [commit y drift de escritura](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src/patcher.ts#L463-L574), [adapter FS/ACP/LSP](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/filesystem.ts), [notebooks](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/read-file.ts), [guard de loops no-op](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/noop-loop-guard.ts).

## 7. Streaming, diff y renderer

**Hechos.** Cada modo registra una estrategia de streaming con extracción de edits completos, cálculo de preview, fallback y proyección de contenido/path para matchers TTSR. Hashline acepta el input parcial, su parser streaming descarta la op final incompleta pero proyecta el body a medida que crece, omite errores transitorios de la sección activa y desactiva validación stale durante streaming; al terminar sí calcula/recupera contra snapshots y muestra errores. Previews multi-section se separan por archivo y comparten un fork (no el clipboard live) para representar CUT/PUT entre secciones. Los matcher digests contienen sólo filas agregadas, nunca gramática, y same-path sections se fusionan.

Fuentes: [contrato y estrategia hashline](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/streaming.ts#L1-L105), [estrategia hashline concreta](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/streaming.ts#L527-L621), [diff preview hashline](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/diff.ts), [tests de preview](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit-streaming-preview.test.ts).

El resultado final genera unified diff, preview compacto y `firstChangedLine`; incluye header nuevo, resoluciones de bloque, move, warnings y diagnostics. Multiarchivo produce `perFileResults`. El renderer común está registrado tanto para `edit` como para wire `apply_patch`; obtiene paths de headers parciales, presenta tarjetas por archivo, estadísticas +/- inline, estados delete/move, errores y diffs con syntax highlighting. Durante streaming limita el diff a la cola del viewport y cachea frames/resultados estables para evitar duplicación/flicker. Los snapshots `oldText/newText` destinados a ACP se podan si juntos exceden 32.768 caracteres; el texto/diff del modelo permanece.

Fuentes: [render del resultado hashline](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/execute.ts#L84-L159), [renderer](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/renderer.ts), [registro del renderer](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/tools/renderers.ts#L85-L92), [poda ACP](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/snapshot-details.ts), [tests renderer](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/tools/edit-renderer.test.ts).

## 8. Errores y cobertura de tests

**Hechos.** Las clases/ramas observables distinguen, entre otros: parse malformado; tag ausente, no reconocido o stale irrecuperable; líneas no vistas; archivo inexistente; policy/plan/generated-file; bloques no resolubles; referencias fuera de rango; clipboard mal secuenciado; no-op; destinos move iguales; targets canonicales duplicados; y fallo parcial de commit. `executeHashlineSingle` preflighta multiarchivo y devuelve un aggregate; las variantes batch de la fachada paran en el primer fallo, marcan `isError` y explican qué ya fue aplicado y qué debe reemitirse. El renderer puede usar `MismatchError.displayMessage`, más rico que `message`, en errores per-file.

Fuentes: [errores del core](https://github.com/can1357/oh-my-pi/tree/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/src), [batch/error aggregation](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/index.ts#L126-L365), [runner hashline](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/hashline/execute.ts).

La cobertura primaria relevante incluye parser/leniency, aplicación, límites, clipboard, bloques, snapshots/colisiones, stale recovery y cadenas de sesión en `packages/hashline/test`; e integración runner, BOM/notebooks, no-op, preflight, path recovery, seen-lines, streaming y renderer en `packages/coding-agent/test`. No es sólo documentación: los tests fijan explícitamente los comportamientos descritos.

Índices de tests: [hashline tests](https://github.com/can1357/oh-my-pi/tree/5af71dc9cf132538e072806424f71f43f734d9ae/packages/hashline/test), [coding-agent core hashline](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/core/hashline.test.ts), [seen-line guard](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit/seen-line-guard.test.ts), [streaming](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/edit-streaming-preview.test.ts), [renderer](https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/test/tools/edit-renderer.test.ts).

## 9. Comparación histórica anterior a la migración

Esta sección preserva el resultado de la investigación que motivó el port. **No
describe la implementación actual.** En aquel baseline, Atenea exponía una sola
variante hashline basada en `SWAP`/`DEL`/`INS`, una sección por llamada y tags
CRC32; carecía de la fachada de cuatro modos, del preview streaming y de varias
garantías upstream descritas arriba. También se reprodujo entonces un defecto de
provenance: tras un edit, el snapshot fresco sólo marcaba la primera línea
cambiada y podía rechazar una segunda edición sobre otra línea ya leída.

La relación histórica era real: el commit upstream
`d6c51fe69aa922bf366679ef8a6111411f9f6815` documentaba
`SWAP`, `DEL` e `INS.PRE/POST/HEAD/TAIL`, aunque incluía operaciones de bloque
que aquel port no implementaba. El 30 de julio de 2026,
[`5ea583e413d6cb212d57e76278952d063dec188a`](https://github.com/can1357/oh-my-pi/commit/5ea583e413d6cb212d57e76278952d063dec188a)
reemplazó esa familia por `PUT`/`CUT`. Esta cronología explica por qué el port
anterior se parecía a una revisión upstream sin ser equivalente al pin analizado.

El harness histórico `read → edit → edit` y la tabla detallada de divergencias
se retiraron de la comparación vigente porque sus conclusiones dejaron de ser
ciertas después de la migración. Se conservan aquí únicamente como procedencia
de la investigación, no como evidencia de producto actual.

## 10. Estado actual de Atenea

Atenea implementa hoy la fachada de cuatro modos `replace`, `patch`, `hashline`
y `apply_patch`, con selección congelada por turno, schemas/wire names por modo,
proyecciones matcher y previews streaming. Hashline usa el protocolo actual
`PUT`/`CUT`/`REM`/`MV`, tags xxHash y snapshots versionados; admite múltiples
secciones, warnings, clipboard y recuperación stale. La implementación y sus
adaptaciones Go están localizadas y verificadas en el
[informe mantenido de paridad](../agents/oh-my-pi-edit-parity.md), especialmente
su tabla de fuentes/evidencia y sus delivery gates. El mapeo señala
`internal/tool/hashline`, `internal/tool/editmode/{replace,patch,apply_patch}.go`
y `internal/tool/edit_preview.go` como las implementaciones locales
([informe, “Pin and mapping”](../agents/oh-my-pi-edit-parity.md#pin-and-mapping)).

Las adaptaciones actuales no pretenden copiar componentes de host sin consumidor
local: el filesystem, locking y snapshot store son implementaciones Go; la
selección es una fachada inmutable por turno; Bubble Tea consume resultados y
previews genéricos; y ACP/notebooks quedan fuera por no existir esos contratos en
el TUI standalone. Estas decisiones y su justificación se mantienen en
[“Deliberate Go adaptations”](../agents/oh-my-pi-edit-parity.md#deliberate-go-adaptations)
y “Nonapplicability evidence” del reporte, en vez de duplicarse aquí.

## Conclusión

El pin upstream examinado combina cuatro modos, edición hashline estructural,
snapshots y recovery, operaciones multiarchivo, streaming y rendering. El valor
de este documento es describir esas fuentes upstream y conservar el contexto
histórico que llevó a la migración. La afirmación vigente de qué está
implementado, qué se adaptó y con qué tests se probó pertenece al
[reporte de paridad mantenido](../agents/oh-my-pi-edit-parity.md).
