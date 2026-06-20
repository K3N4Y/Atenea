// Package runner aloja el loop del agente: el loop externo Run, runTurn (un
// turno de proveedor) y publish (traduccion de eventos). El publisher
// (publish.go) aterrizo en M3: traduce cada llm.Event a un SessionEvent durable
// con la taxonomia del contrato y bufferiza los deltas para emitir tambien el
// bloque cerrado con el contenido completo. El loop Run/runTurn/consume que lo
// alimenta llega en M5..M8.
package runner
