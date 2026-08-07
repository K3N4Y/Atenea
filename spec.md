## Problem Statement

El usuario necesita iniciar una extracción barata cuando considere que una sesión contiene algo que vale la pena aprender, encolarla en segundo plano sin pausar ni ocupar el ciclo principal y auditar después la propuesta, la evidencia y la decisión humana. El flujo no debe depender de checkpoints del workspace: la historia durable de la sesión ya proporciona un corte inmutable y con procedencia suficiente.

La extracción no debe convertirse en aprendizaje automático: el usuario conserva el control sobre qué se aprueba, qué se rechaza y qué queda solamente como historial.

## Solution

Agregar al host de escritorio un comando local `/learn`. Al invocarlo sobre la Session activa, tanto si está inactiva como si tiene una corrida normal en curso, Atenea captura un corte durable, encola una corrida de aprendizaje independiente y devuelve el control inmediatamente. `/learn` no espera, cancela, pausa ni marca como ocupada la corrida principal.

La corrida de aprendizaje realiza una sola interacción con un Provider, no dispone de Tools y devuelve como máximo una lección candidata estructurada, o declara que no existe una lección durable respaldada por la evidencia. La aceptación de `/learn` sólo falla si no existe una Session activa, no hay evidencia durable o no puede crearse la corrida.

`/learn` confirma que la corrida quedó encolada, pero no obliga a abrir ni mantener visible el panel. El usuario puede continuar el ciclo normal y ejecutar `/learned` más tarde para auditar corridas, propuestas, decisiones, procedencia y costos. Desde esa auditoría puede agregar, editar, rechazar, cancelar o reintentar según el estado de cada corrida. Ninguna propuesta se incorpora automáticamente.

Las lecciones aprobadas se guardan por Workspace, se pueden desactivar o eliminar y se aplican a futuros Prompts mediante selección determinista y acotada, sin otra interacción con el Provider. Sólo se incorporan lecciones relevantes dentro de un presupuesto fijo del contexto. La aprobación humana sustituye la verificación automática costosa, aunque la extracción continúa contabilizando y mostrando los tokens que consume.

## User Stories

