// Package llm define la frontera con los proveedores de modelo. M2 fija el
// contrato: la interface Provider, los tipos Request, Event, EventKind y Usage,
// y un FakeProvider scriptable que reproduce un guion determinista de eventos
// por un channel para tests sin red. El adaptador real (Claude/Anthropic) entra
// en M10 detras de esta misma interface.
package llm
