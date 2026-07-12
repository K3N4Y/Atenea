package session

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Estos tests fijan el contrato de los helpers compartidos de apertura del
// store (DefaultDBPath/OpenDefault), la fuente unica que usan la TUI y la app
// Wails (openStore en app.go): ambas resuelven y abren el MISMO SQLite.

func TestDefaultDBPath_UsesEnvOverride(t *testing.T) {
	// ATENEA_DB seteada gana siempre y se devuelve tal cual (util en dev).
	want := filepath.Join(t.TempDir(), "custom.db")
	t.Setenv("ATENEA_DB", want)

	if got := DefaultDBPath(); got != want {
		t.Fatalf("DefaultDBPath() = %q, quiero la ruta de ATENEA_DB tal cual: %q", got, want)
	}
}

func TestDefaultDBPath_DefaultsToUserConfigDir(t *testing.T) {
	// Sin ATENEA_DB, la ruta es <os.UserConfigDir()>/atenea/atenea.db y el
	// directorio <config>/atenea queda creado (MkdirAll). En Linux,
	// os.UserConfigDir respeta XDG_CONFIG_HOME, asi que el test lo redirige a
	// un tempdir para no tocar la config real del usuario.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("os.UserConfigDir no respeta XDG_CONFIG_HOME en %s", runtime.GOOS)
	}
	xdg := t.TempDir()
	t.Setenv("ATENEA_DB", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	want := filepath.Join(xdg, "atenea", "atenea.db")
	if got := DefaultDBPath(); got != want {
		t.Fatalf("DefaultDBPath() = %q, quiero %q (<XDG_CONFIG_HOME>/atenea/atenea.db)", got, want)
	}
	appDir := filepath.Join(xdg, "atenea")
	info, err := os.Stat(appDir)
	if err != nil {
		t.Fatalf("os.Stat(%q) = %v: DefaultDBPath debe crear el directorio de config con MkdirAll", appDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q existe pero no es un directorio", appDir)
	}
}

func TestDefaultCheckpointPath_UsesEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-checkpoints")
	t.Setenv("ATENEA_CHECKPOINTS", want)
	if got := DefaultCheckpointPath(); got != want {
		t.Fatalf("DefaultCheckpointPath() = %q, want %q", got, want)
	}
}

func TestDefaultCheckpointPath_DefaultsToUserConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("os.UserConfigDir does not respect XDG_CONFIG_HOME on %s", runtime.GOOS)
	}
	xdg := t.TempDir()
	t.Setenv("ATENEA_CHECKPOINTS", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	want := filepath.Join(xdg, "atenea", "checkpoints")
	if got := DefaultCheckpointPath(); got != want {
		t.Fatalf("DefaultCheckpointPath() = %q, want %q", got, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", info.Mode().Perm())
	}
}

func TestOpenDefault_OpensSQLiteAtDefaultPath(t *testing.T) {
	// OpenDefault abre el SQLite en la ruta por defecto y devuelve un store
	// durable usable: un round-trip minimo AppendEvent + Sessions y el archivo
	// de la base creado en disco.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")
	t.Setenv("ATENEA_DB", path)

	store, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault() = error %v, se esperaba nil con una ruta escribible", err)
	}
	if store == nil {
		t.Fatal("OpenDefault() = store nil, se esperaba el store SQLite abierto")
	}
	if c, ok := store.(io.Closer); ok {
		t.Cleanup(func() { c.Close() })
	}

	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "hola"}}); err != nil {
		t.Fatalf("AppendEvent = %v, el store abierto debe aceptar eventos", err)
	}
	sums, err := store.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions = %v, se esperaba nil", err)
	}
	if len(sums) != 1 || sums[0].ID != "s1" {
		t.Fatalf("Sessions() = %v, quiero exactamente la sesion s1 recien escrita", sums)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("os.Stat(%q) = %v: OpenDefault debe crear la base SQLite en la ruta por defecto", path, err)
	}
}

func TestOpenDefault_TwoInstancesShareSessions(t *testing.T) {
	// El escenario REAL app+TUI a nivel API: dos procesos llaman OpenDefault con
	// el mismo ATENEA_DB y comparten las sesiones por el archivo. La primera
	// instancia (la app) escribe una sesion; la segunda (la TUI) la ve en
	// Sessions y appendea a la MISMA sesion con el Seq siguiente; y la primera
	// lee el follow-up sin reabrir nada (nadie cachea estado).
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")
	t.Setenv("ATENEA_DB", path)

	open := func(name string) Store {
		t.Helper()
		store, err := OpenDefault()
		if err != nil {
			t.Fatalf("OpenDefault (%s) = error %v, se esperaba nil con una ruta escribible", name, err)
		}
		c, ok := store.(io.Closer)
		if !ok {
			t.Fatalf("OpenDefault (%s) devolvio un %T sin Close, se esperaba el SQLite compartido", name, store)
		}
		t.Cleanup(func() { c.Close() })
		return store
	}
	app := open("app")
	tui := open("tui")

	seq1, err := app.AppendEvent(ctx, "compartida", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "desde la app"}})
	if err != nil {
		t.Fatalf("AppendEvent (app) = %v, se esperaba nil", err)
	}

	sums, err := tui.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions (tui) = %v, se esperaba nil", err)
	}
	if len(sums) != 1 || sums[0].ID != "compartida" {
		t.Fatalf("Sessions (tui) = %+v, la segunda instancia debe ver la sesion escrita por la primera", sums)
	}

	seq2, err := tui.AppendEvent(ctx, "compartida", SessionEvent{Message: &Message{ID: "m2", Role: RoleUser, Text: "desde la tui"}})
	if err != nil {
		t.Fatalf("AppendEvent (tui) a la sesion compartida = %v, se esperaba nil", err)
	}
	if seq2 != seq1+1 {
		t.Fatalf("AppendEvent (tui) = Seq %d, quiero %d: la secuencia debe continuar la de la primera instancia", seq2, seq1+1)
	}

	got, err := app.Messages(ctx, "compartida", 0)
	if err != nil {
		t.Fatalf("Messages (app) = %v, se esperaba nil", err)
	}
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("Messages (app) = %+v, la primera instancia debe ver m1 y m2 en orden (sin cachear estado)", got)
	}
}

func TestOpenDefault_FallsBackToMemoryOnError(t *testing.T) {
	// Si abrir el SQLite falla, OpenDefault devuelve el error Y un store en
	// memoria usable: el caller decide como avisar, pero la sesion no se pierde.
	// El padre de la ruta es un ARCHIVO plano, asi que abrir ahi debe fallar.
	ctx := context.Background()
	plain := filepath.Join(t.TempDir(), "plano")
	if err := os.WriteFile(plain, []byte("no soy un directorio"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) = %v", plain, err)
	}
	t.Setenv("ATENEA_DB", filepath.Join(plain, "sub", "atenea.db"))

	store, err := OpenDefault()
	if err == nil {
		t.Fatal("OpenDefault() = error nil, se esperaba un error: el padre de la ruta es un archivo plano")
	}
	if store == nil {
		t.Fatal("OpenDefault() = store nil, se esperaba el fallback en memoria usable junto al error")
	}
	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "hola"}}); err != nil {
		t.Fatalf("AppendEvent en el fallback = %v, el store en memoria debe ser usable", err)
	}
	if _, err := store.LoadSession(ctx, "s1"); err != nil {
		t.Fatalf("LoadSession en el fallback = %v, la sesion recien escrita debe existir", err)
	}
}