1. Como usuario del host de escritorio, quiero ejecutar `/learn`, para solicitar una lección sin redactarla manualmente.
2. Como usuario, quiero ejecutar `/learn` mientras la corrida normal está activa, para encolar el aprendizaje sin interrumpirla.
3. Como usuario, quiero que `/learn` analice la sesión actual sin crear un checkpoint, para evitar estado de workspace innecesario.
4. Como usuario, quiero que `/learn` capture un corte durable y estable de la historia, para que la propuesta no cambie mientras sigo trabajando.
5. Como usuario, quiero que la extracción se ejecute en segundo plano, para continuar usando Atenea sin esperarla.
6. Como usuario, quiero que la corrida de aprendizaje no marque la Session ni el Workspace como ocupados, para poder enviar nuevos Prompts.
7. Como usuario, quiero recibir una confirmación inmediata de que `/learn` quedó encolado, sin abrir obligatoriamente un panel.
8. Como usuario, quiero ejecutar `/learned` más tarde, para abrir la auditoría sin iniciar otra corrida.
9. Como usuario, quiero auditar corridas terminadas y activas, propuestas, decisiones, evidencia, procedencia, Provider, modelo, duración, tokens y fallos, incluso después de reiniciar Atenea.
10. Como usuario, quiero ver una única lección candidata concisa, para tomar una decisión rápida.
11. Como usuario, quiero que Atenea pueda responder que no hay una lección durable, para no acumular consejos inventados.
12. Como usuario, quiero ver el enunciado, alcance y cuándo no aplicar la lección, para evaluar su generalización.
13. Como usuario, quiero ver evidencia enlazada a la sesión y sus secuencias, para comprobar su origen.
14. Como usuario, quiero ver Provider, modelo, duración y tokens usados, para conocer el costo real.
15. Como usuario, quiero agregar una propuesta sin editarla, para aceptar rápidamente una buena lección.
16. Como usuario, quiero editarla antes de agregarla, para corregir redacción, alcance o excepciones.
17. Como usuario, quiero rechazarla, para impedir que influya en trabajo futuro.
18. Como usuario, quiero que aprobar o rechazar sea idempotente, para evitar decisiones duplicadas o contradictorias.
19. Como usuario, quiero cancelar una corrida activa, para detener trabajo que ya no me interesa.
20. Como usuario, quiero reintentar una corrida fallida sobre el mismo corte, para no mezclar evidencia nueva.
21. Como usuario, quiero que invocaciones equivalentes reutilicen la corrida existente, para no pagar dos veces.
22. Como usuario, quiero que propuestas y decisiones sobrevivan al reinicio de Atenea.
23. Como usuario, quiero que una corrida interrumpida aparezca como interrumpida y reintentable.
24. Como usuario, quiero que las lecciones pertenezcan al Workspace de origen, para no afectar otros proyectos.
25. Como usuario, quiero que una lección aprobada oriente futuros Prompts relevantes, para obtener una mejora observable.
26. Como usuario, quiero que seleccionar lecciones no haga otra llamada al Provider, para no pagar por decidir qué recordar.
27. Como usuario, quiero incorporar pocas lecciones y dentro de un presupuesto fijo, para que el contexto no crezca sin límite.
28. Como usuario, quiero ver qué lecciones están activas, para entender qué guía puede recibir el agente.
29. Como usuario, quiero desactivar una lección sin borrarla, para probar el comportamiento sin perder procedencia.
30. Como usuario, quiero eliminar una lección aprobada, para corregir una decisión equivocada.
31. Como usuario, quiero que rechazar, desactivar o eliminar impida su uso futuro, para mantener control efectivo.
32. Como usuario, quiero que aprender no modifique archivos, instrucciones del Workspace ni código.
33. Como usuario, quiero la lección menos específica que siga respaldada por la evidencia, para evitar sobreajuste.
34. Como usuario, quiero que respuestas inválidas o demasiado grandes se rechacen, para no revisar propuestas defectuosas.
35. Como usuario, quiero errores accionables cuando no haya Provider, sesión o la extracción falle.
36. Como usuario, quiero que toda propuesta figure como no verificada hasta mi aprobación.
37. Como usuario de tecnología asistiva, quiero navegar la auditoría, sus estados y acciones por teclado.

## Implementation Decisions

