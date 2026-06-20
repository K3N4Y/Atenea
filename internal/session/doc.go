// Package session concentra el dominio durable del agente. M1 define los tipos
// base (Seq, Role, Message, SessionEvent, Session) y el Store: un log de eventos
// append-only como unica fuente de verdad, con los mensajes derivados por
// proyeccion. M3 suma la taxonomia de streaming de forma aditiva sobre
// SessionEvent (EventKind con las constantes Step.* / Text.* / Reasoning.* /
// Tool.* y el tipo Usage). M5 suma, de forma aditiva sobre SessionEvent, el campo
// Error (el mensaje de fallo de una tool, Tool.Failed; M8 lo reutiliza para
// Step.Failed). M6 suma, de forma aditiva, el Inbox: el input durable de la
// sesion (queue/steer) detras de una interface, con la implementacion en memoria
// MemoryInbox (dos colas FIFO por sesion); el runner lo drena y promueve a
// mensajes del historial. M7 suma el ContextEpoch: la foto del contexto del
// turno (Agent, Model, BaselineSeq, Revision) que el runner compara para detectar
// cambios concurrentes entre preparar un turno y llamar al proveedor, expuesta por
// Store.Epoch (MemoryStore devuelve un epoch estable en cero, asi el camino feliz
// no reconstruye; el driver real del epoch llega en M10). M8 suma
// PendingToolCalls como proyeccion durable de Tool.Called sin Tool.Success ni
// Tool.Failed posterior; el runner la usa al reanudar tras crash para cerrar
// tools colgadas antes de abrir el siguiente turno. M10 suma SQLiteStore: la
// implementacion durable del Store (log de eventos en SQLite) detras de la misma
// interface, intercambiable con MemoryStore y validada por el mismo contrato.
package session
