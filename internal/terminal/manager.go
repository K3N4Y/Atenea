package terminal

import (
	"errors"
	"sync"
)

// ErrNoSession se devuelve al operar sobre un id que no existe (o ya se cerro).
var ErrNoSession = errors.New("terminal: no existe la sesion")

// Manager guarda varias sesiones pty vivas por id, para que el panel pueda tener
// mas de una terminal a la vez. Cada id tiene su shell y su propio onData.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager crea un registro vacio de sesiones.
func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}}
}

// Start arranca un shell para id. Si ya habia uno con ese id, lo cierra y lo
// reemplaza (re-abrir una tab no acumula procesos).
func (m *Manager) Start(id, name string, args []string, cols, rows uint16, onData func([]byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.sessions[id]; old != nil {
		_ = old.Close()
		delete(m.sessions, id)
	}
	s, err := Start(name, args, cols, rows, onData)
	if err != nil {
		return err
	}
	m.sessions[id] = s
	return nil
}

// Write manda input al shell de id. ErrNoSession si no existe.
func (m *Manager) Write(id string, p []byte) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	_, err := s.Write(p)
	return err
}

// Resize ajusta el tamano del pty de id. ErrNoSession si no existe.
func (m *Manager) Resize(id string, cols, rows uint16) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	return s.Resize(cols, rows)
}

// Close mata el shell de id y lo saca del registro. Idempotente: cerrar un id que
// no existe no es error.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}

// CloseAll cierra todas las sesiones (apagado de la app).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
}
