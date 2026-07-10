---
updated_at: 2026-07-09
summary: Visual identity and UX guidelines for Atenea.
---

# Visual Identity and UX (Atenea)

## 1. Main Concept
The application seeks to break the barrier between technical and creative profiles through the use of Artificial Intelligence. The interface should convey accessibility, fluidity and simplicity, making any user feel empowered and not intimidated.

## 2. Design Principles (UX/UI)
- **Minimalism ("Clean UI"):** The main screen must be free of visual noise. The user should not feel overwhelmed when opening the application.
- **Zero-Friction (Chat First):** Whenever the user enters the application, they should find a new chat open, ready to interact instantly.
- **Soft and Organic Shapes:** Intensive use of very rounded edges to prevent the environment from feeling aggressive:
 - **General elements (containers, inputs, etc.):** use a `border-radius` of **24px**.
 - **Buttons:** use a total rounding (**full / pill style**).
- **Flat Structure (No Cards):** Avoid the traditional design based on cards (cards). Separation of elements should be achieved through the use of white space (padding/margin) and very subtle contrasts, creating a more unified and continuous view.

## 3. Color Palette
- **Base / Background Color:** **Paper White** (a slightly warm and comfortable tone for prolonged viewing, e.g. `#fef9ed` or `#F9F9F8`). Avoid bright pure white `#FFFFFF`.
- **Accent Color:** **Orange**. It represents creativity and energy. It should be used in a **very moderate and strategic way** (only for the send button, active statuses or subtle indicators) so as not to tire the eyes or take away the focus of the content.

## 4. Layout (Screen Structure)
- **Main area:** The chat, occupying most of the screen, centered and clean.
- **Left Sidebar (Sidebar):**
 - It will contain the history of chats or secondary navigations.
 - **Functional requirement:** It must have a persistent state (if the user hid it, it must remain hidden when returned to open the app).

## 5. Development Architecture (Vue)
- The interface must be built under a strict **architecture of functional and independent components**.
- Each part of the UI (sidebar, message input, chat bubble) must be a separate file to make the code as clean and easy to navigate as the visual interface.

## 6. Typography
- **Universal Font (Chat and Code):** **Red Hat Mono**. By using a monospaced font for the entire interface (both for colloquial messages and code blocks), the technical context is unified with the clean design. This adds a lot of personality to the tool and maintains visual consistency.

## 7. Iconography
- **Icon Library:** **Phosphor Icons**.
- **Style:** The *Regular* or *Light* style (thin and consistent strokes) should be prioritized. Its rounded edges and legibility fit perfectly with the soft-edged approach and monospaced typography, keeping the visual weight of the interface to a minimum.

## 8. Anatomy of the Chat Message
The conversation should feel like a continuous and flat canvas (see "Flat Structure"), differentiating the elements only when it provides clarity:
- **User messages:** carry a **subtle background** that distinguishes them from the canvas, but **without a border**. The container respects the general rounding (24px).
- **AI responses (Markdown rendering):** they are rendered **directly on the Paper White background**, without a background or its own container, so that the content flows integrated into the view. During streaming, render the revealed Markdown incrementally with an accent caret; once complete, render the full message without the caret.
- **`edit` and `diff` tools:** these blocks **do have their own background** to visually separate the code and the changes of the conversational flow.

## 9. Thinking Streaming (Thinking Process)
To reflect the AI's reasoning process without cluttering the interface, the following behavior should be implemented:
- **During generation (Active):** Only the **last 4 lines** of the AI's ongoing thinking will be displayed.
- **On completion (Collapsed):** The entire block will automatically collapse into a single line indicating "Thought" accompanied by the total time it took.
- **Optional Expansion:** Once the thinking is done, the user should be able to **fully expand the block** to see the full content if desired.
- **Timer Format (Process Time):**
 - Less than 200ms: `"briefly"` (briefly).
 - Between 200ms and 999ms: Display milliseconds (e.g. `"450ms"`).
 - From 1 second: Dynamic progressive format (e.g. `"3m 5s"`, `"1h 15m 13s"`).


## 10. Tool Read
When you run the `read` tool, the interface should reflect its status using a label accompanied by the file name:
- **During reading (Active):** show `"Reading"` next to the file name.
- **On completion:** show `"Read"` next to the file name.
- **File name:** show only the file name, never the path complete.

## 11. Voice and Microcopy
For the user to feel that the agent is really working, the interface must communicate progress, intent and control at each stage of the interaction. The voice should be clear, warm and slightly technical, without sounding cold or exaggerated.

### Principles
- **Show real progress:** each action of the agent should feel like a concrete progress, not like a generic "loading" state.
- **Give a feeling of control:** the user must understand what is happening and what to expect, without feeling at the mercy of the system.
- **Speak calmly:** the language should be brief, confident and human, avoiding empty or overly verbose phrases.
- **Reinforce user agency:** The system should show that the user is still in control, even when the agent is executing tasks.

### Expected behavior
- **During action:** show microcopy oriented to the ongoing activity, for example: `"Working"`, `"Checking context"`, `"Preparing response"`, `"Reviewing changes"`.
- **When there is visible progress:** communicate it with short phrases that suggest movement and progress, for example: `"Found relevant context"`, `"Drafting answer"`, `"Almost done"`.
- **When the agent is waiting or processing:** use messages that convey continuity and rhythm, without generating anxiety, for example: `"Still working"`, `"Continuing"`.
- **When the user needs guidance:** offer simple messages that convey control, for example: `"You can keep going"`, `"You can stop anytime"`, `"This step is in progress"`.

### Tone rule
- The interface should avoid passive or empty microcopy like `"loading"` when there is an opportunity to communicate something more meaningful.
- The copy should be useful, concrete and user-oriented, not merely decorative.
- Each status message should help answer a basic question: "what is the agent doing and what happens now?"
