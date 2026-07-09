# Spec: compactacion de contexto del agente

Fecha: 2026-07-09

## Estado

Diseno aprobado para convertir el seam `runner.Compactor` existente en una
compactacion real, durable, preventiva y reactiva. Este documento define el
comportamiento y los contratos; no es un plan de implementacion.

## Problema

El runner reconstruye cada turno desde el historial durable y ya puede pedir a
un `Compactor` que reduzca el contexto antes de reintentar. La implementacion
real todavia no existe. Una sesion larga puede acercarse a la ventana del modelo
o recibir un overflow del proveedor, sin una forma segura de conservar el estado
util de la conversacion y continuar.

La compactacion debe reducir el request enviado al modelo sin borrar el log de
eventos, sin romper llamadas de tools y sin ocultar al usuario que el contexto
efectivo cambio.

## Objetivos

- Compactar preventivamente cuando el request estimado alcance el 80% de la
  ventana total del modelo.
- Recuperar de forma reactiva ante un overflow real reportado por el proveedor.
- Generar un resumen estructurado con el mismo proveedor y modelo del agente.
- Conservar literalmente la actividad iniciada por el ultimo mensaje del
  usuario siempre que entre en el presupuesto.
- Persistir cada compactacion como un checkpoint durable, atomico y auditable.
- Mantener todos los eventos originales visibles y disponibles para auditoria.
- Reconstruir el mismo contexto efectivo despues de reiniciar la aplicacion.
- Evitar loops de compactacion y escrituras parciales ante fallos.

## No objetivos

- Borrar, reescribir o archivar eventos historicos.
- Reducir costos mediante un modelo auxiliar.
- Reintentar automaticamente una generacion de resumen fallida.
- Mostrar el resumen como si fuera una respuesta normal del asistente.
- Definir una compactacion manual iniciada por el usuario.
- Cambiar la semantica del limite `MaxSteps` del loop del agente.

## Decisiones de producto

- Politica hibrida: preventiva y reactiva.
- Umbral preventivo fijo: 80% de la ventana total del modelo.
- Resumen estructurado, no texto libre.
- Mismo proveedor y modelo del turno principal.
- Checkpoint durable dentro del log de la sesion.
- Actividad actual preservada desde el ultimo mensaje del usuario.
- Fallback por presupuesto, respetando grupos semanticos completos.
- Evento visible como tarjeta discreta y expandible.
- Si el resumen falla o es invalido, no se modifica el historial efectivo.

## Arquitectura elegida

Se usara un checkpoint durable transaccional. El log de eventos sigue siendo la
fuente de verdad y nunca se mutila. El checkpoint registra el resumen y el rango
historico que reemplaza para el modelo. En la misma operacion atomica se avanza
el `ContextEpoch.BaselineSeq` y se incrementa su `Revision`.

La proyeccion para el modelo queda formada por:

1. El resumen estructurado del ultimo checkpoint vigente.
2. El ultimo mensaje del usuario como ancla literal, cuando el fallback lo haya
   dejado dentro del rango resumido.
3. Los mensajes posteriores al rango cubierto por ese checkpoint.
4. La actividad actual conservada literalmente o, si no cabe, su ventana
   reciente seleccionada por presupuesto.

La proyeccion de UI sigue leyendo el log completo. Por eso los eventos cubiertos
por el resumen no desaparecen del chat.

## Componentes

### Politica de presupuesto

Una unidad independiente calcula si un `llm.Request` necesita compactacion. El
calculo incluye:

- system prompt;
- definiciones de tools;
- mensajes proyectados;
- reserva de salida del modelo;
- un margen conservador para diferencias del tokenizer y framing del proveedor.

El limite del modelo y el tokenizer deben resolverse mediante metadatos del
modelo. Cuando no exista un tokenizer exacto, se usa un estimador conservador.
Un modelo sin limite de contexto conocido no puede usar el umbral preventivo;
en ese caso solo se activa la ruta reactiva de overflow.

