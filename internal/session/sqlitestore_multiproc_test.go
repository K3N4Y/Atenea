package session

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Estos tests fijan el contrato multi-proceso del SQLiteStore: la TUI y la app
// Wails comparten el mismo archivo .db, cada una con su PROPIO NewSQLiteStore
// (pool de conexiones independiente, como dos procesos distintos). Hoy fallan:
// el DSN de archivo no habilita WAL ni busy_timeout (los escritores chocan con
// SQLITE_BUSY) y AppendEvent hace SELECT MAX(seq) + INSERT no atomico entre
// procesos (racea el Seq con UNIQUE constraint sobre la misma sesion).

// TestSQLiteStore_SharedFile_ConcurrentAppendsAcrossStores abre dos stores
// sobre el mismo archivo y appendea concurrentemente a sesiones DISTINTAS.
// Ningun AppendEvent debe fallar (hoy: SQLITE_BUSY / database is locked), y al
// final cada store debe ver ambas sesiones.
func TestSQLiteStore_SharedFile_ConcurrentAppendsAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	storeA, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (A): %v", err)
	}
	t.Cleanup(func() { storeA.Close() })
	storeB, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (B): %v", err)
	}
	t.Cleanup(func() { storeB.Close() })

	const n = 50
	errs := make(chan error, 2*n)
	start := make(chan struct{}) // barrera: ambos escritores arrancan a la vez
	var wg sync.WaitGroup
	for _, w := range []struct {
		store     *SQLiteStore
		sessionID string
	}{
		{storeA, "proc-a"},
		{storeB, "proc-b"},
	} {
		wg.Add(1)
		go func(store *SQLiteStore, sessionID string) {
			defer wg.Done()
			<-start
			for i := 0; i < n; i++ {
				ev := SessionEvent{Message: &Message{
					ID: fmt.Sprintf("%s-m%d", sessionID, i), Role: RoleUser, Text: "hola",
				}}
				if _, err := store.AppendEvent(ctx, sessionID, ev); err != nil {
					errs <- fmt.Errorf("AppendEvent(%s #%d): %w", sessionID, i, err)
					return
				}
			}
		}(w.store, w.sessionID)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("append entre stores: %v", err)
	}
	if t.Failed() {
		return
	}

	// Ambos stores leen el mismo archivo: cada uno debe ver las dos sesiones.
	for name, store := range map[string]*SQLiteStore{"A": storeA, "B": storeB} {
		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions (store %s): %v", name, err)
		}
		seen := make(map[string]bool, len(got))
		for _, s := range got {
			if s.LastActivity.IsZero() || s.LastActivity.Location() != time.UTC {
				t.Fatalf("Sessions (store %s): LastActivity invalido en %+v", name, s)
			}
			seen[s.ID] = true
		}
		if !seen["proc-a"] || !seen["proc-b"] {
			t.Fatalf("Sessions (store %s): got %+v, want proc-a y proc-b", name, got)
		}
	}
}

