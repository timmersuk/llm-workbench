# Engineering Conventions

Small, consequential implementation choices for the workbench's own codebase
(Go backend + Vite/React frontend). These are conventions for building the
tool, not the domain model it manages (see `architectural invariants.md` for
that). Add to this file whenever a new cross-cutting choice is made, so it
doesn't have to be re-derived or re-litigated later.

## Logging

* Backend logging uses `logrus` (`github.com/sirupsen/logrus`), configured
  globally in `cmd/server/main.go` via `LOG_LEVEL` and a JSON/text formatter
  switch. Use `logrus.WithFields` for structured context rather than
  string-formatted messages.

## Configuration

* Env vars are read once, at the top of `main()`, before any component is
  constructed (`cmd/server/main.go`). Names are `SCREAMING_SNAKE_CASE` with
  no prefix namespacing (`HTTP_ADDR`, `WORKSPACE_ROOT`, `LOG_LEVEL`,
  `LLM_BASE_URL`, ...).
* Optional vars use `utils.GetEnvDefault[T](key, default)`
  (`internal/utils/env.go`) — generic over `string`/`bool`/`int`/
  `time.Duration`, silently falling back to `default` if the var is unset or
  fails to parse. Required vars use the `utils.MustGetEnv*` family, which
  calls `logrus.Fatalf` on missing or invalid values. Don't hand-roll
  `os.LookupEnv`/`strconv` calls outside these helpers.

## Storage & file layout

* Task/project persistence is a read-only `FileStore{Root string}`
  (`internal/task/store.go`, `internal/project/store.go`) constructed via
  `NewFileStore(root)`, laid out on disk as `<root>/<ID>/<kind>.yaml` (e.g.
  `data/tasks/TASK-0001/task.yaml`). The root defaults to `WORKSPACE_ROOT`
  (`data/`, see Configuration above) with `tasks/`, `projects/`, and
  eventually `knowledge/` nested under it — the workspace layout described
  in `CLAUDE.md` / `project_summary.md` — this section is about how the Go
  code reads that layout, not the domain model itself. `knowledge/`'s
  on-disk format (when that store is built) is an OKF bundle, not
  `<root>/<ID>/<kind>.yaml` — see `docs/knowledge schema v0.md`.
* Structs carry matching `yaml:` and `json:` tags so the same type
  round-trips straight to the API — don't introduce a separate DTO layer
  for this.
* IDs are validated with a package-level `regexp.MustCompile` pattern (e.g.
  `^TASK-\d+$`) and a sentinel `ErrInvalidID`, checked in `Get` *before* the
  ID is joined into a filesystem path — this doubles as the path-traversal
  guard. Any new ID-keyed store must follow the same
  validate-before-join order. `List()` silently skips directory entries
  that don't match the pattern, and returns results sorted by ID.
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

* `GET /healthcheck` reflects the status of the subsystems the server
  depends on (currently the chat completer's `CheckHealth`), not just process
  liveness. As more subsystems are added, extend the handler to check each
  one and report per-subsystem success/failure rather than collapsing to a
  single boolean — a caller should be able to tell *which* dependency is
  down.
* Shape: `{"status": "ok"|"error", "build_id": "..."}` on success, plus an
  `"error"` field with the underlying message when a subsystem check fails.
  Failure returns `503 Service Unavailable`.

## API error responses

* Handlers return errors via `http.Error(w, message, statusCode)` — plain
  text body, not JSON — for consistency with the standard library idiom used
  throughout `internal/api`. Successful responses are JSON via `writeJSON`.
* Map domain errors to HTTP status in one place per resource (see
  `writeGetError` in `internal/api/json.go`): invalid input → 400, not found
  → 404, anything else → 500 with a generic message (don't leak internal
  error text for 500s).
* Known inconsistency: `/healthcheck` failures are the one exception to the
  plain-text rule above — they return a JSON body (see Healthchecks). The
  frontend's `getHealthStatus` (`frontend/src/api.ts`) parses that JSON
  correctly, but `listTasks`/`listProjects`/`sendChatMessage` only look at
  the HTTP status code and never read the plain-text error body at all.
  This hasn't been reconciled one way or the other (standardize on JSON
  everywhere vs. keep plain-text for REST resources) — treat it as an open
  decision, not an accident to silently "fix" in passing.

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
  regardless of which backend provider is configured — this keeps the
  provider interchangeable per the "providers are replaceable" invariant
  (`docs/architectural invariants.md`).
  New provider integrations should conform to this same request/response
  shape rather than introducing a provider-specific one.

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
* **Tooling**: linting is `oxlint` (not ESLint). `build` runs
  `tsc -b && vite build` (type-check, then bundle). No test framework is
  configured yet.

## General

* Prefer the standard library (`net/http`, `encoding/json`) over frameworks
  for the HTTP layer — see the "prefer open standards" invariant.