- Se introducen cuatro conceptos internos: **corrida de aprendizaje**, **lección candidata**, **lección aprobada** y **auditoría de aprendizaje**. La auditoría es la proyección durable de corridas, propuestas, decisiones, procedencia, uso y fallos.
- `/learn` es un comando local incorporado del host de escritorio. No se expande a un Prompt, no se materializa como mensaje y no inicia una corrida normal del agente; sólo captura el corte y encola trabajo independiente.
- El MVP acepta sólo `/learn` sin argumentos. Opera sobre la Session activa y exige que exista evidencia durable. Puede aceptarse durante una corrida normal o mientras haya Tools pendientes; esas actividades no son motivo de rechazo, espera ni cancelación.
- No se crean ni consumen checkpoints. Al invocarlo, el módulo fija `SessionID` y la última `Seq` durable estable disponible. Si el turno actual aún no tiene un cierre durable, sus eventos incompletos se omiten del contexto. Todo análisis se limita a eventos hasta ese corte aunque luego lleguen nuevos Prompts.
- El corte se materializa antes del trabajo asíncrono. Eliminar luego la Session o agregar eventos no altera la entrada aceptada, y la confirmación de encolado no espera a que termine la corrida.
- El contexto del learner se construye desde la proyección efectiva: conserva Prompts, mensajes finales, nombres y resultados de Tools, errores, diffs relevantes y resúmenes de Compaction; omite deltas, razonamiento privado, eventos de presentación, solicitudes de Permission gate y duplicados.
- El constructor aplica límites deterministas. Con Compaction usa el resumen estructurado como ancla. Outputs, diffs y evidencia respetan presupuestos y la auditoría indica si hubo contenido acotado.
- La corrida vive en un módulo interno separado del loop normal. No amplía contratos de terceros ni crea otra forma de Extension.
- El learner usa un snapshot del Provider y modelo activos al iniciar. Cambios posteriores no alteran esa corrida.
- Cada corrida hace como máximo una interacción con un Provider, sin Tools ni continuación por pasos, y exige salida estructurada validada por el host.
- La salida tiene dos variantes: candidato o `no_candidate` con razón breve. No se fabrica una lección por obligación.
- El prompt exige la formulación menos específica respaldada por evidencia, prohíbe consejos genéricos y detalles accidentales, exige alcance y contraindicaciones, y trata el contenido analizado como evidencia no confiable, no como instrucciones.
- No hay reviewer, tester, verifier ni otro subagente. La aprobación humana es la validación del MVP. Background elimina espera, no costo; uso y duración se registran.
- Estados durables: `queued`, `running`, `ready`, `no_candidate`, `failed`, `cancelling`, `cancelled`, `approved`, `rejected` e `interrupted`; sólo se permiten transiciones válidas.
- Al arrancar, corridas sin worker en `queued`, `running` o `cancelling` pasan a `interrupted` y pueden reintentarse con su corte original.
- Existe como máximo una corrida activa por Workspace; las demás esperan FIFO. Esta restricción sólo aplica a corridas de aprendizaje: la corrida normal tiene ciclo de vida y capacidad independientes, nunca espera a la cola de aprendizaje y nunca es pausada o cancelada por ella.
- Los estados se persisten aparte de los Session events, no entran en la conversación ni consumen contexto. El host emite cambio y la auditoría vuelve a consultar la fuente durable.
- La persistencia soporta almacenes durable y en memoria. Guarda corridas, candidato, decisión, lecciones, procedencia, uso, timestamps y fallos; no duplica permanentemente toda la Session.
- `/learn` sólo confirma el identificador y estado de la corrida encolada. `/learned` es un comando local que no encola ni invoca el Provider: abre la auditoría durable del Workspace activo y permite reabrirla sin duplicar solicitudes.
- La auditoría presenta el historial completo de aprendizaje, no sólo las lecciones aprobadas: corridas en cola o activas, `no_candidate`, fallidas, canceladas, interrumpidas, rechazadas y aprobadas. Cada registro muestra Session y `Seq` capturados, propuesta o razón de ausencia, evidencia y procedencia, Provider/modelo, duración, tokens, errores y decisión humana. Las acciones válidas siguen las transiciones de la corrida.
- `Add` aprueba exactamente; `Edit & Add` permite editar enunciado, alcance y excepciones; evidencia y procedencia son inmutables; `Reject` conserva la decisión y no crea lección.
- Aprobar valida estructura y límites. Crear la lección y cambiar el estado ocurre atómicamente. Repetir acciones no duplica nada.
- Las lecciones son datos privados de Atenea con alcance de Workspace. No se escriben en archivos ni modifican instrucciones descubiertas.
- Antes de un Turn normal, un selector determinista compara el último Prompt con enunciado, alcance y excepciones de lecciones activas. Usa coincidencia léxica normalizada y desempate estable, sin Provider.
- Se seleccionan como máximo cinco lecciones y 1.500 tokens estimados. Evidencia y métricas quedan en la auditoría. Sin relevancia suficiente no se agrega sección.
- Las seleccionadas aparecen en una sección estable del system prompt después de instrucciones del proyecto y antes del entorno. Se presentan como guía aprobada por el usuario, no como hechos universales.
- Desactivar, eliminar o rechazar excluye de nuevas peticiones. Un Turn ya preparado conserva su snapshot.
- Hay límites para enunciado, alcance, excepciones, razón y evidencia. Una salida inválida falla sin una interacción de reparación.
- Sólo se persiste la propuesta estructurada y evidencia acotada; la Session sigue siendo fuente de verdad y se enlaza mediante secuencias.
- El background respeta cancelación, cierre del host, concurrencia acotada y fallos del Provider.
- La primera entrega cubre desktop. El módulo Core permanece independiente del host para permitir TUI futura sin diseñarla ahora.

## Testing Decisions

