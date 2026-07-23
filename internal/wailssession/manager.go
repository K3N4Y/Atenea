// Package wailssession owns the durable lifecycle and projections of sessions
// used by the Wails desktop adapter.
package wailssession

import (
	"context"
	"log"
	"strings"
	"time"

	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/session"
)

const (
	auxTurnTimeout    = 30 * time.Second
	titleSystemPrompt = "Genera un titulo muy corto (maximo 6 palabras) para una conversacion que empieza con el mensaje del usuario. Responde SOLO con el titulo, en el idioma del mensaje, sin comillas, sin punto final y sin prefijos."
)

type Titler func(firstMessage string) string

type Config struct {
	Store       session.Store
	Root        func() string
	Forget      func(string)
	Versioner   event.DataVersioner
	Emit        event.EmitFunc
	WatchPeriod time.Duration
}

// Manager concentrates durable session metadata, projections, deletion, and
// cross-process change observation behind one interface.
type Manager struct {
	store       session.Store
	root        func() string
	forget      func(string)
	versioner   event.DataVersioner
	emit        event.EmitFunc
	watchPeriod time.Duration
	titler      Titler
}

func New(cfg Config) *Manager {
	period := cfg.WatchPeriod
	if period <= 0 {
		period = time.Second
	}
	return &Manager{store: cfg.Store, root: cfg.Root, forget: cfg.Forget, versioner: cfg.Versioner, emit: cfg.Emit, watchPeriod: period}
}

func (m *Manager) SetTitler(titler Titler) { m.titler = titler }

// SetWatchPeriod is intended for deterministic tests before Watch starts.
func (m *Manager) SetWatchPeriod(period time.Duration) { m.watchPeriod = period }

func (m *Manager) List(ctx context.Context) ([]session.SessionSummary, error) {
	return m.store.Sessions(ctx)
}

func (m *Manager) History(ctx context.Context, sessionID string) ([]session.SessionEvent, error) {
	return m.store.Events(ctx, sessionID, 0)
}

func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	if m.forget != nil {
		m.forget(sessionID)
	}
	return m.store.DeleteSession(ctx, sessionID)
}

// Watch publishes changes made by another process to the shared SQLite store.
// In-memory stores have no versioner, so Watch is a no-op.
func (m *Manager) Watch(ctx context.Context) {
	if m.versioner == nil || m.emit == nil {
		return
	}
	go event.WatchStore(ctx, m.versioner, m.watchPeriod, func() { m.emit("sessions:changed") })
}

// Turn owns the first-prompt metadata work around one admitted agent turn.
type Turn struct {
	manager   *Manager
	sessionID string
	message   string
	first     bool
}

func (m *Manager) Turn(sessionID, message string) *Turn {
	return &Turn{manager: m, sessionID: sessionID, message: message}
}

// BeforeAdmit captures the initial cwd. It must run immediately before agent
// admission, while the caller's workspace lifecycle lock is held.
func (t *Turn) BeforeAdmit() error {
	if _, err := t.manager.store.LoadSession(context.Background(), t.sessionID); err == nil {
		return nil
	}
	t.first = true
	if _, err := t.manager.store.AppendEvent(context.Background(), t.sessionID, session.SessionEvent{Kind: session.KindSessionCwd, Text: t.manager.root()}); err != nil {
		log.Printf("atenea: no se pudo guardar la carpeta de %s: %v", t.sessionID, err)
	}
	return nil
}

// AfterRun titles only the first prompt and only after the current run finishes.
func (t *Turn) AfterRun(current bool) {
	if !current || !t.first || t.manager.titler == nil {
		return
	}
	title := strings.TrimSpace(t.manager.titler(t.message))
	if title == "" {
		return
	}
	if _, err := t.manager.store.AppendEvent(context.Background(), t.sessionID, session.SessionEvent{Kind: session.KindSessionTitle, Text: title}); err != nil {
		log.Printf("atenea: no se pudo guardar el titulo de %s: %v", t.sessionID, err)
	}
}

// ProviderTitler builds the production titler while keeping provider selection
// live: providerAndModel is evaluated for every title.
func ProviderTitler(providerAndModel func() (llm.Provider, string)) Titler {
	return func(message string) string {
		provider, model := providerAndModel()
		ctx, cancel := context.WithTimeout(context.Background(), auxTurnTimeout)
		defer cancel()
		out, err := provider.Stream(ctx, llm.Request{Model: model, System: titleSystemPrompt, Messages: []llm.Message{{Role: "user", Text: message}}})
		if err != nil {
			return ""
		}
		var b strings.Builder
		for ev := range out {
			if ev.Kind == llm.TextDelta {
				b.WriteString(ev.Text)
			}
		}
		return strings.TrimSpace(b.String())
	}
}
