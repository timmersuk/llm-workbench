# Engineering Conventions

Small, consequential implementation choices for the workbench's own codebase
(Go backend + Vite/React frontend). These are conventions for building the
tool, not the domain model it manages (see `architectural invariants.md` for
that). Add to this file whenever a new cross-cutting choice is made, so it
doesn't have to be re-derived or re-litigated later.

## Logging

* Backend logging uses `logrus` (`github.com/sirupsen/logrus`), configured
  globally in `cmd/server/main.go` via `LOG_LEVEL` and a JSON/text formatter
  switch.
* Always use structured logging: attach context via `WithField(key, value)`
  (one field) or `WithFields(logrus.Fields{...})` (multiple fields, plus
  `WithError(err)` for an error value), then call `Debug`/`Info`/`Warn`/
  `Error`/`Panic` with a short, fixed message string. Never use the
  `printf`-style variants (`Debugf`/`Infof`/`Warnf`/`Errorf`/`Printf`/
  `Panicf`) — interpolated values belong in fields, not the message, so
  they're queryable in the JSON log output. See `cmd/server/main.go`'s
  startup log, `internal/project/store.go`/`internal/task/store.go`'s
  `List()` skip-warnings, and `internal/api/stage_conversation.go`'s
  error/warn logs for the pattern to match.
* Exception: `logrus.Fatalf` is fine as-is. It's only used on unrecoverable
  startup failures (`utils.MustGetEnv*` in `internal/utils/env.go` and
  `cmd/server/main.go`'s frontend-mount/listen failures) where the process
  exits immediately after, so there's no downstream log aggregation benefit
  to structuring it.
* Every HTTP request is logged once, centrally, by `loggingMiddleware`
  (`internal/api/router.go`), wrapping the whole mux: method, path, status,
  duration, at `Info` for 2xx/3xx, `Warn` for 4xx, `Error` for 5xx. This is
  the only place request-level outcome is logged — individual handlers
  still log their own domain-specific detail (e.g. a persistence failure)
  on top of it, not instead of it.

## Configuration

* Env vars are read once, in `loadConfig() config`, called at the top of
  `main()` before any component is constructed (`cmd/server/main.go`).
  Names are `SCREAMING_SNAKE_CASE` with no prefix namespacing (`HTTP_ADDR`,
  `REPOS_ROOT`, `LOG_LEVEL`, `LLM_BASE_URL`, ...).
* Optional vars use `utils.GetEnvDefault[T](key, default)`
  (`internal/utils/env.go`) — generic over `string`/`bool`/`int`/
  `time.Duration`, silently falling back to `default` if the var is unset or
  fails to parse. Required vars use the `utils.MustGetEnv*` family, which
  calls `logrus.Fatalf` on missing or invalid values. Don't hand-roll
  `os.LookupEnv`/`strconv` calls outside these helpers.
* `REPOS_ROOT` (required, `utils.MustGetEnv`) is the shared parent directory
  under which every sibling repo — the gitstore data checkout and every
  project's code repository — is checked out. `DATA_REPO_URL` (required,
  `utils.MustGetEnv`) names the git remote `gitstore.Open` clones/resumes
  into `workspaceRoot` (Storage & file layout below), a directory computed
  at startup as `REPOS_ROOT` joined with a name derived from `DATA_REPO_URL`
  (`repoDirName`, `cmd/server/main.go`) — the same convention `git clone`
  itself uses to name a destination directory from a remote URL, so the
  data checkout is just another sibling repo under `REPOS_ROOT`.
  `PUSH_INTERVAL` (optional, default 30s) controls how often the background
  push worker attempts to push local commits to it.

## Graceful shutdown

* `cmd/server/main.go` splits into `loadConfig() config` (env parsing —
  called directly from `main()`, so `utils.MustGetEnv`'s process-exiting
  Fatal path only ever runs before `run()` starts) and
  `run(ctx context.Context, cfg config) error` (server construction and
  serving). `main()` is the only `os.Exit`/`logrus.Fatal` point in the
  process; `run()` must never call one directly, or it would bypass
  shutdown. `main()` derives `ctx` via
  `signal.NotifyContext(context.Background(), os.Interrupt)`, so Ctrl+C
  cancels it.
