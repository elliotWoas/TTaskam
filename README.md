# ttaskam — Telegram Anti-Procrastination Coach

An anti-procrastination Telegram bot written in Go, backed by MongoDB Atlas, and designed for people who get stuck in planning loops instead of starting.

The core philosophy: **vague intention → concrete first action → single-task focus tunnel → finish or deliberately stop**. The bot minimizes time-to-first-action rather than hoarding task lists.

---

## 1. Quick Workflow

```
[User Intention]                "Prepare annual tax filing"
       │
       ▼
[Concrete Micro-Start]          "Download bank statement PDF" (< 2 min action)
       │
       ▼
[Focus Tunnel]                  🟢 Session Active (Backlog & new tasks locked)
       │
   ┌───┴───────────────┐
   ▼                   ▼
[⏸️ Pause / ▶️ Resume] [⏹️ End Focus]          [✅ Done]
 (Slot reserved)        (Slot freed immediately) (Task & session COMPLETED)
```

1. **Send intention**: User sends a task they are avoiding.
2. **Define micro-start**: The bot prompts for one concrete, physical action that takes $\le$ 2 minutes to begin immediately (suggested by AI if enabled, or entered manually).
3. **Enter Focus Tunnel**: Starting focus locks the user into that task. Creating new tasks or browsing the backlog is blocked until the session ends.
4. **Pause or Conclude**: Users can pause (slot remains reserved), conclude with `Done` (marks task complete), or `End` (frees slot immediately for a new task while keeping the previous task in progress).

---

## 2. Feature Status Matrix

| Capability | Status | Notes |
| :--- | :---: | :--- |
| **Micro-Start Requirement** | **Available** | Tasks cannot start without a concrete first action. |
| **72-Hour Task Decay** | **Available** | Unstarted tasks decay to `ARCHIVED` at exactly 72h. Started tasks are permanently exempt. |
| **Focus Tunnel** | **Available** | Enforces at most one open (`ACTIVE` or `PAUSED`) session per user in MongoDB. |
| **Atomic Start & Done** | **Available** | Coordinated via MongoDB multi-document transactions. |
| **Versioned Callbacks** | **Available** | Stale buttons from old or changed sessions are rejected. |
| **Idempotent Task Creation** | **Available** | 24-hour persistent idempotency keys prevent duplicates across retries. |
| **Immediate Slot Release on End** | **Available** | `/end` frees the focus slot instantly; optional exit feedback never blocks next task. |
| **Deterministic Manual Flow** | **Available** | Full core flow works with `/new`, `/start`, buttons, and manual micro-start entry. |
| **AI Message Classification** | **Implemented** | Wired in code; runs in manual fallback mode when `LLM_API_KEY` is unset. |
| **Live Real-Model Evaluation** | **Unverified Live** | Test suite written; requires `LLM_API_KEY` to run against live provider. |
| **Live Telegram Interaction** | **Unverified Live** | Poller is active; awaiting initial user message in Telegram. |
| **Google Sheets / Memory** | **Deferred** | Out of scope for MVP to prioritize initiation mechanics. |

For detailed interaction rules, see [docs/USER_GUIDE.md](docs/USER_GUIDE.md).  
For internals, state machines, and invariants, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

---

## 3. Running Locally on Windows

### Option A: Run Precompiled Binary (Recommended)
You do not need Go installed to run the compiled executable from the repository root:

```powershell
# 1. Clear any inherited environment variables from your current shell
Remove-Item Env:MONGO_URI, Env:TELEGRAM_BOT_TOKEN, Env:MONGO_DB_NAME, Env:LLM_API_KEY, Env:LLM_BASE_URL, Env:LLM_MODEL -ErrorAction SilentlyContinue

# 2. Launch the bot (it will load .env automatically)
.\bin\bot.exe
```

### Option B: Build & Run with Go
Requires Go 1.23+ installed:

```powershell
# Build binary
go build -o bin/bot.exe ./cmd/bot

# Run test suites
go test -v ./...

# Run directly
go run ./cmd/bot
```

---

## 4. Environment Variables (`.env`)

The application automatically loads `.env` from the working directory on startup.

> [!WARNING]
> **Precedence Warning:** Exported process environment variables override values in `.env`. If your shell has set placeholder variables (e.g. `$env:MONGO_URI = "mongodb+srv://..."`), clear them first with `Remove-Item Env:MONGO_URI`.

| Variable Name | Required? | Default | Description & Source |
| :--- | :---: | :---: | :--- |
| `TELEGRAM_BOT_TOKEN` | **Yes** | — | Telegram Bot API token from [@BotFather](https://t.me/BotFather). |
| `MONGO_URI` | **Yes** | — | MongoDB Atlas connection string (`mongodb+srv://...`). Used by bot and integration test. |
| `MONGO_DB_NAME` | No | `ttaskam` | MongoDB database name. |
| `LLM_API_KEY` | No | `""` | API key for LLM provider (OpenAI, Gemini, Groq). If empty, bot runs in 100% manual mode. |
| `LLM_BASE_URL` | No | `https://api.openai.com/v1` | REST endpoint URL. For Gemini: `https://generativelanguage.googleapis.com/v1beta/openai`. |
| `LLM_MODEL` | No | `gpt-4o-mini` | Model identifier (e.g. `gpt-4o-mini`, `gemini-1.5-flash`, `llama-3.1-8b-instant`). |

Example template: [.env.example](.env.example). Never commit real secrets to Git.
