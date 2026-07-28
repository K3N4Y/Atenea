# Atenea Domain Language

Atenea is a coding agent that works inside a user's workspace through a shared
conversation. This glossary names the product concepts consistently across the
desktop app, terminal UI, documentation, and extension contracts.

## Conversation

**Session**:
A durable conversation between a user and an agent, including its prompts,
responses, tool activity, and control events.
_Avoid_: Chat, thread

**Prompt**:
User input admitted to a session for the agent to act on.
_Avoid_: Query, request

**Turn**:
One provider interaction that starts from the current session context and ends
with a response, tool calls, or a failure.
_Avoid_: Completion, response cycle

**Step**:
One unit of progress in a session run; a step contains a turn and any tool calls
that turn requests.
_Avoid_: Turn

**Steer**:
A prompt that redirects work already in progress and is applied at the next safe
turn boundary.
_Avoid_: Interrupt, follow-up

**Session event**:
An immutable fact in a session's durable history.
_Avoid_: Log entry, message

**Compaction**:
Replacement of older session context with a structured summary so the session
can continue within its context budget.
_Avoid_: Truncation, cleanup

## Agent capabilities

**Provider**:
A model service that accepts the agent's context and available tools and streams
one turn.
_Avoid_: Model, backend

**Tool**:
A named capability the agent can request during a turn to observe or affect its
environment.
_Avoid_: Function, command

**Permission gate**:
The decision point that allows, denies, or asks the user before a tool call runs.
_Avoid_: Authorization prompt, tool approval

**Extension**:
A capability supplied independently of Atenea that participates through a
published contract.
_Avoid_: Plugin

## Product boundaries

**Host**:
A user-facing application that runs the Atenea agent experience, currently the
desktop app or terminal UI.
_Avoid_: Client, frontend

**Core**:
The host-independent agent behavior shared by every host.
_Avoid_: Backend, engine

**Workspace**:
The project directory in which an agent session operates and whose instructions
and files define the immediate working context.
_Avoid_: Repository, working directory
