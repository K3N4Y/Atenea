# TUI visual hierarchy and consistency recommendations

## Scope

This document evaluates only the current standalone TUI implementation under `internal/tui` and its assembly in `cmd/atenea`.

The analysis intentionally does not use `.okf` as a source of product or interface concepts.

## Diagnosis

The TUI has consistent geometry, but it does not yet have a clear visual and semantic hierarchy.

Several surfaces share a two-cell margin and respect the terminal width, but each component independently decides its colors, emphasis, backgrounds, borders, and symbols. As a result, the interface can be correctly aligned while still feeling like several visual systems placed together.

### 1. Too many elements compete for attention

A normal screen can simultaneously emphasize:

- The Git branch in green.
- Context usage in the top bar.
- A full background block for each user message.
- Activity rows with symbols and status colors.
- The full composer border.
- Token usage embedded in the composer's top border.
- Model, plan mode, and YOLO mode embedded in the bottom border.
- A Git summary below the composer.

Relevant implementation areas:

- Top bar: `internal/tui/top_bar.go:50`
- Composer: `internal/tui/view_composer.go:63`
- Git summary: `internal/tui/view_status.go:84`
- Messages and activity: `internal/tui/view_transcript.go:123`

It is not immediately clear whether the user should look first at the conversation, the current agent state, the model, or repository metadata.

### 2. Colors carry too many meanings

Cyan is simultaneously used for interactive focus, the prompt, links, and Markdown headings. Green represents successful tool calls, added diff lines, and the current Git branch.

Relevant palette definitions:

- Accent: `internal/tui/theme/theme.go:19`
- Success: `internal/tui/theme/theme.go:22`

This weakens the semantic value of color:

- A Git branch is not a success state.
- A Markdown heading is not an interactive selection.
- A selected model, a link, and a cursor should not necessarily speak with the same visual voice.

Color should answer “what does this mean?” rather than merely “how can this look different?”

### 3. There is a palette, but not a complete style system

The `theme` package centralizes raw colors, but visual hierarchy is still assembled locally in each view.

Examples:

- Shared base styles: `internal/tui/view.go:9`
- Transcript styles: `internal/tui/view_transcript.go:105`
- Permission styles: `internal/tui/permission_panel.go:27`
- Diff styles: `internal/tui/view_diff.go:22`
- Overlay styles: `internal/tui/overlay.go:153`

The interface lacks shared semantic roles such as:

- Primary text.
- Secondary text.
- Metadata.
- Surface title.
- Active selection.
- Elevated surface.
- Destructive action.
- Pending state.

`statusStyle = Faint(true)` is currently a general solution for secondary information (`internal/tui/view.go:12`). This produces an interface with two dominant levels: normal content and dimmed content, without enough intermediate hierarchy.

### 4. The top bar and composer duplicate related information

The top bar shows context usage (`internal/tui/top_bar.go:86`), while the composer shows input and output token counts (`internal/tui/view_composer.go:134`). The active model and modes appear in the composer's bottom border (`internal/tui/view_composer.go:73`).

These values belong to the same operational context but are split across opposite ends of the screen. The duplication adds noise without forming a coherent information group.

### 5. The transcript mixes several visual languages

The same conversational flow contains:

- User messages as background-filled blocks with a rail.
- Assistant responses as uncontained Markdown documents.
- Reasoning with a diamond marker.
- Tool calls as compact textual activity rows.
- Permission events as yellow activity.
- Diffs as rich cards with colored bands.
- Notices as faint text.

The primary rendering switch is in `internal/tui/view_transcript.go:123`.

Each distinction is individually useful, but the components do not appear to belong to one visual family:

- User message: heavy surface with substantial padding.
- Assistant response: editorial document.
- Tool activity: operational log.
- Diff: data-rich card.

There is no strong visual concept of an assistant turn that groups reasoning, tools, subagents, and final response.

### 6. The composer has too much persistent decoration

The rounded border already establishes the input area. The same border also carries:

- Token usage at the top.
- Model and mode information at the bottom.

See `internal/tui/view_composer.go:63`.

This turns the primary interaction element into a telemetry panel. On narrow terminals, metadata also competes directly with the available input width.

### 7. Overlays are consistent with each other, but not with the main screen

The pickers generally use:

- A square border.
- A cyan title embedded in the top border.
- Tabular headers.
- A footer containing all keyboard shortcuts.
- Full-screen replacement.