- El seam principal es el flujo del host: `/learn` → confirmación inmediata y corrida en segundo plano → `/learned` → auditoría y decisión humana → presencia o ausencia de la lección en un Turn futuro.
- Pruebas con Provider programado demostrarán que `/learn` no se envía como Prompt, no altera la historia, no anuncia Tools, devuelve antes de terminar el worker y hace una sola petición.
- Una prueba de corte iniciará `/learn` durante una corrida normal, agregará eventos después y verificará que propuesta y procedencia sólo reflejen el corte original.
- Se verificará que `/learn` se acepta mientras hay una corrida normal o Tools pendientes, no cambia el estado de ocupación de la Session y sólo se rechaza sin Session o sin evidencia durable.
- Se probarán candidato, `no_candidate`, JSON inválido, campos faltantes, vacío, exceso de tamaño, fallo y cancelación.
- Una respuesta inválida debe fallar sin segunda petición automática.
- Se probará la máquina de estados, idempotencia, transiciones inválidas, cancelación, reintento e interrupción por reinicio.
- Habrá pruebas de contrato para persistencias durable y en memoria: orden, aislamiento por Workspace, atomicidad y supervivencia de la auditoría.
- Se probarán deduplicación, una corrida de aprendizaje activa por Workspace y cola FIFO, sin bloquear corridas normales.
- Se probará que eliminar la Session después de materializar la entrada no rompe la corrida.
- El constructor de contexto debe omitir deltas, razonamiento y UI; preservar resultados y errores; respetar Compaction y límites; y omitir turnos aún no cerrados en el corte.
- El selector será probado como determinista, estable ante mayúsculas/puntuación, aislado por Workspace y acotado por cantidad/tokens.
- Una lección relevante aprobada aparecerá en el system prompt; rechazada, desactivada, eliminada o irrelevante no.
- Se probará la consistencia de snapshots ante decisiones concurrentes.
- Las pruebas de UI cubrirán confirmación no bloqueante de `/learn`, apertura de `/learned`, estados, badge, edición, auditoría y prevención de dobles envíos.
- Componentes: teclado, foco, nombres accesibles, acciones deshabilitadas y anuncios de estado.
- Se reutilizarán patrones existentes de comandos locales, Providers programados, stores, Pinia y paneles, sin probar detalles privados.
- Un smoke test ejecutará `/learn` durante una Session activa, comprobará que el Turn normal continúa, abrirá `/learned`, auditará y aprobará la propuesta, y comprobará la siguiente petición recibida por el Provider.

## Out of Scope

- Checkpoints de workspace para `/learn`.
- Verificación automática con reviewers, testers, verifiers, tests generados o segunda interacción del Provider.
- Aprobación automática o aprendizaje sin acción explícita.
- Más de una lección por corrida.
- Modificar código, skills, prompts base, instrucciones o archivos del Workspace.
- Entrenar o actualizar pesos.
- Aprender de cambios externos no representados en la historia durable.
- Lecciones globales entre Workspaces.
- Embeddings, bases vectoriales o recuperación con otra llamada al Provider.
- Sincronización entre dispositivos o cuentas.
- Interfaz TUI o headless CLI en la primera entrega.
- Exponer learner, persistencia o scheduler como contrato público o Extension.
- Garantizar verdad fuera del alcance revisado; la aprobación es humana y reversible.

## Further Notes

- Background significa que no bloquea ni interrumpe el ciclo normal; no significa gratis. El ahorro surge de eliminar crítica/verificación automática y usar aprobación humana.
- Bennett se usa como criterio de redacción, no como prueba matemática: preferir la formulación menos específica respaldada por evidencia, sin volverla vaga o universal.
- La Session y sus Session events siguen siendo la fuente de verdad. La secuencia capturada sustituye al checkpoint y ofrece procedencia inmutable sin acoplar aprendizaje a restauración del Workspace.
- El seam de aceptación es deliberadamente único y alto: comando local → cola independiente → auditoría durable → decisión humana → efecto observable en un Turn futuro.
- La implementación debe mantener el loop y sus implementaciones en módulos internos; no requiere ampliar contratos públicos ni crear una nueva frontera de terceros.
