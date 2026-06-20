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
// llegan en M7.
package runner
