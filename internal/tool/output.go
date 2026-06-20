package tool

import "sync"

// OutputStore acota el output de cada tool call y guarda el completo por callID. El
// loop pone en el historial (que el modelo ve) el Output acotado; el completo
// queda referenciable para la UI o una re-lectura. Cumple "output grande se acota
// fuera del mensaje via un ToolOutputStore" (ver docs/atenea-agent-loop.md). Es
// seguro para uso concurrente: en M5 varias goroutines de settle escriben a la
// vez.
type OutputStore struct {
	limit int
	mu    sync.Mutex
	full  map[string]string
}

// NewOutputStore crea el store con el limite de bytes que vera el modelo. Un
// limit <= 0 desactiva el acotado (todo el output pasa tal cual), util en tests.
func NewOutputStore(limit int) *OutputStore {
	return &OutputStore{limit: limit, full: make(map[string]string)}
}

// Cap guarda el output completo bajo callID y devuelve el Result que vera el
// modelo: el output entero si cabe, o los primeros limit bytes con Truncated =
// true. El completo siempre queda en el store, recuperable con Full.
func (s *OutputStore) Cap(callID, output string) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.full[callID] = output
	if s.limit > 0 && len(output) > s.limit {
		return Result{Output: output[:s.limit], Truncated: true}
	}
	return Result{Output: output}
}

// Full devuelve el output completo guardado para un callID.
func (s *OutputStore) Full(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.full[callID]
	return v, ok
}
