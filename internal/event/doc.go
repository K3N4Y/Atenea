// Package event aisla la frontera con Wails. El Bus reenvia cada SessionEvent
// durable al canal session:<id> del frontend a traves de una EmitFunc inyectada
// (en produccion runtime.EventsEmit; en tests un fake), y PublishError manda el
// error duro que corta una corrida al canal session:<id>:error. El EmittingStore
// decora un session.Store: tras cada AppendEvent exitoso publica el evento ya
// sellado (Seq/SessionID) al Bus, asi la UI observa el log durable como un stream
// sin que la logica del loop conozca Wails. Es la pieza de cableado de M9.
package event