* `run()` builds an `*http.Server` (rather than a bare `http.ListenAndServe`
  call) and races its `ListenAndServe` against `ctx.Done()`. On
  cancellation it calls `srv.Shutdown`, bounded by a fresh
  `context.WithTimeout(context.Background(), cfg.shutdownTimeout)` —
  `SHUTDOWN_TIMEOUT` (default 10s), read with the same `utils.GetEnvDefault`
  pattern as `AGENT_TIMEOUT`/`LLM_TIMEOUT` — so shutdown can't hang
  indefinitely waiting on in-flight requests.
* After `Shutdown`, `run()` walks the `agentRunners` map and type-asserts
  each entry against `interface{ CloseAll() }` — deliberately not an
  `AgentRunner` interface method — to release any resources a live
  conversation left open. Today only `*ClaudeRunner` implements it,
  disconnecting every cached `claude` CLI client (`ClaudeRunner.CloseAll`,
  `internal/agentrunner/claude_runner.go`) so no `claude` subprocess is left
  orphaned when the server exits. `ChatClientRunner`/`CodexRunner` own
  nothing to close, so a type assertion means they need no no-op stub the
  way adding this to the `AgentRunner` interface would have forced.

## Storage & file layout

* Production persistence is git-backed: `internal/gitstore.Store`
  (`internal/gitstore/gitstore.go`, `commit.go`, `push.go`), built on the
  pure-Go `go-git` library rather than shelling out to a `git` binary (this
  repo's "prefer open standards" invariant, plus this milestone's explicit
  ban on a running `git` binary as a hard runtime dependency —
  `internal/gitutil`'s own shell-out is a different, already-installed-by-
  the-operator dependency for project *code* repositories, not this store's
  own persistence). `gitstore.Open(workspaceRoot, dataRepoURL)` resolves the
  derived `workspaceRoot` (`REPOS_ROOT` joined with a name derived from
  `DATA_REPO_URL`, see Configuration above) into a working checkout tracking
  `DATA_REPO_URL` as its `origin` remote — cloning into an empty root,
  resuming an existing matching checkout, or erroring
  (`ErrAmbiguousWorkspace`) on anything else — then every `Create`/`Update`
  commits synchronously and locally, serialized by one process-wide mutex,
  while a background goroutine (`Store.RunPushWorker`) periodically pushes
  accumulated commits to `origin`, logging and retrying indefinitely on
  failure and never pulling after startup. `cmd/server/main.go` requires
  `REPOS_ROOT` and `DATA_REPO_URL` at startup (`utils.MustGetEnv`) — the
  server refuses to start without them.
* `gitstore.Store` doesn't implement `project.Store`/`task.Store` itself —
  Go has no method overloading, and the two interfaces both declare
  `List`/`Get`/`Create`/`Update` with different signatures — so it splits
  into `Store.Projects` (`*gitstore.ProjectStore`) and `Store.Tasks`
  (`*gitstore.TaskStore`), two types sharing one underlying checkout/mutex,
  matching how `internal/api.Server` already takes `Projects`/`Tasks` as
  two separate fields (`internal/api/router.go`). Both wrap a
  `FileStore` (below) for the actual YAML read/write, adding git plumbing
  around it; read-only methods bypass git entirely and delegate straight
  through.