La condicion preventiva es:

```text
tokens_estimados_request >= floor(ventana_total_modelo * 0.80)
```

La reserva de salida forma parte de `tokens_estimados_request`; no se resta otra
vez de la ventana total.

### Selector de historial

El selector trabaja sobre mensajes con su `Seq` de origen y produce grupos que
no pueden separarse:

- un mensaje normal forma un grupo;
- un mensaje assistant con tool calls se agrupa con todos sus resultados;
- una tool call local sin resultado no es elegible para compactacion mientras
  este pendiente;
- un resultado nunca puede conservarse sin su llamada correspondiente.

En la ruta normal, el candidato a resumir termina justo antes del ultimo mensaje
del usuario. Desde ese mensaje en adelante se conserva la actividad literalmente.

Si esa actividad no entra despues de crear el resumen, se aplica el fallback:

1. El ultimo mensaje del usuario se fija como ancla literal obligatoria.
2. Los grupos posteriores se recorren desde el mas reciente al mas antiguo.
3. Se conserva un sufijo contiguo de grupos completos hasta agotar el presupuesto.
4. El tramo entre el mensaje ancla y ese sufijo se incluye en una segunda
   compactacion del mismo checkpoint antes de confirmarlo.
5. Si el mensaje ancla por si solo no entra, la compactacion falla con un error
   explicito de actividad no compactable.

El checkpoint registra el `Seq` del mensaje ancla. Al proyectar, se reinyecta ese
mensaje literal antes del sufijo reciente aunque su `Seq` quede dentro del rango
cubierto. Asi el baseline sigue siendo un corte contiguo y la reconstruccion no
depende de una lista arbitraria de mensajes preservados.

No se truncan silenciosamente textos ni outputs de tools en esta capa. Las tools
pueden seguir usando sus mecanismos propios de outputs acotados o referenciados.

### Generador de resumen

El generador hace una llamada aislada a `Provider.Stream` con el mismo modelo del
epoch. No incluye tools y no publica deltas como respuesta del asistente. Usa un
timeout propio para que un stream incompleto no bloquee el runner.

La entrada contiene el checkpoint anterior, si existe, mas los grupos nuevos que
seran cubiertos. Esto permite compactaciones sucesivas sin volver a enviar todo
el historial original.

La salida debe representar este esquema logico:

```text
objetivo_actual
restricciones_e_instrucciones
decisiones_tomadas
trabajo_completado
estado_de_archivos_y_cambios
resultados_relevantes_de_tools
errores_e_intentos_fallidos
pendientes_y_siguiente_paso
hechos_que_no_deben_reinterpretarse
```

Cada campo es obligatorio, aunque su valor pueda indicar que no hay elementos.
La serializacion concreta puede ser JSON o un tipo equivalente, pero debe
validarse antes de persistir y renderizarse de forma estable para el modelo.

El resumen se rechaza cuando:

- el stream falla o termina incompleto;
- la respuesta esta vacia;
- falta un campo obligatorio;
- no puede deserializarse;
- excede el presupuesto asignado al resumen;
- no produce una reduccion real del request;
- contiene referencias a un rango distinto del solicitado.

### Checkpoint durable

Se agrega `Context.Compacted` a la taxonomia de eventos. Su payload conceptual es:

```go
type CompactionCheckpoint struct {
    Summary              StructuredSummary
    ExpectedEpoch        ContextEpoch
    CoveredThroughSeq    Seq
    AnchorUserSeq        Seq
    PreservedFromSeq     Seq
    Model                string
    Reason               CompactionReason
    InputTokensBefore    int
    EstimatedTokensAfter int
}
```

