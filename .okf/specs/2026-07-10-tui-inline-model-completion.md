---
updated_at: 2026-07-10
summary: Approved design for selecting provider models through the existing inline slash-command popup.
---

# Design: inline `/model` completion

## Objective

Replace the full-screen `/model` modal with contextual completion inside the
existing composer popup. Model selection behaves like slash-command
completion: the first Enter or Tab writes an explicit command into the
composer, and a second Enter executes it.

## Interaction

- Typing `/` shows `/model` among the built-in slash commands.
- Completing `/model` produces `/model ` and immediately changes the same
  popup to model-search mode.
- Typing any free-form query after `/model ` filters across provider IDs,
  provider names, and complete model IDs, case-insensitively.
- A query such as `chatgpt5.5` may show matching rows from OpenRouter and
  OpenAI simultaneously.
- Each result displays enough provider identity to distinguish duplicates,
  but selection writes `/model <provider-id> <model-id> `.
- The first Enter or Tab on a model result only completes that canonical text.
  It does not persist, switch providers, append history, or start an LLM run.
- Pressing Enter again executes the canonical command, persists and switches
  the provider/model atomically, clears the composer, and updates the footer.
- The trailing space reserves syntax for a later effort argument:
  `/model <provider-id> <model-id> <effort>`.

## Parsing and matching

- Execution accepts exactly `/model <provider-id> <model-id>` plus surrounding
  whitespace. Extra arguments are rejected until effort support ships.
- Provider and model identities are case-sensitive when executing; filtering
  is case-insensitive.
- Duplicate model IDs remain visible when they belong to different providers.
- Every result row is one selectable provider/model pair; provider headings
  are not separate rows.
- Results retain the existing menu limit and keyboard behavior: Up/Down
  cycles, Escape closes, and Enter/Tab applies completion.

## Architecture

- Extend `menuItem` with optional provider/model identity rather than creating
  fake `command.Command` values.
- `refreshMenu` detects either the slash-command token or the text after the
  `/model ` prefix and sources model rows from the optional `modelAgent`.
- `applySelection` writes normal command/file completion or the canonical
  model command according to item type.
- `submitPrompt` parses and executes canonical `/model` commands through
  `SelectModel` before normal prompt history or inbox handling.
- `ModelsRefreshedMsg` refreshes an open inline model popup.
- Remove `modelSelector`, its modal renderer, modal keyboard routing, and
  modal-only styles and tests.

## Error behavior

- Failed selection keeps the canonical command in the composer and exposes the
  error through the existing local-error path.
- Empty catalogs show a non-selectable `No models available` row.
- Queries without matches show a non-selectable `No matches` row.
- Background discovery remains non-blocking and updates the inline popup
  without erasing configured or cached results after warnings.

## Acceptance

- `/model` never opens a modal or replaces the chat view.
- `/model ` shows models in the same popup location and style as commands.
- Free-form queries show matching models from all providers.
- First Enter/Tab completes `/model <provider-id> <model-id> `.
- Second Enter switches and persists the selection.
- `/model` never enters prompt history or durable LLM events.
- Existing command, mention, history, explorer, and permission keyboard
  contracts remain unchanged.
