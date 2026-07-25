// Package llm is the published contract with a model provider.
//
// An adapter implements Provider and nothing else. Everything else here is the
// neutral domain model that contract speaks — Request, Message, ToolDef, Event,
// Usage — kept deliberately free of any vendor wire format, so an adapter
// translates its own SDK at the edge without leaking it into the agent loop.
// ToolDef.Schema stays raw JSON Schema end to end for the same reason.
//
// What is NOT here is intentional: the turn loop, the adapters atenea ships,
// the model catalog and the provider wiring are private under internal/. This
// package is the surface third parties implement, so it is the surface that has
// to stay stable.
package llm