* Underneath GitStore, the on-disk layout and the actual YAML read/write
  is unchanged from the pre-GitStore `FileStore{Root string}` shape:
  project persistence (`internal/project/store.go`, `NewFileStore(root)`)
  lays out `<root>/<projectId>/project.yaml`. Task persistence
  (`internal/task/store.go`) is the same `FileStore{Root}` shape, but a
  single process-wide instance serves *every* project — every method takes
  an explicit `projectID` (`internal/task.Store`'s doc comment) rather than
  the store itself being constructed per-project — `internal/task` still
  has zero knowledge of `internal/project`; `projectID` is just a path
  segment its `FileStore` joins under the shared root. The root is
  `workspaceRoot`, computed at startup from `REPOS_ROOT` (see Configuration
  above) — no longer a repo-relative `data/` default — the workspace layout
  described in `CLAUDE.md` / `project_summary.md` (`data/projects/<id>/project.yaml`,
  `data/projects/<id>/tasks/<taskId>/task.yaml`) — this section is about how
  the Go code reads/writes that layout, not the domain model itself.
  `FileStore` itself is no longer reachable in production (`cmd/server/main.go`
  only ever constructs `gitstore.Store`) — it survives purely as a test
  fixture, used directly (no git) across `internal/project`'s,
  `internal/task`'s, and `internal/api`'s own test suites, and indirectly
  inside every `gitstore.ProjectStore`/`gitstore.TaskStore`.
  `knowledge/`'s on-disk format is an OKF bundle, not
  `<root>/<ID>/<kind>.yaml` — `internal/knowledge.FileStore` (also a
  `Root string`, matching this same `FileStore` naming/shape convention)
  implements `Get`/`List`/`Put` over it (Milestone 9 PR 1), and is not
  git-backed by this milestone — see `docs/knowledge schema v0.md`.
* Both `FileStore`s support `Create`/`Update` as well as `List`/`Get` —
  persistence is not read-only.
* Structs carry matching `yaml:` and `json:` tags so the same type
  round-trips straight to the API — don't introduce a separate DTO layer
  for this. Create/Update request bodies that genuinely differ in shape
  from the stored type (e.g. omitting server-assigned fields like `id` or
  timestamps) get their own type instead (`project.CreateInput`/`UpdateInput`)
  — still no separate DTO *layer*, just a distinct type where the wire
  shape actually differs.
* IDs are validated with a shared slug-style guard (empty, or containing
  `/`, `\`, or `..` → reject) and a sentinel `ErrInvalidID`, checked
  *before* the ID is joined into a filesystem path — this doubles as the
  path-traversal guard. Any new ID-keyed store must follow the same
  validate-before-join order. Task ids used to be constrained to
  `^TASK-\d+$`; that pattern is gone — ids are now client-specified at
  creation time (see below) and can be any valid slug. `List()` reports
  directories that don't parse as an entity as a `LoadError` rather than
  silently skipping them (see "Partial list results" below).
* Task ids are unique only *within* their owning project (the same id may
  exist under two different projects, since each has its own nested `tasks/`
  root); project ids are unique globally. Neither is auto-disambiguated on
  collision: `task.FileStore.Create` returns `ErrAlreadyExists` if the
  client-specified id's directory already exists, and
  `project.FileStore.Create` returns its own `ErrAlreadyExists` if the
  slug derived from `Name` already exists — both map to `409 Conflict`
  via `writeMutationError` (`internal/api/json.go`), this repo's first use
  of that status. Project ids are still server-derived (slugified `Name`),
  unlike task ids.
* Execution records (`execution.yaml`, not yet implemented) must be
  append-only per `docs/task schema v0.md` §5.2 — when that store is built,
  follow the same validate-before-join pattern above and never overwrite an
  existing `execution.yaml`.

## Partial list results

* `FileStore.List()` (`internal/task/store.go`, `internal/project/store.go`)
  does **not** fail the whole call when one entry fails to read or parse.
  It skips the bad entry, logs it (`logrus.Warn` with the entry's id and the
  underlying error), and reports it in the returned `ListResult{Tasks/
  Projects, Errors}` alongside every entry that *did* load — one malformed
  `task.yaml` shouldn't take `GET /api/v1/tasks` down for everything else.
  Only a root-level failure (the store's root directory itself is
  unreadable) still returns a real `error` from `List()`.
* `ListResult` is the wire format too — `handleListTasks`/
  `handleListProjects` (`internal/api/tasks.go`, `internal/api/projects.go`)
  write it straight through via `writeJSON`, so the response body is
  `{"tasks": [...], "errors": [{"id": "...", "error": "..."}]}` (or
  `projects`/`errors`), never a bare array. This follows the "no hidden
  state" invariant end to end: the frontend (`TasksPanel.tsx`,
  `ProjectsPanel.tsx`) reads `result.errors` and renders a notice rather
  than silently dropping entries it didn't ask about.
* Any future list endpoint over an on-disk collection should follow this
  same shape (skip + log + report, not fail-fast) rather than reintroducing
  the all-or-nothing behavior this replaced.

## Error wrapping

* Error messages are lowercase, gerund-phrase, no trailing punctuation, and
  wrap with `%w`: `fmt.Errorf("reading %s: %w", path, err)`,
  `fmt.Errorf("encoding chat completion request: %w", err)`.
* Sentinel errors (e.g. `ErrInvalidID`) are package-level `var`s, checked
  upstream with `errors.Is` — not string matching.

## HTTP routing & versioning

* All JSON API routes are versioned under `/api/v1`
  (`internal/api/router.go`); `/healthcheck` and the frontend catch-all
  (`GET /`) deliberately sit outside the version prefix.
* Routes use the Go 1.22+ method+pattern mux syntax directly on
  `http.ServeMux` (`mux.HandleFunc("GET /api/v1/tasks/{id}", ...)`) — no
  routing framework. Path params are read via `r.PathValue("id")`.

## Healthchecks

* `GET /healthcheck` reflects the status of every subsystem the server
  depends on — each registered `agentrunner.AgentRunner`'s `CheckHealth`,
  not just process liveness — per-subsystem, not collapsed to a single
  boolean, so a caller can tell *which* dependency is down. There is no
  separate chat-completer probe: the local-LLM path is itself an
  `AgentRunner` entry (`"local"`, wrapping `chat.ChatClient` via
  `ChatClientRunner`), so its health surfaces the same way as every other
  entry. As more subsystems are added, extend the handler
  (`handleHealthcheck`, `internal/api/router.go`) to probe each one the
  same way.
* Shape: `{"status": "ok"|"error", "build_id": "...", "subsystems": {"<key>":
  {"status": "ok"|"error", "error"?: "..."}, ...}}`. Subsystem keys are
  `"agent:<name>"` for each `agentRunners` entry (e.g. `"agent:claude-code"`,
  `"agent:local"`). The top-level `"error"` field is a semicolon-joined
  summary of every failing subsystem, kept for frontend backward-
  compatibility alongside the per-subsystem detail. Failure (any subsystem
  down) returns `503 Service Unavailable`.

## API error responses

* Handlers return errors via `writeAPIError(w, statusCode, message)`
  (`internal/api/json.go`) — a small flat JSON envelope,
  `{"error":{"code":"...","message":"..."}}`, in place of the standard
  library's plain-text `http.Error`. `code` is a fixed slug per status
  category (`bad_request`, `conflict`, `not_found`, `bad_gateway`,
  `internal_error` — see `errorCodeForStatus`), not a per-message enum: it
  exists so a frontend caller can branch on category without an enum that
  has to keep pace with every handler's wording. `message` is the same
  human-readable text every call site already passed to `http.Error`.
  Successful responses are JSON via `writeJSON`, which `writeAPIError`
  itself is built on.
* Map domain errors to HTTP status in one place per resource (see
  `writeGetError` in `internal/api/json.go`): invalid input → 400, not found
  → 404, anything else → 500 with a generic message (don't leak internal
  error text for 500s).
* `/healthcheck` keeps its own richer JSON body (see Healthchecks) rather
  than this envelope — it reports per-subsystem detail on success too, not
  just on failure, so it was never a fit for a plain error shape.
* Frontend callers parse `error.message` from the JSON body instead of
  reading response text (`frontend/src/api.ts`'s `streamSSE` and
  `deleteStageMessage`).

## Interface-based dependency injection

* Small interfaces (`TaskLister`, `ProjectLister`, `ChatCompleter` in
  `internal/api/router.go`) are declared in the *consuming* package, each
  with a doc comment naming the concrete production type it's satisfied by
  (e.g. "Satisfied by `*task.FileStore`"). Constructors return concrete
  structs; callers accept the narrow interface, not the concrete type.
  This is the general "providers are replaceable" mechanism
  (`docs/architectural invariants.md`, `docs/provider abstraction.md`) —
  any future provider-shaped dependency (e.g. a knowledge store) should
  follow the same shape.

## Package/file layout

* One file per HTTP resource/concern in `internal/api`
  (`tasks.go`, `projects.go`, `chat.go`, `frontend.go`, `json.go`,
  `router.go`), each with a matching `_test.go`. Mocks are centralized in a
  single shared `mocks_test.go` rather than duplicated per test file.

## Testing

* Interface mocks use `testify/mock`; assertions use `testify/assert` and
  `require`. Per-handler unit tests (`tasks_test.go`, `projects_test.go`,
  `chat_test.go`) call the handler func directly with
  `httptest.NewRequest`/`NewRecorder`, faking path params via
  `req.SetPathValue`. `router_test.go` goes one level up: it drives the real
  `http.ServeMux` from `NewRouter` (still via `NewRecorder`, still with
  mocked stores) to cover route registration/method matching.
* Test names follow `Test<Function>_<Case>` (e.g. `TestHandleGetTask_NotFound`).
* Integration tests (`internal/api/integration_test.go`) go a level further
  than `router_test.go`: real `FileStore`s and a real `*chat.Client` wired
  into the real router, served over a real `net.Listener`
  (`httptest.NewServer`) and driven with a real `http.Client` — no mocks at
  all. This is what catches wiring bugs (interface mismatches, real YAML
  round-tripping) that mock-based tests structurally can't. These use a
  distinct `TestIntegration_<Case>` naming, since they exercise the whole
  server rather than one function.

## Chat / LLM provider integration

* `internal/chat/client.go` speaks the OpenAI-compatible chat completions
  shape (`POST {base}/chat/completions`, `GET {base}/models` for health)
  regardless of which backend provider is configured. New provider
  integrations should conform to this same request/response shape rather
  than introducing a provider-specific one. See
  `docs/adr/0004-chat-client-openai-compatible-wire-format.md` for why
  this shape was standardized on instead of a bespoke interface with
  per-vendor adapters.
* `LLM_TIMEOUT` means two different things depending on call shape, both
  handled in `internal/chat/client.go`: for `StreamChatCompletion`, it's an
  **idle/inactivity** timeout that resets on every chunk received, so a
  stream that keeps emitting data never times out no matter its total
  duration — only a stalled/hung connection aborts (as
  `ErrStreamIdleTimeout`). For `CreateChatCompletion`, it's still a
  **total-duration** timeout (via `context.WithTimeout`), and is also
  relayed to the model as a leading system-message hint
  (`withTimeBudgetHint`) so reasoning-capable models have a chance to pace
  themselves instead of reliably blowing the deadline mid-reasoning. Keep
  this distinction in mind before changing either method's timeout wiring.

## Requirements/Planning agent executors

* `internal/agentrunner` implements the `executor.type: claude-code | codex |
  local | human` abstraction from `docs/task schema v0.md` for both
  Requirements/Planning stage conversations and the free-floating Chat tab
  — there is no separate direct `chat.ChatClient` code path in
  `internal/api`; every executor, including the local-LLM one, is reached
  through `agentRunners` (`api.NewRouter`'s `agentRunners` param) and
  `AgentRunner.Run`. `AgentRunner.Run` is one conversational turn, keyed by
  `RunInput.SessionKey` (stage conversations use `taskID+":"+stage`; free
  chat uses a client-generated id). `*ClaudeRunner` (`claude_runner.go`) is
  backed by `github.com/severity1/claude-agent-sdk-go` (an unofficial
  wrapper around the `claude` CLI subprocess — pin its version in `go.mod`,
  don't float it); `*ChatClientRunner` (`chat_client_runner.go`) adapts any
  `chat.ChatClient` (the local-LLM path) into the same interface, offering
  `RunInput.Tools` to `chat.ChatClient.StreamSessionTurn`'s `tools` param so
  Draft proposals (`propose_context`/`propose_plan`; Review offers two at
  once, `propose_review`/`propose_knowledge`, `docs/milestones/done/milestone9.md`)
  work identically to `ClaudeRunner`'s. A `codex_runner.go` is expected to follow the same
  `AgentRunner` interface later. Adopting a third-party multi-agent
  orchestration framework (e.g. AgenticGoKit) instead of this hand-rolled
  layer was considered and deferred — see
  `docs/adr/0005-defer-agenticgokit-adoption.md`.
* `chat.ChatClient.StreamSessionTurn` holds a session's conversation
  history in-memory (`openAIClient.sessions`, keyed by `sessionKey`) rather
  than the caller resending full history each call — same
  process-lifetime-only tradeoff `ClaudeRunner`'s cached `claudecode.Client`
  already has. If the model calls a tool, `StreamSessionTurn` returns the
  `*chat.ToolCall` and records it in the held history as an assistant
  tool-call message plus a synthetic tool-role acknowledgement
  (`"Draft proposed to user for review."`, matching `ClaudeRunner`'s own
  ack text), so the next turn stays protocol-valid without the caller
  reconstructing that shape.
* Availability is otherwise discovered live via `AgentRunner.CheckHealth`,
  not gated by a static enable flag — mirrors `chat.ChatClient.CheckHealth`'s
  real upstream probe. `main.go` always registers `"claude-code"` into the
  shared `agentRunners` map passed to `api.NewRouter`; `ClaudeRunner.
  CheckHealth` reports unavailable if the `claude` CLI isn't on `PATH`
  (`exec.LookPath`, indirected behind a package-level `lookPath` var for
  tests — the SDK has no cheaper real ping; a full `Connect`+`Disconnect`
  would spawn a subprocess per check). `REPOS_ROOT` itself is a hard
  startup requirement, not a health-checked one: `main.go` reads it via
  `utils.MustGetEnv` and refuses to start without it, since any agent
  runner capable of introspecting a task's reference repo (`claude-code`
  today, others later) needs it to know where repos live. `ClaudeRunner.
  CheckHealth` still separately errors if constructed with an empty
  `reposRoot` (defense-in-depth for direct/test construction bypassing
  `main.go`).
  `handleListAgentExecutors` (`internal/api/agent_executors.go`) and
  `handleHealthcheck` both call `CheckHealth` per entry rather than just
  checking map presence. `agentrunner.ResolveWorkspace` derives a
  per-task agent's cwd from a project's first configured `Repositories`
  entry (e.g. `github.com/timmersuk/logthing`) by convention: its last path
  segment joined under `REPOS_ROOT` (so repos checked out as siblings
  of this workbench resolve correctly), validated to exist and to never
  escape that root. `handlePostStageMessage` (`stage_conversation.go`)
  calls `ResolveWorkspace` unconditionally for every executor, including
  `"local"` — a project with no resolvable `Repositories` entry can't run
  GrillMe/Planning Mode with any executor, even though `ChatClientRunner`
  itself ignores `RunInput.Workspace`. Free chat has no per-task project, so
  it passes `REPOS_ROOT` itself as `RunInput.Workspace` without
  calling `ResolveWorkspace` — a Claude Code session started from the Chat
  tab gets read access rooted at the whole sibling-repos directory, not one
  specific repo. `AGENT_TIMEOUT` (default 5m) bounds a single `Run` call end
  to end; `AGENT_EXECUTION_TIMEOUT` (default 30m) separately bounds a single
  `Execute` call, since an unattended multi-step implementation run needs a
  much larger budget than one turn of a human-paced conversation — sharing
  one timeout between them cut autonomous executions off mid-run (visible as
  a `context deadline exceeded` failure with 0 commits/tokens reported, at
  exactly the `Run` timeout's duration) well before they could finish.
* Safety guardrails, all in `internal/agentrunner`: `WithAllowedTools`
  restricted to `Read`/`Grep`/`Glob` plus the stage's Draft-proposing MCP
  tool(s) — no `Write`/`Edit`/`Bash`, ever, regardless of what the model asks
  for; `RunInput.Tools` is optional (an empty slice means no MCP tool is
  registered at all — the shape free chat uses, since it has no Draft
  concept); the workspace path is always caller-resolved via
  `ResolveWorkspace` (or the free-chat default above), never agent-chosen;
  one in-flight run per `SessionKey` at a time (`ClaudeRunner.tryLock`/
  `unlock`); a hard `context.WithTimeout` wraps each `Run` call. Nothing is
  written to `context.yaml`/`plan.yaml` from this path — same Draft/
  Finalize separation as the chat path (`internal/api/stage_conversation.go`).
* Turn budgets (`RunInput.MaxTurns`, `ExecuteInput.MaxTurns`) are always an
  explicit value the *caller* computes and passes in — never inferred by an
  `AgentRunner` implementation from another field (e.g. `EnableBashTool`),
  and never backstopped by a hardcoded per-implementation constant when
  left unset. Zero means no turn-count limit at all (still bounded by the
  runner's own timeout), not "substitute some default." Stage-conversation
  callers (`internal/api/stage_conversation.go`'s `resolveStageRun`) pick
  `requirementsPlanningMaxTurns` (30) or `reviewMaxTurns` (1000) per stage;
  `internal/api/execution.go` sets `executionMaxTurns` (1000) for Execute.
  `ClaudeRunner` only emits `claudecode.WithMaxTurns` when the value is
  positive — omitting the option entirely (not passing 0) is how the
  underlying `claude` CLI is told not to cap turns.
  `internal/toolloop/engine.go`'s loop treats `MaxTurns <= 0` as unbounded.
  See `data/knowledge/coding-standards/caller-supplied-configuration.md` for
  the incident this convention comes from and why the same shape of
  mistake is worth watching for elsewhere.
* One `claudecode.Client` (one `claude` CLI subprocess) is created lazily
  per `SessionKey` and kept alive until `AgentRunner.CloseSession(sessionKey)`
  is called, since `WithCwd`/`WithSystemPrompt`/`WithAllowedTools` are all
  client-scoped (fixed at `Connect` time) rather than per-query in this SDK
  — a single global client couldn't serve two sessions with different
  workspaces/prompts. `CloseSession` calls the SDK's own `Disconnect()`
  and forgets the cached client; it's wired to two real call sites — a
  successful Finalize (`internal/api/finalize.go`; deliberately *not*
  Revise, which resumes the same `Conversation` by design) and the Chat
  tab's "New chat" action (`POST /api/v1/chat/sessions/close`) — rather
  than left as an unused capability.

## Build & single-binary packaging

* The frontend builds to `internal/web/dist` (`vite.config.ts`:
  `build.outDir: '../internal/web/dist'`, `emptyOutDir: true`) and is
  embedded into the Go binary via `//go:embed all:dist`
  (`internal/web/assets.go`). The `Makefile`'s `frontend` target must run
  before `go build` — there is no runtime dependency on Node/pnpm.
* `BuildID` is a package-level `var BuildID = "dev"` in `cmd/server/main.go`,
  overridden at build time via `-ldflags "-X main.BuildID=$(BUILD_ID)"`
  (`Makefile`), defaulting to `git rev-parse --short HEAD`. It's threaded
  through the stack as a plain string (e.g. `NewRouter(..., buildId string)`)
  and never re-derived downstream.

## Frontend conventions

* **Data fetching**: a thin `getJSON<T>(path)` wrapper over native `fetch`
  in `frontend/src/api.ts`, one function per endpoint. Paths are relative
  (no base-URL constant) — same-origin serving in prod, the Vite dev-server
  proxy in dev (`vite.config.ts`).
* **State management**: no library — `useState`/`useEffect` per component.
  Data-fetching components share the same shape: `data` state +
  `error: string | null` state, fetch in `useEffect`,
  `.then(setData).catch(err => setError(err.message))`.
* **Styling**: plain CSS in one global `frontend/src/index.css`, using CSS
  custom properties (`--text`, `--bg`, `--accent`, ...) with a
  `@media (prefers-color-scheme: dark)` override block for dark mode. Flat
  kebab-case class names — no BEM, CSS modules, or Tailwind.
* **TypeScript**: `frontend/src/types.ts` mirrors the Go API structs
  field-for-field, including JSON tag casing (snake_case preserved, not
  converted to camelCase, since it's a direct mirror of Go's JSON output —
  e.g. `Task` in `types.ts` matches `internal/task/task.go`'s json tags
  exactly). `verbatimModuleSyntax` is on, so type-only imports must use
  `import type {...}`.
* **Component structure**: one component per file, named exports (except
  `App`), flat `src/` (no `components/` subfolder yet). Each "panel"
  component is self-contained: fetch + loading/error/empty states + render,
  no shared data-fetching hook abstraction yet.
* **URL/history sync**: no routing library (e.g. react-router) — the
  tab/page set is small enough not to justify one, per the "prefer open
  standards / minimal dependencies" invariant. `frontend/src/url.ts` is a
  pure `parsePath(pathname): Route` / `routeToPath(route): string` pair
  (`Route = { tab: 'projects' | 'chat'; projectId?; taskId? }`), unit-tested
  with no browser dependency. `App.tsx` is the sole owner of navigation
  history: it holds `route` state, parses `location.pathname` on mount,
  listens for `popstate`, and is the only place that calls
  `history.pushState`/`replaceState`. Selection state that used to be
  internal `useState<Project|null>`/`useState<Task|null>` in
  `ProjectsPanel`/`ProjectDetailPanel` is instead a controlled
  `selectedProjectId?`/`selectedTaskId?: string` prop driven by `App`; each
  panel resolves the incoming id against its already-loaded list first (no
  network call on a normal click), falling back to a `getProject`/
  `getProjectTask` call for the deep-link/reload case, and exposes distinct
  `onSelect*`/`onBackTo*`/`onInvalid*` callbacks up rather than one
  overloaded callback — `App` uses `pushState` for every user-driven
  navigation (`onSelect*`, `onBackTo*`) and `replaceState` only for the
  silent-fallback correction (`onInvalid*`, and the initial-load correction
  of a non-canonical path like `/` down to `/projects`), so a corrected URL
  never creates a bogus back-button stop.
* **Tooling**: linting is `oxlint` (not ESLint). `build` runs
  `tsc -b && vite build` (type-check, then bundle).
* **Testing**: Vitest + `@testing-library/react`, jsdom environment
  (`frontend/vitest.config.ts`, a sibling config that `mergeConfig`s
  `vite.config.ts` rather than a `test` block inside it, since the latter
  would put a devDependency on the production build's `tsc -b` type-check
  path — see `frontend/tsconfig.node.json`'s `include`). Tests are
  colocated `*.test.ts`/`*.test.tsx` next to source. `frontend/src/api.test.ts`
  mocks global `fetch` directly (`vi.stubGlobal('fetch', ...)`) to assert
  wire-format correctness; every other test mocks the `./api` module
  boundary (`vi.mock('./api')`) instead, so component tests assert
  rendering/state logic with no overlap between the two layers. Run via
  `pnpm run test` (`vitest run`) inside `frontend/`, or `make test` at the
  repo root (`frontend-test` Makefile target).

## Exploratory manual testing

* When an agent is running the service locally and "trying things out" —
  more than a one-off `curl` call, but not a formal test script — prefer
  driving it through the REST API (`curl`/`httpie`/a script) over the
  browser UI, unless the thing actually being tested *is* UI behavior
  (rendering, interactions, layout). Puppeting a full browser for every
  intermediate check is slow and burns far more tokens than an equivalent
  API call for the same information. Reach for the browser only when the
  question can't be answered any other way.

## General

* Prefer the standard library (`net/http`, `encoding/json`) over frameworks
  for the HTTP layer — see the "prefer open standards" invariant.
