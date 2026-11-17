# Graph Report - ttaskam  (2026-11-16)

## Corpus Check
- 27 files · ~15,606 words
- Verdict: corpus is large enough that graph structure adds value.
- Unclassified: 2 file(s) not represented in the graph (top: .example 1, (none) 1)

## Summary
- 240 nodes · 888 edges · 14 communities (8 shown, 6 thin omitted)
- Extraction: 96% EXTRACTED · 4% INFERRED · 0% AMBIGUOUS · INFERRED: 34 edges (avg confidence: 0.85)
- Token cost: 0 input · 0 output

## Community Hubs (Navigation)
- testing.T
- task_test.go
- time.Time
- docToSession
- time.Time
- context.Context
- FocusService
- ttaskam
- Bot
- TaskService
- TaskRepo
- main
- TaskRepository
- ActionSuggester

## God Nodes (most connected - your core abstractions)
1. `FocusSession` - 42 edges
2. `Task` - 42 edges
3. `Bot` - 25 edges
4. `MemoryRepo` - 19 edges
5. `TaskService` - 16 edges
6. `FocusService` - 15 edges
7. `setupFocusTest()` - 15 edges
8. `NewTaskService()` - 13 edges
9. `UserContext` - 11 edges
10. `main()` - 10 edges

## Surprising Connections (you probably didn't know these)
- `main()` --calls--> `NewClient()`  [EXTRACTED]
  cmd/bot/main.go → internal/adapter/llm/client.go
- `main()` --calls--> `NewBot()`  [EXTRACTED]
  cmd/bot/main.go → internal/adapter/telegram/bot.go
- `main()` --calls--> `NewFocusService()`  [EXTRACTED]
  cmd/bot/main.go → internal/service/focus_service.go
- `main()` --calls--> `NewTaskService()`  [EXTRACTED]
  cmd/bot/main.go → internal/service/task_service.go
- `main()` --calls--> `NewFocusTxManager()`  [EXTRACTED]
  cmd/bot/main.go → internal/adapter/mongo/focus_tx_manager.go

## Import Cycles
- 2-file cycle: `internal/service/ai_eval_test.go -> internal/service/focus_service_test.go -> internal/service/ai_eval_test.go`
- 2-file cycle: `internal/service/focus_service_test.go -> internal/service/task_service_test.go -> internal/service/focus_service_test.go`
- 2-file cycle: `internal/service/ai_eval_test.go -> internal/service/task_service_test.go -> internal/service/ai_eval_test.go`
- 2-file cycle: `internal/domain/session_test.go -> internal/domain/task_test.go -> internal/domain/session_test.go`
- 3-file cycle: `internal/service/ai_eval_test.go -> internal/service/task_service_test.go -> internal/service/focus_service_test.go -> internal/service/ai_eval_test.go`
- 3-file cycle: `internal/service/ai_eval_test.go -> internal/service/focus_service_test.go -> internal/service/task_service_test.go -> internal/service/ai_eval_test.go`

## Communities (14 total, 6 thin omitted)

### Community 0 - "testing.T"
Cohesion: 0.12
Nodes (29): TestLiveLLM_Evaluation(), NewMemoryRepo(), TestTelegram_CleanCallbackData(), CleanCallbackData(), NewFocusSession(), TestSession_CannotCompleteAlreadyEnded(), TestSession_CannotSetExitReasonWhileActive(), TestSession_LifecycleAndVersion() (+21 more)

### Community 1 - "task_test.go"
Cohesion: 0.23
Nodes (4): chatMessage, chatRequest, chatResponse, responseFormat

### Community 3 - "docToSession"
Cohesion: 0.27
Nodes (3): docToSession(), FocusTxManager, SessionRepo

### Community 4 - "time.Time"
Cohesion: 0.20
Nodes (6): SessionStatus, Task, TaskStatus, idempotencyDoc, sessionDoc, taskDoc

### Community 6 - "FocusService"
Cohesion: 0.20
Nodes (3): SessionRepository, FocusService, NewFocusService()

### Community 8 - "Bot"
Cohesion: 0.17
Nodes (8): NewBot(), TestTelegram_Keyboards(), BuildExitReasonKeyboard(), BuildFocusKeyboard(), BuildMicroStartPromptKeyboard(), BuildTaskListKeyboard(), BuildTaskReadyKeyboard(), Bot

### Community 9 - "TaskService"
Cohesion: 0.11
Nodes (16): ConversationState, IntentType, NewClient(), AIInterpretation, AIInterpreter, UserContext, containsPersian(), TaskService (+8 more)

### Community 11 - "main"
Cohesion: 0.27
Nodes (9): main(), NewFocusTxManager(), EnsureIndexes(), TestMongo_Integration(), NewSessionRepo(), NewTaskRepo(), LoadConfig(), loadDotEnv() (+1 more)

## Knowledge Gaps
- **1 isolated node(s):** `ttaskam`
  These have ≤1 connection - possible missing edges. (Counts symbols only; 17 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **6 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Task` connect `time.Time` to `testing.T`, `task_test.go`, `time.Time`, `docToSession`, `context.Context`, `FocusService`, `Bot`, `TaskService`, `TaskRepo`, `TaskRepository`?**
  _High betweenness centrality (0.120) - this node is a cross-community bridge._
- **What connects `ttaskam` to the rest of the system?**
  _1 weakly-connected nodes found - possible missing edges._
- **Should `testing.T` be split into smaller, more focused modules?**
  _Cohesion score 0.12413793103448276 - nodes in this community are weakly interconnected._
- **Why does `FocusSession` connect `time.Time` to `testing.T`, `task_test.go`, `docToSession`, `time.Time`, `context.Context`, `FocusService`, `Bot`, `TaskRepo`?**
  _High betweenness centrality (0.108) - this node is a cross-community bridge._
- **Should `TaskService` be split into smaller, more focused modules?**
  _Cohesion score 0.10967741935483871 - nodes in this community are weakly interconnected._
- **Why does `Bot` connect `Bot` to `task_test.go`, `FocusService`, `TaskService`?**
  _High betweenness centrality (0.090) - this node is a cross-community bridge._