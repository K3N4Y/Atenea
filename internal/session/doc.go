// Package session concentra el dominio durable del agente. M1 define los tipos
// base (Seq, Role, Message, SessionEvent, Session) y el Store: un log de eventos
// append-only como unica fuente de verdad, con los mensajes derivados por
// proyeccion. M3 suma la taxonomia de streaming de forma aditiva sobre
// SessionEvent (EventKind con las constantes Step.* / Text.* / Reasoning.* /
// Tool.* y el tipo Usage). Inbox, historial proyectado y epoch llegan en hitos
// posteriores.
package session
