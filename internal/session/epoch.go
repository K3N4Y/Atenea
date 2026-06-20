package session

// ContextEpoch es una foto del contexto de la sesion que el runner usa para
// detectar cambios concurrentes entre preparar un turno y llamar al proveedor.
// Agent y Model identifican la configuracion activa del turno (el modelo del epoch
// es el que el runner pone en el Request). Revision se incrementa cuando el contexto
// cambia de una forma que invalida un request ya preparado (cambio de agente/modelo,
// reconciliacion de archivos/instrucciones). BaselineSeq marca desde donde cuenta el
// historial proyectado del turno (Store.Messages lee con sinceSeq = BaselineSeq):
// una compaction futura lo avanza para dejar fuera lo ya resumido.
//
// Es comparable a proposito (solo campos comparables): el runner decide el rebuild
// con un simple after != before. M7 lo usa minimo: MemoryStore.Epoch devuelve un
// epoch estable en cero, asi el camino feliz no reconstruye nunca; el driver real
// (que mueve Agent/Model/Revision/BaselineSeq por cambios de contexto) llega con el
// store real (M10).
type ContextEpoch struct {
	Agent       string
	Model       string
	BaselineSeq Seq
	Revision    int
}
