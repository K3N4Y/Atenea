---
name: design-under-constraint
description: Formal vocabulary for design decisions in any medium — interfaces, layering, protocols, UI flows, defaults, forms, workflows. Use when choosing between candidate designs, deciding what to hide or expose, judging whether something is over-specified, placing a layer, standardising a format, or asking whose interest a default serves.
---

# Design Under Constraint

Design is the act of forcing chaos into an order that serves a purpose. The act is the same whether the medium is code, a protocol, a room, or a screen, so the vocabulary below is stated over *states* and *statements* rather than over modules or pages — it must survive the change of medium.

The formalism is adapted from Michael Timothy Bennett's work on weakness and abstraction layers. It is used here as vocabulary that forces precision, not as proof.

## Definitions

**D1 — Situation** `Φ`: the set of all statements that can be made about the thing being designed.

**D2 — State** `s ⊆ Φ`: everything that holds at once. A candidate design, a screen, a running configuration.

**D3 — Extension** `ext(x)`: the set of states in which statement `x` holds.

**D4 — Weakness** `w(x) = |ext(x)|`. A **weak** statement admits many states; a **strong** one admits few. Weak is not vague — it is general.

**D5 — Vocabulary** `V ⊆ Φ`: the statements a participant can express or perceive. What is outside `V` does not exist for that participant.

**D6 — Layer** `L(V)`: everything expressible from `V`. A layer hides `Φ \ V` from whoever stands on it.

**D7 — Task** `α = ⟨I, O⟩`: the inputs the design must accept and the outputs it must produce.

**D8 — Constraint**: any statement that shrinks `ext`. Distance, cost, materials, latency, failure modes, who is holding the phone.

**D9 — Policy** `h`: a design. `h` is **correct for α** when it yields `O` for every `I`.

**D10 — Design**: the choice of the pair `⟨V, h⟩` — first a vocabulary, then a policy expressible in it. Most design failures are `V` failures wearing an `h` costume.

**D11 — Default**: the policy that runs when the participant expresses nothing.

## Principles

**P1 — No constraints, no task.** `α` is undefined until `I` and `O` are written down. "Build a bridge" is not a task; distance, load, ground, wind and budget are what make it one. Design begins at the gap between the state that exists and the state preferred — name both before proposing anything.

**P2 — Competing tasks have no correct answer.** Given `α` and `β` whose correct-policy sets barely intersect, no `h` maximises both: stronger is heavier, safer is dearer, faster is coarser. You are choosing a point, not computing a value. State which task you sacrificed.

**P3 — Among correct policies, take the weakest.** Accuracy that does not shrink the set of failing states is cost without generality. Harry Beck's 1931 tube map dropped every statement about real geometry because none of it discriminated between states satisfying *passenger reaches destination*; the weaker map generalised to every rider and outlived the accurate one. **Precision and usefulness are different quantities.** A design shows what the task needs, not what is true.

**P4 — Complexity is a property of the layer, not the thing.** A billion transistors are intractable at the transistor layer and unremarkable at the gate layer. When something will not fit in one head, choose a `V` in which the task is expressible in few statements, and let each layer expose that `V` upward while hiding the rest. **Deciding where the division goes is the design work**; the pieces are the easy part.

**P5 — Shared vocabulary is what scales, not shared substance.** The shipping container's power is not that it holds cargo — a crate holds cargo. It is that ship, crane, truck and factory each only have to understand one `V`. Five people improvise; five thousand need a `V`. Standardise the interface, never the contents.

**P6 — No `⟨V, h⟩` is neutral.** Every vocabulary makes some tasks cheap and others inexpressible, and D11 decides the outcome for everyone who expresses nothing. A stair admits one body and refuses another; a highway shortens a commute and severs a neighbourhood. **Bad design does not always look broken** — a cancellation flow buried six taps deep is failing the customer's `α` and serving the vendor's perfectly. Ask whose task the default minimises, and say so.

## Running it

1. Write `α = ⟨I, O⟩` and the constraints (D8). If you cannot, stop: there is no task yet, only an idea.
2. Name the competing task `β` and which of the two you are sacrificing (P2).
3. Choose `V` — what the caller, reader or user is allowed to know — before choosing `h` (D10, P4).
4. Delete every statement in `h` that does not shrink the set of failing states (P3).
5. Name the default and whose `α` it serves (P6).

Report the design as `⟨V, h⟩`, the sacrificed `β`, and the default's beneficiary. Three lines is usually enough.
