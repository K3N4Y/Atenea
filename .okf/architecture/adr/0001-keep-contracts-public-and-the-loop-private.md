---
updated_at: 2026-07-27
summary: Publish only the contracts third parties implement or consume, while keeping Atenea's agent loop and shipped implementations private.
---

# ADR 0001: Keep contracts public and the loop private

## Status

Accepted on 2026-07-24.

## Context

Atenea needs stable seams for independently developed tools, model providers,
permission behavior, and durable-event consumers. Publishing the runner, stores,
wiring, UIs, or shipped adapters as a Go API would also turn their current shapes
into compatibility promises. Those implementations are still changing, while
the small interfaces and data types at their boundaries are the parts an
integrator actually needs.

## Decision

Publish third-party-facing contracts under `agentcore/`: types and interfaces
that another module implements or reads, plus contract test kits where a type
alone cannot express the behavioral promise. Keep the agent loop, composition,
storage, projections, and Atenea's implementations under `internal/`.

Contracts use only the Go standard library and never import `internal/`. A new
type crosses into `agentcore/` only when an external implementer or consumer
needs it; host-side extension points and implementation conveniences remain
private. `agentcore/boundary_test.go` enforces the dependency direction.

## Consequences

- Third parties can compile against small, intentional contracts without taking
  dependencies on Atenea's implementation stack.
- Atenea can refactor the loop and shipped implementations without treating
  those changes as public API breaks.
- Published contracts require deliberate compatibility management and tests;
  moving a type into `agentcore/` is a long-lived commitment.
- Importable contracts extend Atenea, but do not provide a supported API for
  embedding or driving the loop. The headless CLI is the language-agnostic
  automation surface.

## Rejected alternatives

- **Publish the runner and stores as an SDK.** This freezes implementation
  details that external code does not need and that Atenea must remain free to
  evolve.
- **Keep every package under `internal/`.** This makes even narrow tool and
  provider implementations impossible without a fork.
- **Expose concrete implementations instead of contracts.** This couples
  integrations to dependencies and lifecycle choices rather than behavior.

## Further reading

- [Published contracts](../public-contracts.md)
- [Agnosticism and extensibility audit](../../audits/2026-07-24-agnostic-extensibility-audit.md#r1--make-the-module-consumable-and-publish-contracts-only)
