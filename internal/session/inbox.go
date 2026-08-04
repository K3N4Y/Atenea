package session

import (
	"context"
	"sync"
)

// Delivery clasifica como entra un prompt durable al runner. queue es el prompt
// principal (uno por actividad abierta); steer es direccionamiento aceptado
// mientras la sesion ya esta corriendo (entra en la siguiente continuacion). El
// valor cero DeliveryNone significa "nada que promover": Run lo usa cuando corre
// con force sin input pendiente, y promote lo trata como no-op.
type Delivery int

const (
	DeliveryNone  Delivery = iota // sin entrega: no hay nada que promover
	DeliveryQueue                 // prompt principal en cola (uno por actividad)
	DeliverySteer                 // direccionamiento aceptado durante la actividad
)

// Prompt is user content admitted into the inbox.
type Prompt struct {
	Text   string
	Images []Image
}

// Inbox es el input durable de la sesion. Admit no bloquea y es durable; el loop
// (Run) drena el inbox cuando corre. M6 implementa MemoryInbox; M10 puede agregar
// una version persistente detras de esta misma interface, igual que Store.
type Inbox interface {
	// Admit agrega p al input pendiente de sessionID con la entrega d.
	Admit(ctx context.Context, sessionID string, p Prompt, d Delivery) error

	// HasPending informa si hay algun prompt pendiente de la entrega d.
	HasPending(ctx context.Context, sessionID string, d Delivery) (bool, error)

	// Promote saca de pendientes los prompts de la entrega d que entran al proximo
	// turno y los devuelve en orden de admision: queue saca el SIGUIENTE prompt
	// encolado (FIFO, uno solo); steer saca TODOS los steers pendientes. El runner
	// materializa cada prompt devuelto como un Message{Role: user} en el Store
	// antes de leer el historial. DeliveryNone (o sin pendientes) devuelve nil.
	Promote(ctx context.Context, sessionID string, d Delivery) ([]Prompt, error)
}

// MemoryInbox es la implementacion en memoria del Inbox para M6..M9. Guarda dos
// colas FIFO por sesion (queue y steer) bajo un mutex.
type MemoryInbox struct {
	mu    sync.Mutex
	queue map[string][]Prompt
	steer map[string][]Prompt
}

// NewMemoryInbox crea un inbox vacio listo para usar.
func NewMemoryInbox() *MemoryInbox {
	return &MemoryInbox{queue: map[string][]Prompt{}, steer: map[string][]Prompt{}}
}

// var _ Inbox = (*MemoryInbox)(nil) asegura en compilacion que cumple la interface.
var _ Inbox = (*MemoryInbox)(nil)

func clonePrompt(prompt Prompt) Prompt {
	prompt.Images = cloneImages(prompt.Images)
	return prompt
}

func (in *MemoryInbox) Admit(ctx context.Context, sessionID string, p Prompt, d Delivery) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	p = clonePrompt(p)
	switch d {
	case DeliveryQueue:
		in.queue[sessionID] = append(in.queue[sessionID], p)
	case DeliverySteer:
		in.steer[sessionID] = append(in.steer[sessionID], p)
	}
	return nil
}

func (in *MemoryInbox) HasPending(ctx context.Context, sessionID string, d Delivery) (bool, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch d {
	case DeliveryQueue:
		return len(in.queue[sessionID]) > 0, nil
	case DeliverySteer:
		return len(in.steer[sessionID]) > 0, nil
	}
	return false, nil
}

func (in *MemoryInbox) Promote(ctx context.Context, sessionID string, d Delivery) ([]Prompt, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch d {
	case DeliveryQueue:
		q := in.queue[sessionID]
		if len(q) == 0 {
			return nil, nil
		}
		next := clonePrompt(q[0])
		in.queue[sessionID] = q[1:]
		return []Prompt{next}, nil
	case DeliverySteer:
		s := in.steer[sessionID]
		if len(s) == 0 {
			return nil, nil
		}
		in.steer[sessionID] = nil
		out := make([]Prompt, len(s))
		for i := range s {
			out[i] = clonePrompt(s[i])
		}
		return out, nil
	}
	return nil, nil
}
