// Package runner aloja el loop del agente: el loop externo Run, runTurn (un
// turno de proveedor) y publish (traduccion de eventos). El publisher
// (publish.go) aterrizo en M3: traduce cada llm.Event a un SessionEvent durable
// con la taxonomia del contrato y bufferiza los deltas para emitir tambien el
// bloque cerrado con el contenido completo. El loop runTurn/consume aterrizo en
// M5: runTurn arma el Request desde el historial proyectado, llama Provider.Stream
// una vez, y consume traduce el stream asentando las tools locales de forma
// concurrente con errgroup, devolviendo needsContinuation. El loop externo Run
// (run.go) aterrizo en M6: drena el Inbox de la sesion (queue/steer), promueve
// el input pendiente a Message{Role: user}, y corre el doble loop (actividad +
// pasos, con MaxSteps = 25) que llama runTurn en bucle. Continua solo por tool
// call local (needsContinuation) o por un steer admitido durante la corrida,
// nunca por texto del asistente; agotar los pasos devuelve StepLimitExceededError.
// Las senales de control internas (errRebuildTurn, errContinueAfterCompaction)
// aterrizaron en M7: runTurn pasa a ser un retry loop sobre runTurnAttempt, que
// snapshotea el ContextEpoch al preparar el request y lo re-chequea antes de
// Stream; si el epoch cambio (agente, modelo o revision) devuelve errRebuildTurn
// y reconstruye desde el store SIN haber streameado el request viejo, y ante
// overflow del contexto compacta el historial (Compactor opcional) y reintenta
// una vez por errContinueAfterCompaction. M8 aterrizo la interrupcion por ctx
// cancelado, ProviderError/Step.Failed, el cierre de tools no resueltas del turno
// y failInterruptedTools para reanudar limpiando tools colgadas. M9/M10 siguen
// siendo Wails/provider/store real.
package runner
