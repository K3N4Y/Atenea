// Package session concentra el dominio durable del agente. M1 define los tipos
// base (Seq, Role, Message, SessionEvent, Session) y el Store: un log de eventos
// append-only como unica fuente de verdad, con los mensajes derivados por
// proyeccion. Inbox, historial proyectado y epoch llegan en hitos posteriores.
package session
