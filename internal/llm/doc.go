// Package llm define la frontera con los proveedores de modelo. M2 fija el
// contrato: la interface Provider, los tipos Request, Event, EventKind y Usage,
// y un FakeProvider scriptable que reproduce un guion determinista de eventos
// por un channel para tests sin red. El adaptador real (Claude/Anthropic) entra
// en M10 detras de esta misma interface. M4 sumo, de forma aditiva, el tipo
// ToolDef: el esquema anunciable de una tool que el registry (internal/tool)
// materializa y que M5 pondra en Request.Tools. M5 hizo crecer el Request de
// forma aditiva con Messages (el historial proyectado en el formato del
// proveedor, via el nuevo tipo Message) y Tools (las defs materializadas), y sumo
// el campo Event.ProviderExecuted, que marca una tool call que el proveedor
// ejecuto el mismo.
package llm