// TestSQLiteStore_SharedFile_ConcurrentAppendsSameSession abre dos stores sobre
// el mismo archivo y appendea concurrentemente a la MISMA sesion. Todos los
// appends deben tener exito y los Seq resultantes deben ser unicos y contiguos
// 1..100. Hoy el SELECT MAX(seq) + INSERT de AppendEvent no es atomico entre
// procesos: dos stores leen el mismo MAX y chocan (UNIQUE constraint o BUSY).
func TestSQLiteStore_SharedFile_ConcurrentAppendsSameSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	storeA, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (A): %v", err)
	}
	t.Cleanup(func() { storeA.Close() })
	storeB, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (B): %v", err)
	}
	t.Cleanup(func() { storeB.Close() })

	const n = 50
	const sessionID = "shared"
	errs := make(chan error, 2*n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, store := range []*SQLiteStore{storeA, storeB} {
		wg.Add(1)
		go func(store *SQLiteStore) {
			defer wg.Done()
			<-start
			for i := 0; i < n; i++ {
				if _, err := store.AppendEvent(ctx, sessionID, SessionEvent{Kind: KindStepStarted}); err != nil {
					errs <- fmt.Errorf("AppendEvent #%d: %w", i, err)
					return
				}
			}
		}(store)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("append a la misma sesion: %v", err)
	}
	if t.Failed() {
		return
	}

	// El log compartido debe tener los 100 eventos con Seq estrictamente 1..100.
	got, err := storeA.Events(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 2*n {
		t.Fatalf("Events: got %d eventos, want %d", len(got), 2*n)
	}
	for i, ev := range got {
		if ev.Seq != Seq(i+1) {
			t.Fatalf("Events[%d].Seq = %d, want %d (Seqs con huecos o duplicados)", i, ev.Seq, i+1)
		}
	}
}

func TestSQLiteStore_LegacyInsertTriggerTimestampsAfterWriteLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	locker, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (locker): %v", err)
	}
	t.Cleanup(func() { locker.Close() })
	writer, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (writer): %v", err)
	}
	t.Cleanup(func() { writer.Close() })

	tx, err := locker.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_context (session_id) VALUES ('write-lock')`); err != nil {
		tx.Rollback()
		t.Fatalf("acquire write lock: %v", err)
	}

	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := writer.db.ExecContext(ctx,
			`INSERT INTO events (session_id, seq, kind, has_message) VALUES ('blocked', 1, '', 0)`,
		)
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		tx.Rollback()
		t.Fatalf("legacy insert returned before write lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	lowerBound := time.UnixMilli(time.Now().UTC().UnixMilli()).UTC()
	if err := tx.Commit(); err != nil {
		t.Fatalf("release write lock: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("legacy insert after write lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy insert remained blocked after write lock release")
	}

	summaries, err := writer.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if got := summaries[0].LastActivity; got.Before(lowerBound) {
		t.Fatalf("LastActivity = %v, want timestamp sampled after lock release %v", got, lowerBound)
	}
}

// TestSQLiteStore_SharedFile_WriterClosesReaderStillReads: el escritor (la app)
// escribe una sesion y CIERRA su store (el cierre en WAL dispara el checkpoint
// del -wal hacia la base); el lector (la TUI), abierto ANTES de ese cierre
// sobre el mismo archivo, debe seguir leyendo la sesion completa via Events y
// verla en Sessions. Tumbaria una implementacion donde el estado quedara solo
// en el -wal y el Close lo perdiera, o donde las conexiones vivas del lector
// quedaran clavadas en un snapshot anterior al cierre.
func TestSQLiteStore_SharedFile_WriterClosesReaderStillReads(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	writer, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (writer): %v", err)
	}
	// El lector se abre ANTES del Close del escritor: sus conexiones ya existen
	// cuando el checkpoint ocurre, como la TUI viva mientras la app se cierra.
	reader, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (reader): %v", err)
	}
	t.Cleanup(func() { reader.Close() })

	in := []SessionEvent{
		{Kind: KindSessionCwd, Text: "/home/u/proj"},
		{Message: &Message{ID: "m1", Role: RoleUser, Text: "hola desde la app"}},
		{Kind: KindToolCalled, CallID: "c1", ToolName: "read"},
		{Kind: KindToolSuccess, CallID: "c1", ToolName: "read", Message: &Message{ID: "c1", Role: RoleTool, Text: "contenido", ToolCallID: "c1"}},
		{Kind: KindStepEnded, Message: &Message{ID: "a1", Role: RoleAssistant, Text: "listo"}},
	}
	for i, ev := range in {
		if _, err := writer.AppendEvent(ctx, "compartida", ev); err != nil {
			t.Fatalf("AppendEvent #%d (writer): %v", i, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close (writer): %v", err)
	}

	// El lector debe reconstruir el log COMPLETO tras el cierre del escritor.
	got, err := reader.Events(ctx, "compartida", 0)
	if err != nil {
		t.Fatalf("Events (reader) tras el Close del writer: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("Events (reader): got %d eventos, want %d (%+v)", len(got), len(in), got)
	}
	for i := range in {
		want := in[i]
		want.SessionID = "compartida"
		want.Seq = Seq(i + 1)
		if !reflect.DeepEqual(got[i], want) {
			t.Fatalf("Events[%d] (reader): got %+v, want %+v", i, got[i], want)
		}
	}

	// Y la proyeccion Sessions tambien la ve, con titulo y carpeta vigentes.
	sums, err := reader.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions (reader) tras el Close del writer: %v", err)
	}
	want := []sessionSummaryProjection{{ID: "compartida", Title: "hola desde la app", Cwd: "/home/u/proj"}}
	if projected := projectSessionSummaries(sums); !reflect.DeepEqual(projected, want) {
		t.Fatalf("Sessions (reader): got %+v, want %+v", sums, want)
	}
	if sums[0].LastActivity.IsZero() || sums[0].LastActivity.Location() != time.UTC {
		t.Fatalf("Sessions (reader): LastActivity invalido en %+v", sums[0])
	}
}

// Los dos tests DataVersion_* fijan el contrato de la senal barata que alimenta
// el refresco en vivo de la sidebar: DataVersion expone PRAGMA data_version, que
// cambia cuando OTRA conexion (tipicamente otro proceso, como la TUI) modifica
// la base, y NO cambia por las escrituras de la propia conexion (el pool esta
// clavado en 1 conexion, asi que "propia conexion" == "propio store").

// TestSQLiteStore_DataVersion_ChangesOnExternalWrite: una escritura hecha por
// OTRO store (otro pool sobre el mismo archivo, como otro proceso) debe mover
// el DataVersion observado por el primero.
func TestSQLiteStore_DataVersion_ChangesOnExternalWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	storeA, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (A): %v", err)
	}
	t.Cleanup(func() { storeA.Close() })
	storeB, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (B): %v", err)
	}
	t.Cleanup(func() { storeB.Close() })

	v1, err := storeA.DataVersion(ctx)
	if err != nil {
		t.Fatalf("DataVersion (antes): %v", err)
	}

	// El "otro proceso" escribe la base compartida.
	if _, err := storeB.AppendEvent(ctx, "tui", SessionEvent{Kind: KindStepStarted}); err != nil {
		t.Fatalf("AppendEvent (B): %v", err)
	}

	v2, err := storeA.DataVersion(ctx)
	if err != nil {
		t.Fatalf("DataVersion (despues): %v", err)
	}
	if v2 == v1 {
		t.Fatalf("DataVersion = %d antes y despues de la escritura externa; debe cambiar", v1)
	}
}

// TestSQLiteStore_DataVersion_StableOnOwnWrite: las escrituras del PROPIO store
// no mueven su DataVersion; asi la app no se auto-dispara el refresco de la
// sidebar por sus propios appends (que ya viajan en vivo por el bus).
func TestSQLiteStore_DataVersion_StableOnOwnWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v1, err := store.DataVersion(ctx)
	if err != nil {
		t.Fatalf("DataVersion (antes): %v", err)
	}

	if _, err := store.AppendEvent(ctx, "propia", SessionEvent{Kind: KindStepStarted}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	v2, err := store.DataVersion(ctx)
	if err != nil {
		t.Fatalf("DataVersion (despues): %v", err)
	}
	if v1 != v2 {
		t.Fatalf("DataVersion cambio de %d a %d por una escritura propia; debe ser estable", v1, v2)
	}
}

// TestSQLiteStore_FileDSN_EnablesWAL verifica que abrir un store sobre archivo
// deja la base en journal_mode WAL, el unico modo que permite lectores y un
// escritor de procesos distintos sin SQLITE_BUSY. WAL es persistente en el
// archivo, asi que una conexion independiente debe verlo. Hoy el DSN no lo
// activa y la base queda en "delete".
func TestSQLiteStore_FileDSN_EnablesWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// Un append fuerza la creacion fisica de la base en disco.
	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindStepStarted}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Conexion independiente (simula el otro proceso): el modo WAL persistido
	// en el archivo debe ser visible sin ninguna configuracion extra.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open (conexion independiente): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want %q (la base no quedo en WAL)", mode, "wal")
	}
}
