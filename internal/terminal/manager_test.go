package terminal

import "testing"

// Varias sesiones conviven y son independientes: cerrar una no afecta a la otra.
func TestManager_IndependentSessions(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()
	ca, cb := &collector{}, &collector{}
	if err := m.Start("a", "cat", nil, 80, 24, ca.write); err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if err := m.Start("b", "cat", nil, 80, 24, cb.write); err != nil {
		t.Fatalf("Start b: %v", err)
	}
	if err := m.Write("a", []byte("alpha\n")); err != nil {
		t.Fatalf("Write a: %v", err)
	}
	if err := m.Write("b", []byte("bravo\n")); err != nil {
		t.Fatalf("Write b: %v", err)
	}
	ca.waitFor(t, "alpha")
	cb.waitFor(t, "bravo")

	// Cerrar "a" no debe tocar a "b".
	if err := m.Close("a"); err != nil {
		t.Fatalf("Close a: %v", err)
	}
	if err := m.Write("b", []byte("still\n")); err != nil {
		t.Fatalf("b deberia seguir viva: %v", err)
	}
	cb.waitFor(t, "still")
}

// Tras cerrar una sesion, escribirle falla (se removio del registro).
func TestManager_WriteAfterCloseErrors(t *testing.T) {
	m := NewManager()
	c := &collector{}
	if err := m.Start("x", "cat", nil, 80, 24, c.write); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Close("x"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Write("x", []byte("y")); err == nil {
		t.Fatal("Write a sesion cerrada deberia fallar")
	}
}

// Edge: arrancar con un id ya en uso reemplaza la sesion (cierra la vieja).
func TestManager_StartReplaces(t *testing.T) {
	m := NewManager()
	defer m.CloseAll()
	if err := m.Start("dup", "cat", nil, 80, 24, (&collector{}).write); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	c := &collector{}
	if err := m.Start("dup", "sh", []string{"-c", "printf READY"}, 80, 24, c.write); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	c.waitFor(t, "READY")
}

// CloseAll cierra todas: ninguna queda escribible.
func TestManager_CloseAll(t *testing.T) {
	m := NewManager()
	if err := m.Start("a", "cat", nil, 80, 24, (&collector{}).write); err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if err := m.Start("b", "cat", nil, 80, 24, (&collector{}).write); err != nil {
		t.Fatalf("Start b: %v", err)
	}
	m.CloseAll()
	if err := m.Write("a", []byte("x")); err == nil {
		t.Fatal("a deberia estar cerrada")
	}
	if err := m.Write("b", []byte("x")); err == nil {
		t.Fatal("b deberia estar cerrada")
	}
}