`Reason` solo admite `preventive` y `overflow`. `PreservedFromSeq` identifica el
primer evento del sufijo reciente conservado literalmente. `AnchorUserSeq`
identifica el ultimo mensaje del usuario que inicia la actividad. Sin fallback,
`AnchorUserSeq` y `PreservedFromSeq` pertenecen al mismo tramo literal y
`CoveredThroughSeq` queda antes de ambos. Con fallback, el baseline puede avanzar
mas alla del ancla: la proyeccion recupera ese mensaje por su `Seq` y despues
agrega el sufijo desde `PreservedFromSeq`.

No se requiere un timestamp dentro del payload: el orden durable queda fijado
por el `Seq` del evento. El evento no materializa un `session.Message` normal.

### Contrato atomico del store

El `Store` expone una operacion equivalente a:

```go
CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error)
```

La operacion debe ejecutar en una sola seccion critica o transaccion:

1. Verificar que la sesion existe.
2. Comparar el epoch actual con `ExpectedEpoch`.
3. Verificar que `CoveredThroughSeq` existe y no retrocede el baseline vigente.
4. Verificar que `AnchorUserSeq` corresponde al ultimo mensaje de usuario elegido
   y que `PreservedFromSeq` delimita un sufijo valido de grupos completos.
5. Agregar `Context.Compacted` al log.
6. Guardar `BaselineSeq = CoveredThroughSeq`.
7. Incrementar `Revision` exactamente una vez.
8. Confirmar todo o no confirmar nada.

Un conflicto devuelve un error distinguible de checkpoint obsoleto. El caller
descarta el resumen generado y reconstruye desde el estado durable; no intenta
forzar el checkpoint ni reutilizarlo contra otro rango.

`MemoryStore` y `SQLiteStore` deben cumplir el mismo contract test. SQLite debe
persistir el epoch por sesion, no derivarlo solo del ultimo proceso en memoria.

### Proyeccion para el runner

La lectura usada por el runner debe ser explicita y distinta de la rehidratacion
de UI. Puede introducirse un contrato como `ContextForRunner`, que devuelve:

- epoch vigente;
- ultimo checkpoint vigente;
- mensaje de usuario ancla cuando quede en o antes del baseline;
- mensajes posteriores al rango cubierto;
- `Seq` de origen necesarios para agrupar y presupuestar.

El resumen se convierte en una parte de contexto claramente etiquetada como
resumen generado de turnos anteriores. No debe fingir un mensaje nuevo del
usuario ni del asistente. La representacion elegida debe ser soportada de manera
uniforme por los providers; si `llm.Request.System` sigue siendo un string, el
resumen se agrega a una seccion delimitada del system prompt.

La UI continua usando `Events(sessionID, 0)` y por tanto conserva el historial
completo, incluidos los eventos de compactacion.

## Flujo preventivo

1. El runner toma el epoch y construye el request candidato.
2. La politica estima su ocupacion.
3. Por debajo de 80%, el turno continua sin compactacion.
4. Al alcanzar 80%, el selector determina el rango resumible.
5. El generador produce y valida el resumen.
6. Se reconstruye un request candidato con el checkpoint aun no confirmado.
7. Si no hay reduccion o no cabe, se aplica el fallback por presupuesto.
8. El store confirma evento y epoch de forma atomica.
9. El attempt devuelve la senal interna de reconstruccion postcompactacion.
10. El runner relee el epoch, proyecta el checkpoint durable y llama una vez al
    provider para el turno normal.

## Flujo reactivo

La ruta reactiva se usa cuando el proveedor identifica de forma confiable un
error de ventana de contexto, incluso si la estimacion estaba por debajo de 80%.

1. El adapter del proveedor normaliza el error como `ContextOverflowError`.
2. Si el attempt no compacto previamente, ejecuta el mismo pipeline con
   `Reason = overflow`.
3. Confirma el checkpoint y reconstruye el turno.
4. Permite un solo reintento postcompactacion para esa ejecucion de `runTurn`.
5. Un segundo overflow termina el turno con un error explicito y no vuelve a
   compactar.

