// Package agentcore is the root of atenea's published contracts. It holds no
// code of its own: it documents the rule its subpackages exist to enforce.
//
// Contracts public, loop private. Everything under agentcore/ is a type or an
// interface a third party implements or reads — a tool (agentcore/tool), a model
// provider (agentcore/llm), the durable event stream (agentcore/session), the
// ask-before-run boundary (agentcore/permission). Everything that runs the agent
// — the turn loop, the stores, the wiring, the UIs, the tools and adapters atenea
// ships — stays under internal/, where it is free to move.
//
// Two invariants keep that promise honest, both enforced by a test in this
// directory:
//
//   - No package under agentcore/ imports internal/. A contract that reaches
//     into the implementation is not a contract.
//   - No package under agentcore/ imports a third-party module. Implementing a
//     contract must not force a dependency on whatever atenea happens to use.
//
// A change that would break either one is a signal that the type belongs on the
// private side.
package agentcore