See `internal/tui/overlay.go:145` and `internal/tui/model_picker.go:253`.

The composer uses a rounded border, while permissions use filled surfaces. The TUI therefore has at least three panel languages:

1. Rounded borders.
2. Square borders.
3. Filled surfaces.

Their use does not consistently communicate hierarchy, elevation, or modality.

### 8. Selection and status symbols do not form a single vocabulary

Before normalization, the TUI used symbols including:

- `❯` in menus and the session picker.
- `>` in model and MCP pickers.
- `●`, `○`, and `◌` for different states.
- `◆` for reasoning.
- `✓`, `✗`, `?`, and `–` for activity states.
- `┃`, `│`, and `↳` for visual relationships.

Examples:

- `internal/tui/view_composer.go:24`
- `internal/tui/model_picker.go:198`
- `internal/tui/mcp_picker.go:243`
- `internal/tui/view_transcript.go:24`

Each symbol is understandable in isolation, but together they create too much visual vocabulary for the user to learn.


## Recommendations

### P0 — Establish one screen hierarchy

Define four explicit levels:

1. **Primary:** assistant response and text currently being written by the user.
2. **Secondary:** the user's previous request and relevant results.
3. **Operational:** tools, reasoning, tokens, Git state, and background activity.
4. **Interruptive:** permissions, errors, and confirmations.

Recommended rule:

> At rest, only primary content uses high contrast. Operational and interruptive elements increase contrast only while they are active or require attention.

This hierarchy should be established before changing individual colors.

### P0 — Reduce the base screen to three regions

#### Header

Use a single quiet line, for example:

```text
atenea  ~/dev/atenea  main                         gpt-5.6  16k/256k
```

Recommendations:

- Use the workspace path as the main context identifier.
- Present the Git branch as neutral metadata instead of a success state.
- Group model and context information on the right.
- Reconsider the permanent blank row above and below the current top bar.

The current top bar reserves three terminal rows (`internal/tui/layout.go:28`) while its content occupies only one.

#### Conversation

The conversation should receive most of the available space and visual contrast.

#### Composer

Keep the input visually clean:

```text
╭────────────────────────────────────────────────────────────────────╮
│ ❯ Ask Atenea…                                                      │
╰────────────────────────────────────────────────────────────────────╯
```

Plan mode, YOLO mode, model, token usage, and Git state should live in one shared status line rather than inside the composer border.

### P0 — Create semantic style roles

Without introducing a large abstraction, define and consistently use roles such as:

- `primaryTextStyle`
- `secondaryTextStyle`
- `metadataStyle`
- `focusStyle`
- `successStyle`
- `warningStyle`
- `dangerStyle`
- `surfaceStyle`
- `selectedRowStyle`

Recommended rules:

- **Accent:** focus, cursor, and active selection only.
- **Success:** a completed successful operation.
- **Warning:** something requiring attention or a decision.
- **Error:** an actual failure.
- **Muted:** persistent metadata.
- **Bold:** titles and decisions, not general state.
- **Faint:** information that is truly optional.

Do not use green for the Git branch or cyan for editorial headings by default.

### P1 — Reduce the visual weight of user messages

The current user message combines:

- A full background.
- A rail.
- Three cells of horizontal padding.
- A blank row above and below the content.

See `internal/tui/view_transcript.go:125`.

#### Preferred option: compact rail

```text
  ▌ Fix the race in the session store.
```

Recommended properties:

- No full-width background.
- Accent only on the rail.
- The same outer margin as assistant content.
- One blank line after the message.

#### Alternative

Keep the background, but remove the rail and reduce vertical padding. The rail and background currently communicate the same distinction twice.

### P1 — Group tool activity inside the assistant turn

Tools should read as subordinate details of an assistant turn rather than as elements equivalent to messages.

Example:

```text
  Checking context
  ├─ Read      internal/tui/view.go
  ├─ Search    "Foreground(" · 31 matches
  └─ Test      go test ./internal/tui · passed

  Here are the recommendations…
```

Recommended principles:

- One activity rail per assistant turn.
- Spinner or accent only for the active operation.
- Completed operations should normally become neutral metadata.
- Use green sparingly, such as for a completion icon.
- Expand errors in red.
- Collapse long outputs.
- Represent subagents as children in the same activity tree.

