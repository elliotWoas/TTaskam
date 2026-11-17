# Telegram User & Testing Guide

This guide describes what you can actually do in Telegram with `ttaskam`, how commands and buttons behave across states, how free text is handled, and how to execute a complete repeatable smoke test.

---

## 1. Commands Reference

All commands are handled by [internal/adapter/telegram/bot.go](../internal/adapter/telegram/bot.go).

| Command | Allowed When? | Action & Bot Response |
| :--- | :--- | :--- |
| `/start` | Any time | If idle: displays welcome message and core loop instructions.<br>If focused: displays current focus tunnel banner with action buttons. |
| `/help` | Any time | Displays command cheat-sheet. |
| `/new <title>` | When **not** in active focus | Explicit manual task capture. Creates task in `CREATED` status and prompts for micro-start without AI classification.<br>*(If focused: blocked by Focus Tunnel).* |
| `/status` | Any time | If focused: shows task title, current step, total elapsed seconds, and `[Pause] [Done] [End]` buttons.<br>If idle: shows idle message and hints to use `/tasks` or type an intention. |
| `/tasks` | When **not** in active focus | Lists active uncompleted tasks with inline `[Start]` buttons. Lazily evaluates 72h decay on unstarted tasks.<br>*(If focused: blocked by Focus Tunnel).* |
| `/pause` | Session is `ACTIVE` | Transitions focus session to `PAUSED`. Suspends active timer. Focus tunnel slot remains reserved. |
| `/resume` | Session is `PAUSED` | Transitions focus session back to `ACTIVE`. Resumes active timer. |
| `/done` | Session is `ACTIVE` or `PAUSED` | Concludes session and marks task `COMPLETED` atomically. Focus slot is released. |
| `/end` | Session is `ACTIVE` or `PAUSED` | Concludes session immediately (`ENDED`). Task remains `STARTED` (uncompleted). Focus slot is freed instantly. Displays optional exit reason buttons. |

---

## 2. Buttons Reference & Callback Formats

Buttons are constructed in [internal/adapter/telegram/keyboards.go](../internal/adapter/telegram/keyboards.go) and embed target entity IDs and versions:

### Focus Action Keyboard
Displayed on active and paused focus sessions:
- `[⏸️ Pause]` (`focus:pause:<session_id>:<version>`): Available when `ACTIVE`.
- `[▶️ Resume]` (`focus:resume:<session_id>:<version>`): Available when `PAUSED`.
- `[✅ Done]` (`focus:done:<session_id>:<version>`): Marks task & session `COMPLETED`.
- `[⏹️ End]` (`focus:end:<session_id>:<version>`): Concludes session, leaves task `STARTED`.

### Task Initiation & Ready Keyboard
Displayed after a task has a concrete micro-start:
- `[🟢 Start Focus]` (`task:start:<task_id>`): Atomically opens focus session and marks task `STARTED`.
- `[✏️ Edit Step]` (`task:edit:<task_id>`): Puts user into `AWAITING_STEP` state to type a new step.
- `[❌ Cancel]` (`task:cancel:<task_id>`): Cancels setup.

### Micro-Start Prompt Keyboard
Displayed when a task is created without a micro-start:
- `[⚡ Use default: 'Open workspace & review']` (`task:default:<task_id>`): Applies standard starter step.
- `[❌ Cancel]` (`task:cancel:<task_id>`): Cancels setup.

### Exit Reason Keyboard
Displayed after `/end` (non-blocking, purely optional):
- `[🌀 Distracted]` (`reason:<session_id>:DISTRACTED`)
- `[🚧 Blocked]` (`reason:<session_id>:BLOCKED`)
- `[🥱 Tired]` (`reason:<session_id>:TIRED`)
- `[🛑 Done For Now]` (`reason:<session_id>:DONE_FOR_NOW`)
- `[⏩ Skip]` (`reason:<session_id>:SKIPPED`)

---

## 3. Conversation States & Free-Text Behavior

The bot tracks user state using domain types defined in [internal/domain/ai.go](../internal/domain/ai.go) and [internal/service/task_service.go](../internal/service/task_service.go):

```
                        ┌─────────────┐
                        │    IDLE     │
                        └──────┬──────┘
                               │ Free text / /new
                               ▼
                        ┌─────────────┐
                   ┌──> │AWAITING_STEP│
                   │    └──────┬──────┘
         Edit step │           │ Micro-start set
                   │           ▼
                   │    ┌─────────────┐
                   └────┤ READY_FOCUS │
                        └──────┬──────┘
                               │ Start focus
                               ▼
                        ┌─────────────┐
                        │   FOCUSED   │
                        │(ACTIVE/PAUSE│
                        └─────────────┘
```

### Free-Text Handling by State

#### State 1: `FOCUSED` (Session `ACTIVE` or `PAUSED`)
- **Behavior**: The Focus Tunnel is strictly locked.
- Any incoming free text is intercepted by Go and rejected with:
  > *⛔ Focus Tunnel Active: You are currently focused on "[Task Title]". Finish or end this session before starting a new task.*
