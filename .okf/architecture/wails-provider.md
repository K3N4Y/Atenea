---
updated_at: 2026-07-22
summary: Provider lifecycle seam used by the Wails desktop adapter.
---

# Wails provider lifecycle

The desktop frontend's legacy provider selector is implemented by
`internal/wailsprovider.Manager`. The module owns provider validation,
construction, credential resolution, model discovery, and the synchronized
active provider/configuration snapshot.

`main.App` remains the Wails adapter. Its public `Model`, `ProviderConfig`,
`SetProvider`, and `ListModels` bindings are unchanged, but they delegate
provider state to the manager. When a selection succeeds, `App` uses the
manager's complete snapshot to rebuild workspace-dependent agent wiring. A
validation failure leaves both provider and configuration unchanged.
Provider mutation and workspace wiring publication are serialized by
`internal/wailsworkspace.Manager`, so no prompt is admitted between them.

The seam consumed by agent wiring is `Manager.Snapshot`: it returns the active
`llm.Provider`, its secret-free configuration, and whether local-model prompt
rules apply. Keeping these values in one snapshot prevents readers from seeing
a provider paired with stale configuration during a live selection change.

Credentials are not part of the snapshot or the frontend contract. OpenRouter
continues to resolve `OPENROUTER_API_KEY` first and the shared `/connect`
credential store second. Local OpenAI-compatible endpoints remain keyless.

This module is intentionally separate from `internal/providerconfig.Service`.
That service is the richer persisted multi-provider catalog used by the TUI;
the Wails frontend still exposes its existing OpenRouter/local selector. The
two can converge later by changing the adapter without returning provider
lifecycle state to `App`.