Task and subagent children use `├─` for non-final children and `└─` for the final child, detached job, or summary; apply the same tree consistently to all assistant activity.

### P1 — Consolidate telemetry

Model, context, tokens, modes, and Git state should form one metadata strip.

Example:

```text
plan · gpt-5.6 · 16k/256k                   3 files  +42 −7
```

Place it either below the composer or in the header, but not in both places.

Recommended narrow-terminal priority:

1. Active mode (`plan`, `YOLO`).
2. Model.
3. Context usage.
4. Git changes.
5. Detailed input/output token counts.

Today, each component truncates or degrades independently.

### P1 — Simplify the permission panel

The permission panel currently combines several backgrounds and selection treatments (`internal/tui/permission_panel.go:27`). It is visually more complex than the decision it presents.

Suggested structure:

```text
 Permission required
 Bash wants to run:

   go test ./internal/tui/...

 [Deny]   Allow once   Allow bash this session
```

Recommendations:

- Use one elevated surface.
- Use warning emphasis for the title.
- Put the command on one secondary surface.
- Use one accent treatment for the current selection.
- Do not communicate an unconfirmed “Allow” action as success.
- Reserve green for an authorization that has already been granted.

### P1 — Unify all pickers

Model, MCP, connection, and session pickers should share:

- The same border.
- The same cursor (`❯`).
- The same selected-row treatment.
- The same header hierarchy.
- The same footer format.
- The same empty, loading, and error states.

The shared overlay implementation at `internal/tui/overlay.go:158` is the natural convergence point.

Recommended selection vocabulary:

```text
❯ OpenAI
  Anthropic
  OpenRouter
```

Represent the currently applied value separately:

```text
❯ OpenAI       active
  Anthropic
  OpenRouter
```

The cursor answers “where will I act?” while the state answers “what is currently applied?” They should not be represented by competing prefixes.

### P2 — Reduce the symbol vocabulary

Use this maximum semantic vocabulary for renderer-owned selection, status, and relationship markers:

- `❯` — selection or cursor.
- `│`, `├─`, `└─` — hierarchy, relationships, and continuation rails.
- `●` — active, connected, or in progress.
- `✓` — completed successfully.
- `!` — attention required, including permission requests and user-denied operations.
- `×` — error or failed operation.

Preserve `┃` only for the user-message rail and snackbar rail. This is an intentional distinction from the thinner operational `│` rail; the snackbar uses `┃` as well. Remove equivalent renderer variants such as `>`, `◆`, `○`, `◌`, `✗`, `?`, `↳`, and the activity marker `–`. Inactive states may use text alone when their status label already explains the state.

Do not normalize ordinary punctuation, structural panel borders, Markdown/editorial glyphs, diff markers, or user/model/provider/tool text merely because it contains one of these characters.

### P2 — Align Markdown hierarchy with the application shell

Markdown already has an internal hierarchy (`internal/tui/view_markdown.go:58`), but it is not fully coordinated with the surrounding TUI.

Recommendations:

- H1: bold primary text, without accent by default.
- H2: bold primary text with section spacing.
- H3 and below: bold secondary text.
- Accent: links only.
- Block quotes: muted rail.
- Horizontal rules: adapt to the available content width instead of using a fixed 40-cell rule (`internal/tui/view_markdown.go:17`).
- Code: use the same secondary surface language as commands and diffs.

### P2 — Design the empty conversation state explicitly

Current notices appear as faint transcript text (`internal/tui/view_transcript.go:182`). This leaves a new conversation without a clear visual center.

A minimal empty state could be:

```text
                         Atenea

             Working in ~/dev/atenea
             gpt-5.6 · plan off

             Type a task or use / for commands
```

The empty state should disappear after the first user message. It does not need to become a dashboard or use multiple cards.

## Recommended implementation order

1. Define visual roles and color semantics.
2. Simplify the header and composer.
3. Consolidate telemetry into one location.
4. Reduce the weight of user messages.
5. Group tool activity by assistant turn.
6. Simplify permissions.
7. Normalize overlays and symbols.
8. Align Markdown and the empty state with the new system.

## Guiding principle

Do not begin with isolated palette changes. The primary problem is not the choice of individual colors; it is that too many elements present themselves as important at the same time.

The highest-impact improvement is to make the conversation and input unmistakably primary, while keeping operational information quiet until it requires attention.
