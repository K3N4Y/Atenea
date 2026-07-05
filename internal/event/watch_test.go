package event

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Estos tests fijan el contrato de WatchStore, el poller que convierte el
// PRAGMA data_version del store en la senal "otro proceso escribio la base"
// para refrescar la sidebar en vivo. Se testea contra fakes DataVersioner
// (jamas contra Wails ni contra SQLite real): notifica en cada cambio de
// version, no notifica sin cambio, y termina al cancelarse el ctx.

// flipVersioner devuelve version 1 en las primeras lecturas y 2 despues:
// simula la escritura de otro proceso entre dos ticks del watcher.
type flipVersioner struct {
	reads atomic.Int64
}

var _ DataVersioner = (*flipVersioner)(nil)

func (f *flipVersioner) DataVersion(ctx context.Context) (int64, error) {
	if f.reads.Add(1) <= 2 {
		return 1, nil
	}
	return 2, nil
}

// TestWatchStore_NotifiesOnVersionChange: cuando el DataVersion cambia entre
// ticks, WatchStore llama onChange. Correr con -race.
func TestWatchStore_NotifiesOnVersionChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan struct{}, 1)
	go WatchStore(ctx, &flipVersioner{}, time.Millisecond, func() {
		select {
		case changed <- struct{}{}:
		default: // ya hay una notificacion pendiente; el test solo necesita una
		}
	})

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStore no llamo onChange tras el cambio de data_version")
	}
}

// countingVersioner devuelve siempre la misma version y avisa por enough
// cuando el watcher lleva al menos 3 lecturas: suficientes ticks para afirmar
// que sin cambio de version no hay notificacion.
type countingVersioner struct {
	reads  atomic.Int64
	once   sync.Once
	enough chan struct{}
}

var _ DataVersioner = (*countingVersioner)(nil)

func (c *countingVersioner) DataVersion(ctx context.Context) (int64, error) {
	if c.reads.Add(1) >= 3 {
		c.once.Do(func() { close(c.enough) })
	}
	return 7, nil
}

// TestWatchStore_DoesNotNotifyWithoutChange: con la version constante, tras
// varios ticks onChange no se llama ni una vez (las lecturas propias del store
// no deben producir refrescos fantasma). Correr con -race.
func TestWatchStore_DoesNotNotifyWithoutChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &countingVersioner{enough: make(chan struct{})}
	var calls atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchStore(ctx, fake, time.Millisecond, func() { calls.Add(1) })
	}()

	select {
	case <-fake.enough:
	case <-time.After(2 * time.Second):
		t.Fatal("el watcher no llego a 3 lecturas de DataVersion a tiempo")
	}
	cancel()
	// Esperar a que el watcher retorne antes de leer calls (sin carreras).
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStore no retorno tras cancelar el ctx")
	}

	if n := calls.Load(); n != 0 {
		t.Fatalf("onChange llamado %d veces sin cambio de version, quiero 0", n)
	}
}

// steppedVersioner avanza la version con las lecturas: 1,1,2,2,3,3,3... (cada
// version se lee dos veces y la 3 queda estable). Simula DOS escrituras
// externas separadas por varios ticks del watcher. Cierra enough cuando lleva
// al menos 8 lecturas: ticks de sobra en la version estable para afirmar que
// no hay notificaciones extra.
type steppedVersioner struct {
	reads  atomic.Int64
	once   sync.Once
	enough chan struct{}
}

var _ DataVersioner = (*steppedVersioner)(nil)

func (s *steppedVersioner) DataVersion(ctx context.Context) (int64, error) {
	n := s.reads.Add(1)
	if n >= 8 {
		s.once.Do(func() { close(s.enough) })
	}
	if v := (n + 1) / 2; v < 3 {
		return v, nil
	}
	return 3, nil
}

// TestWatchStore_NotifiesOnEachChange: cada cambio de version produce SU
// notificacion (dos cambios -> dos onChange) y la version estable posterior no
// produce ninguna extra. Tumbaria un watcher que notifica una sola vez y deja
// de mirar, o que no actualiza el baseline tras notificar (notificaria en cada
// tick de la version estable). Correr con -race.
func TestWatchStore_NotifiesOnEachChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &steppedVersioner{enough: make(chan struct{})}
	changed := make(chan struct{}, 16)
	var calls atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchStore(ctx, fake, time.Millisecond, func() {
			calls.Add(1)
			changed <- struct{}{}
		})
	}()

	for i := 1; i <= 2; i++ {
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatalf("no llego la notificacion #%d: el watcher debe notificar en CADA cambio de version, no solo en el primero", i)
		}
	}

	// Dejar correr al watcher varios ticks sobre la version estable y esperar a
	// que retorne antes de leer calls (sin carreras).
	select {
	case <-fake.enough:
	case <-time.After(2 * time.Second):
		t.Fatal("el watcher no llego a 8 lecturas de DataVersion a tiempo")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStore no retorno tras cancelar el ctx")
	}

	if n := calls.Load(); n != 2 {
		t.Fatalf("onChange llamado %d veces, quiero exactamente 2 (una por cambio de version; el baseline debe actualizarse tras notificar)", n)
	}
}

// TestWatchStore_StopsOnCancel: cancelar el ctx hace retornar a WatchStore (el
// watcher no queda vivo tras el shutdown de la app). Correr con -race.
func TestWatchStore_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchStore(ctx, &countingVersioner{enough: make(chan struct{})}, time.Millisecond, func() {})
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStore no retorno tras cancelar el ctx")
	}
}