Errores de red, autenticacion, rate limit o cancelacion no activan compactacion.

## Concurrencia e idempotencia

- El epoch se toma antes de seleccionar el rango.
- Cualquier cambio de modelo, agente, baseline o revision invalida el trabajo.
- Dos compactaciones pueden generar resumen en paralelo, pero solo una puede
  confirmar para el mismo epoch.
- Repetir `CommitCompaction` con un epoch ya consumido devuelve conflicto y no
  agrega otro evento.
- El runner nunca llama al provider principal con un request construido desde un
  epoch que cambio durante la compactacion.
- La implementacion debe ser segura bajo `go test -race`.

## Manejo de fallos

- Fallo o timeout del resumen: no se escribe evento ni se avanza el baseline.
- Resumen invalido: mismo comportamiento, con error diagnostico.
- Conflicto de epoch: descartar resumen y reconstruir; no presentarlo como fallo
  del usuario si la reconstruccion puede continuar.
- Sin rango resumible: continuar solo si el request cabe; de otro modo fallar de
  forma explicita.
- Actividad minima demasiado grande: fallar indicando que el ultimo mensaje no
  cabe incluso sin historial anterior.
- Error atomico del store: conservar el estado previo completo.
- Cancelacion del usuario: cancelar tambien la llamada aislada de compactacion.

No hay retry automatico de la generacion del resumen. El usuario puede reintentar
el turno y disparar una nueva compactacion desde el estado durable sin cambios.

## UI y rehidratacion

`Context.Compacted` crea una tarjeta no conversacional con el titulo `Contexto
compactado`. La tarjeta:

- aparece en la posicion del evento dentro del log;
- inicia contraida;
- permite expandir las secciones del resumen;
- muestra el motivo y el rango cubierto;
- no altera el texto de mensajes historicos;
- no se mezcla con bloques de reasoning ni respuestas del asistente;
- se reconstruye igual al recargar la sesion.

Durante la generacion no se muestran deltas del resumen. La tarjeta aparece solo
despues del commit atomico. Si la compactacion falla, la UI no muestra una tarjeta
parcial.

## Observabilidad

El evento durable contiene suficiente informacion para auditar por que ocurrio y
si hizo progreso. Los errores de compactacion deben conservar causa y fase en el
error del turno o log de desarrollo, sin persistir contenido parcial del resumen.

Las metricas minimas derivables son:

- numero de compactaciones por sesion y motivo;
- tokens estimados antes y despues;
- conflictos de epoch;
- fallos de generacion o validacion;
- overflows repetidos despues de compactar.

No se agrega telemetria remota como parte de este alcance.

## Seguridad y privacidad

- El resumen se guarda en el mismo store y con el mismo alcance que la sesion.
- No se envia contexto a un proveedor distinto del ya configurado.
- Los secretos u outputs excluidos por las tools no deben reintroducirse desde
  snapshots externos.
- Los errores no deben imprimir el resumen completo por defecto.

## Compatibilidad y migracion

- Sesiones sin checkpoints mantienen `BaselineSeq = 0` y se proyectan como hoy.
- La migracion SQLite agrega estado de epoch por sesion y columnas o payload para
  `Context.Compacted` sin reescribir eventos previos.
- Providers que no normalicen overflow conservan la compactacion preventiva, pero
  un overflow no reconocido se trata como error normal.
- El `Compactor` nil puede mantenerse en tests unitarios enfocados en el camino
  historico, pero el wiring real debe instalar la implementacion.

## Estrategia de pruebas

La implementacion seguira el ciclo TDD definido en `AGENTS.md`.

### Politica y estimacion

- No compacta debajo de 80%.
- Compacta exactamente al alcanzar 80%.
- Incluye system, tools, mensajes y reserva de salida.
- Usa fallback conservador para modelo sin tokenizer exacto.
- Modelo sin ventana conocida no compacta preventivamente.

### Seleccion y resumen

