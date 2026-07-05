package event

import (
	"context"
	"time"
)

// DataVersioner expone la version de datos de un store (PRAGMA data_version en
// SQLite): un contador que cambia cuando OTRA conexion (tipicamente otro
// proceso, como la TUI) modifica la base, y no por las escrituras propias.
type DataVersioner interface {
	DataVersion(ctx context.Context) (int64, error)
}

// WatchStore sondea DataVersion cada interval y llama onChange cuando la
// version cambia: es el puente para que la sidebar se entere de sesiones
// escritas por otro proceso (la TUI) en el SQLite compartido. El emit concreto
// lo decide el caller (la frontera Wails vive fuera de este paquete). La
// lectura inicial fija el baseline; si falla, el baseline se toma en el primer
// tick exitoso. Los errores de lectura se ignoran y se reintenta en el
// siguiente tick. Retorna al cancelarse ctx.
func WatchStore(ctx context.Context, v DataVersioner, interval time.Duration, onChange func()) {
	baseline, err := v.DataVersion(ctx)
	seeded := err == nil

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			version, err := v.DataVersion(ctx)
			if err != nil {
				continue // transitorio: se reintenta en el proximo tick
			}
			if !seeded {
				baseline, seeded = version, true
				continue
			}
			if version != baseline {
				baseline = version
				onChange()
			}
		}
	}
}
