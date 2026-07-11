# TUI Chat Composer Visual Refresh

## Summary

Refresh the TUI chat composer so its visual hierarchy closely matches the
approved reference: a quiet rounded input frame, an emphasized prompt on the
left, and the active model embedded into the lower-right edge of the frame.
The change is visual only and preserves the current composer behavior.

## Goals

- Make the composer frame lighter and less visually dominant than the input.
- Keep the `❯` prompt and cursor clearly aligned inside the frame.
- Render the active model as a label interrupting the lower-right border.
- Preserve the existing token usage display in the upper composer border.
- Keep multiline input, autocomplete, focus, submission, and mouse behavior
  unchanged.
- Render safely without horizontal overflow in narrow terminals.

## Out of scope

- Adding shortcut hints or a shortcut footer.
- Removing or redesigning the token usage display.
- Changing composer key bindings or submission behavior.
- Changing model selection behavior.
- Reproducing the reference application's mode indicator.

## Visual design

### Composer frame

The composer remains a rounded rectangular frame with one cell of horizontal
padding. Its border uses a subdued style so the typed content remains the
primary visual element. The frame spans the available chat width and grows
vertically with the existing multiline textarea up to its current limit.

### Prompt and content

The first input row begins with the existing `❯ ` prompt. The prompt keeps the
accent color, while the textarea cursor line remains transparent. Content and
cursor calculations continue to use the textarea's current width and height
rules.

### Model label

The active model is rendered inside the lower-right border rather than in the
status line below the composer. A single space separates the label from each
adjacent border segment. The label uses the subdued status style and is
truncated when necessary so the frame never exceeds the available width.

For very narrow terminals where the full label and a meaningful border cannot
coexist, the renderer progressively truncates the label and may omit it as the
last fallback. The rounded corners and total frame width remain valid.

### Token usage

The existing token usage text remains embedded in the upper-left composer
border. It keeps its current content and subdued presentation. No shortcut
hints are added beside it.

## Layout behavior

- Reserved viewport lines continue to account for the dynamic textarea height
  and the two composer border rows.
- Moving the model into the frame does not add another row.
- Autocomplete remains positioned relative to the composer and retains its
  existing available-height calculations.
- The composer must not exceed the chat pane width after ANSI styling is
  stripped.

## Testing strategy

Follow the repository TDD cycle:

1. Add a render test that expects the model label in the lower-right border and
   no longer expects it in the status row.
2. Implement the smallest border renderer that passes the new assertion.
3. Add cases for long model names, narrow terminals, multiline composer height,
   and preservation of token usage without shortcut hints.
4. Refactor shared width and truncation calculations while retaining the same
   output.
5. Run package tests, the full Go suite, `gofmt -l .`, and `go vet ./...`.

## Acceptance criteria

- The composer visually follows the approved reference within terminal UI
  constraints.
- The active model appears in the lower-right composer border at normal widths.
- Token usage remains visible below the composer.
- No shortcut hints are introduced.
- Existing composer interactions behave unchanged.
- Normal and narrow renderings do not overflow their allocated width.
- All repository quality gates pass.