- Conserva desde el ultimo mensaje del usuario.
- Incluye el checkpoint anterior en compactaciones sucesivas.
- Mantiene tool call y resultados en el mismo grupo.
- Nunca conserva un resultado huerfano.
- El fallback elige grupos recientes completos.
- Rechaza una actividad minima que no cabe.
- Rechaza resumen vacio, incompleto, enorme o sin reduccion.

### Store

- Commit agrega evento, avanza baseline e incrementa revision.
- Fallo intermedio no deja escritura parcial.
- Checkpoint obsoleto no modifica estado.
- Reinicio de SQLite reconstruye epoch y checkpoint.
- `MemoryStore` y `SQLiteStore` pasan el mismo contract test.
- Compactaciones sucesivas avanzan monotonamente.

### Runner y provider

- Ruta preventiva reconstruye antes de `Provider.Stream` normal.
- Happy path no genera llamada auxiliar.
- Overflow normalizado compacta y reintenta una sola vez.
- Segundo overflow falla sin loop.
- Otros errores no disparan compactacion.
- Cancelacion corta generador y turno.
- Cambio concurrente de epoch descarta el resumen.

### UI

- Evento crea tarjeta contraida y expandible.
- Rehidratacion conserva posicion y contenido.
- No crea mensaje de assistant.
- Fallo de compactacion no crea tarjeta parcial.
- El historial cubierto sigue visible.

### Puertas de cierre

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
cd frontend && npm test
cd frontend && npm run lint
```

Los comandos frontend exactos se ajustaran a los scripts existentes durante el
plan, sin introducir una herramienta nueva solo para esta funcionalidad.

## Criterios de aceptacion

1. Una sesion que alcanza 80% genera un solo checkpoint durable antes del turno
   normal y continua con menos contexto.
2. El contexto postcompactacion contiene el resumen estructurado y la actividad
   reciente literal, salvo el fallback documentado, que conserva el ultimo prompt
   del usuario y un sufijo reciente de grupos completos.
3. Todos los eventos originales siguen visibles y consultables.
4. Reiniciar Atenea produce el mismo contexto efectivo para el siguiente turno.
5. Ningun fallo de resumen o store deja un baseline avanzado sin evento valido.
6. Una carrera entre compactaciones confirma como maximo un checkpoint por epoch.
7. Un overflow real permite exactamente un retry postcompactacion.
8. Tool calls y resultados nunca quedan separados por la seleccion de ventana.
9. La UI muestra una tarjeta discreta, expandible y durable.
10. Todas las pruebas y puertas de calidad pasan, incluida la suite con `-race`.

## Riesgos y mitigaciones

- **Estimacion inexacta:** margen conservador y fallback reactivo.
- **Resumen que pierde hechos:** esquema obligatorio, checkpoint previo como
  entrada y validacion de campos.
- **Resumen demasiado grande:** presupuesto propio y requisito de reduccion real.
- **Carreras con nuevos eventos:** compare-and-swap por epoch dentro del store.
- **Tools separadas:** agrupacion semantica antes de presupuestar.
- **Loop de overflow:** un solo retry postcompactacion.
- **Divergencia UI/modelo:** contratos separados para log completo y contexto del
  runner.

## Impacto esperado en el codigo

La implementacion probablemente tocara, sin fijar aun el orden del plan:

- `internal/llm`: metadatos de contexto, estimacion y error normalizado.
- `internal/session`: evento, checkpoint, epoch y operacion atomica del store.
- `internal/session/runner`: compactor real, seleccion, generacion y retries.
- `internal/wiring` o `app.go`: instalacion de la implementacion real.
- `frontend/src`: proyeccion y tarjeta de `Context.Compacted`.
- tests de contrato, runner, providers y UI.

No se asume que estos deban vivir en archivos grandes existentes. El plan debe
preferir unidades pequenas para politica, seleccion, generacion y persistencia.