- The message includes current status and action buttons. Zero AI calls are made.

#### State 2: `AWAITING_STEP` (User prompted for micro-start)
- The user is in `pendingStep[userID]` waiting to supply a concrete $< 2$ minute action.
- **Vague input** (e.g. `u know what im talking about`, `idk`, `stuff`, `ok`):
  - Rejected by Go validation.
  - Bot responds with clarification: *"That's still a bit broad or vague. What is the very first physical action you can do in under 2 minutes? (e.g. open the doc, find the email)"*.
  - Task micro-start is **not** updated.
- **Concrete input** (e.g. `Open file and write title`):
  - Saves micro-start to the task and transitions to ready state (`[🟢 Start Focus]`).

#### State 3: `IDLE` (No active session, no pending step)
- **If AI is enabled (`LLM_API_KEY` set)**:
  - **Chit-chat / Frustration** (`wtf`, `ok`, `hello`): Classified as `CHAT`. Go **never saves a task** (task count remains 0). Bot responds with a coach check-in: *"Ready when you are! What is one task you've been putting off that we can start right now?"*.
  - **Ambiguity / Overwhelm** (`I don't know where to start` / `نمیدونم از کجا شروع کنم`): Classified as `NEEDS_CLARIFICATION`. Go **never saves a task**. Bot asks a single clarifying question.
  - **Genuine Intention** (`I need to email my professor, but I keep putting it off`): Classified as `NEW_TASK`. Go creates task in MongoDB, assigns the suggested micro-start, and presents `[🟢 Start Focus]`.
- **If AI is disabled (Current default when `LLM_API_KEY` is unset)**:
  - Free text is treated as a manual task intention.
  - A task is created with that text as the title, and the bot replies with the micro-start prompt keyboard (`[⚡ Use default...]` `[❌ Cancel]`).

---

## 4. Key Behavioral Distinctions

### `Done` vs. `End`
| Attribute | `Done` (`/done` or button) | `End` (`/end` or button) |
| :--- | :--- | :--- |
| **Task Status** | Becomes `COMPLETED` | Remains `STARTED` (in progress) |
| **Focus Session** | Marked `COMPLETED` | Marked `ENDED` |
| **Focus Slot** | Released immediately | Released immediately |
| **Exit Feedback**| None (celebrates completion) | Prompts for optional reason |
| **Future Starting**| Cannot be started again | Can be resumed into focus anytime |

### The 72-Hour Decay Rule
- **Unstarted tasks (`CREATED`)**: If `now >= expires_at` (exactly 72 hours after `created_at`), the task expires. It cannot be started from old buttons or lists; any action transitions it to `ARCHIVED`.
- **Started tasks (`STARTED`)**: Any task that has entered a focus session at least once is **permanently exempt** from the 72-hour decay. You can end a session, leave the task for weeks, and start focus on it again later.

---

## 5. Complete Repeatable Test Walkthrough

Follow these steps in Telegram to verify the entire system:

1. **Start**: Send `/start`. Verify you receive the welcome guide.
2. **Ambiguity Test**: Send `wtf` or `نمیدونم از کجا شروع کنم`.
   - *If AI enabled*: Verify it does NOT create a task, but sends a friendly coaching prompt.
   - *If AI disabled*: Verify it creates a task title and asks for a concrete step.
3. **Task Creation**: Send `Draft quarterly report` (or `/new Draft quarterly report`).
   - Verify bot creates task and asks: *"What is your concrete first action (< 2 minutes)?"*
4. **Vague Step Rejection**: Reply with `u know what im talking about`.
   - Verify bot rejects this and asks for a concrete physical step.
5. **Concrete Step Entry**: Reply with `Open Google Docs and write document heading`.
   - Verify bot saves the step and presents `[🟢 Start Focus]` and `[✏️ Edit Step]`.
6. **Start Focus**: Tap `[🟢 Start Focus]`.
   - Verify bot updates to `🟢 Focus ACTIVE` with `[⏸️ Pause] [✅ Done] [⏹️ End]`.
7. **Tunnel Lockout Verification**: While focused, send `Buy groceries` or `/tasks`.
   - Verify bot rejects the input with `⛔ Focus Tunnel Active` and displays your active task.
8. **Pause & Resume**:
   - Tap `[⏸️ Pause]`. Verify status changes to `⏸️ Focus PAUSED`.
   - Tap `[▶️ Resume]`. Verify status changes back to `🟢 Focus ACTIVE`.
9. **Deliberate End**: Tap `[⏹️ End]`.
   - Verify session ends immediately, elapsed work is shown, and reason buttons appear.
10. **Non-Blocking Feedback Test**: Do NOT tap any reason button. Immediately send `Review team pull request`.
    - Verify the bot accepts your new task immediately without getting trapped in the exit prompt.
11. **Complete Task**:
    - Tap `[⚡ Use default]` then `[🟢 Start Focus]`.
    - Tap `[✅ Done]`. Verify task and session complete, releasing the slot.
