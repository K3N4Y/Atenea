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
// mensajes del historial. El historial proyectado avanzado y el epoch llegan en
// hitos posteriores (M7).
package session
