import { useEffect, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import { CopyIcon, DeleteIcon, EditIcon, RegenerateIcon } from './ActionIcons'
import { isAbortError, listAgentExecutors, listModels } from './api'
import { MarkdownMessage } from './MarkdownMessage'
import { ToolActivitySequence } from './ToolActivity'
import { appendTextBlock, appendToolCallBlock, appendToolResultBlock } from './toolActivityBlocks'
import type { ToolActivityBlock } from './toolActivityBlocks'
import type { ChatStreamEvent, Conversation, ConversationMessage } from './types'
import { useLiveTurnStatus } from './useLiveTurnStatus'
import { useStickyAutoScroll } from './useStickyAutoScroll'

interface DisplayMessage {
  role: string
  // content is a derived concatenation of every text segment's text, kept
  // alongside segments purely for handleCopyMessage/handleEditMessage below
  // (Copy and "edit and resend" both need the plain reply text, not its
  // segment structure) — segments is what's rendered.
  content: string
  reasoningContent: string
  toolCallName: string | null
  segments: ToolActivityBlock[]
  error: string | null
  thinkingCollapsed: boolean
  // created_at backs cycleStartAt filtering below — locally-appended
  // messages (sendMessage/startConversation/truncateAndResend) stamp "now",
  // which is always after any past cycleStartAt.
  created_at: string
}

// toDisplaySegments prefers m.segments (the server always populates it —
// see ConversationMessage.EffectiveSegments, internal/task/conversation.go)
// and falls back to synthesizing today's bundled [tools, text] shape from
// m.tool_activity/m.content only when segments is absent, so pre-existing
// test fixtures that construct a bare ConversationMessage literal without
// it keep rendering exactly as before. Historical activities carry no id
// (persisted ConversationToolActivity never has one — see
// toolActivityBlocks.ts) since a reloaded message's calls/results were
// already correlated correctly server-side before being persisted; id only
// matters for live correlation, via handleStreamEvent below.
function toDisplaySegments(m: ConversationMessage): ToolActivityBlock[] {
  if (m.segments && m.segments.length > 0) {
    return m.segments.map((s) =>
      s.kind === 'text'
        ? { kind: 'text', text: s.text }
        : {
            kind: 'tools',
            activities: s.tool_activity.map((a) => ({ name: a.name, arguments: a.arguments, result: a.result, isError: a.is_error })),
          },
    )
  }
  const segments: ToolActivityBlock[] = []
  if (m.tool_activity && m.tool_activity.length > 0) {
    segments.push({
      kind: 'tools',
      activities: m.tool_activity.map((a) => ({ name: a.name, arguments: a.arguments, result: a.result, isError: a.is_error })),
    })
  }
  if (m.content) {
    segments.push({ kind: 'text', text: m.content })
  }
  return segments
}

// mergeDraftDefaults fills in emptyDraft's fields the model's tool call
// omitted, one level deep — the model frequently proposes a partial object
// (e.g. context.summary but no context.files), and a plain shallow spread
// would replace an entire nested object with an incomplete one, leaving
// e.g. draft.context.files undefined instead of []. Generic over D since
// this component doesn't know Draft's shape (RequirementsDraft nests a
// context object, TaskPlan is flat) — this only needs to recurse one level
// to cover both.
function mergeDraftDefaults<D>(emptyDraft: D, parsed: Partial<D>): D {
  const merged: D = { ...emptyDraft, ...parsed }
  for (const key of Object.keys(emptyDraft as object) as (keyof D)[]) {
    const defaultValue = emptyDraft[key]
    const parsedValue = parsed[key]
    if (isPlainObject(defaultValue) && isPlainObject(parsedValue)) {
      merged[key] = { ...defaultValue, ...parsedValue }
    }
  }
  return merged
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

// askQuestionToolName mirrors drafttool.AskQuestionName
// (internal/drafttool/drafttool.go) — no shared codegen between the Go and
// TypeScript sides, so this string is kept in sync by convention, the same
// way ReviewPanel.tsx's proposeKnowledgeToolName is. Offered alongside the
// stage's own Draft-proposing tool on Requirements/Planning (stageTool,
// internal/api/stage_conversation.go): a structured alternative to writing
// a question's options/recommendation into prose, rendered below as
// clickable choices next to the normal reply textarea.
const askQuestionToolName = 'ask_question'

// AskQuestionArgs is ask_question's tool-call argument shape (the wire
// shape drafttool.go's askQuestionSchema describes) — not a mirror of any
// persisted Go struct, since a call to this tool is never written to disk:
// the human's answer (typed or clicked) is just the next plain chat
// message, so this only exists to drive the option buttons below for the
// one turn that proposed it.
interface AskQuestionArgs {
  options: string[]
  recommended_option?: string
  recommendation_reason?: string
}

// parseAskQuestionArgs tolerates a schema violation the model produced
// anyway (most importantly a missing/non-array `options`) by falling back
// to an empty list rather than throwing — so the question just renders with
// no buttons instead of crashing the panel. A JSON.parse failure still
// throws, same as every other tool-call arguments parse in this file, left
// for the caller's try/catch.
function parseAskQuestionArgs(argumentsJSON: string): AskQuestionArgs {
  const parsed = JSON.parse(argumentsJSON) as Partial<AskQuestionArgs>
  return {
    options: Array.isArray(parsed.options) ? parsed.options.map(String) : [],
    recommended_option: typeof parsed.recommended_option === 'string' ? parsed.recommended_option : undefined,
    recommendation_reason: typeof parsed.recommendation_reason === 'string' ? parsed.recommendation_reason : undefined,
  }
}

// isMaxTurnsError matches the SDK's "Reached maximum number of turns (N)"
// failure text (internal/agentrunner/claude_runner.go's processMessage) —
// same regex convention TaskDetailPanel.tsx uses for Execution failures.
// The turn cap resets per message rather than for the whole session
// (verified live against the `claude` CLI's stream-json protocol: a message
// that hits its cap still leaves the session able to answer the very next
// one with a fresh budget), so this is never a dead conversation — just a
// turn that ran out of room, worth explaining plainly rather than showing
// the raw SDK string.
function isMaxTurnsError(error: string): boolean {
  return /maximum number of turns/i.test(error)
}

function toDisplayMessage(m: ConversationMessage): DisplayMessage {
  return {
    role: m.role,
    content: m.content,
    reasoningContent: '',
    toolCallName: m.tool_call?.name ?? null,
    segments: toDisplaySegments(m),
    error: m.error ?? null,
    thinkingCollapsed: true,
    created_at: m.created_at,
  }
}

// SecondaryDraftConfig lets a stage register a second Draft-proposing tool
// that coexists with the stage's own (main) one — today only Review, whose
// propose_knowledge tool (docs/milestones/done/milestone9.md) can be called
// independently of propose_review, any number of times, without ending the
// conversation. Unlike the main draft's Finalize/Request-changes/Dismiss
// trio, this is a plain two-way accept/reject decision (no "needs_changes"
// concept, no state to reopen — a rejected proposal is just more
// conversation the executor can redraft within), so no "request changes"
// affordance is offered here; the human can just reply normally in chat.
export interface SecondaryDraftConfig<S> {
  // toolName is the tool call name that routes here instead of to the main
  // draft (chat.ToolSchema's Name — drafttool.ProposeKnowledgeName today).
  toolName: string
  emptyDraft: S
  heading: string
  renderDraft: (draft: S, onChange: (draft: S) => void) => ReactNode
  onAccept: (draft: S) => Promise<void>
  onReject: (draft: S) => Promise<void>
}

// StageConversationOps is the five conversation operations a
// StageConversationPanel drives — getConversation/postMessage/
// startConversation/deleteMessage/regenerateMessage — factored out as
// props instead of the panel hardcoding taskId+stage api.ts calls itself.
// This is what lets the exact same panel back both a task's GrillMe/
// Planning/Review stage conversations (stageConversationOps, api.ts, closing
// over projectId/taskId/stage) and the pre-creation "New Task" chat
// (taskDraftConversationOps, api.ts, closing over projectId/sessionId
// instead) with no behavior change to the former. Every op omits
// projectId/taskId(/stage) or projectId/sessionId — whichever a given
// caller closes over — since the panel itself has no notion of what's being
// conversed about, only that it can be gotten/posted-to/started/deleted-from/
// regenerated.
export interface StageConversationOps {
  getConversation: () => Promise<Conversation>
  postMessage: (
    content: string,
    model: string,
    executor: string,
    onEvent: (event: ChatStreamEvent) => void,
    signal?: AbortSignal,
  ) => Promise<void>
  startConversation: (
    model: string,
    executor: string,
    onEvent: (event: ChatStreamEvent) => void,
    signal?: AbortSignal,
  ) => Promise<void>
  deleteMessage: (index: number) => Promise<Conversation>
  regenerateMessage: (
    index: number,
    content: string,
    model: string,
    executor: string,
    onEvent: (event: ChatStreamEvent) => void,
    signal?: AbortSignal,
  ) => Promise<void>
}

interface StageConversationPanelProps<D, S = never> {
  // conversationKey uniquely identifies this conversation (e.g.
  // `${projectId}:${taskId}:requirements` or `${projectId}:${sessionId}`) —
  // used only to key the mount effect (so switching to a different
  // conversation re-initializes) and to namespace this instance's DOM
  // element ids; the panel itself never parses or otherwise depends on its
  // shape.
  conversationKey: string
  ops: StageConversationOps
  // readOnly renders a frozen, historical conversation: the message input,
  // Edit/Regenerate/Delete actions, ask_question option buttons, and any
  // pending Draft's action panel are all suppressed — Copy remains, since
  // it can't mutate anything. Used by TaskDraftView.tsx to show a task's
  // pre-creation "New Task" chat after the fact, where the conversation
  // moved on (a task now exists) and must not be reopened or re-answered
  // from this view.
  readOnly?: boolean
  // title/description label this interview so it reads as a distinct,
  // interactive zone rather than more of the read-only summary above it
  // (TaskDetailPanel wraps the read-only fields and this panel in
  // separately styled containers for the same reason).
  title: string
  description: string
  emptyDraft: D
  renderDraft: (draft: D, onChange: (draft: D) => void) => ReactNode
  onFinalize: (draft: D) => Promise<void>
  // normalizeDraft runs right after a proposed tool call is merged into D
  // (both on the streamed tool_call event and on rehydration from a saved
  // conversation) — the one seam to repair a shape the model got wrong
  // despite the tool's JSON Schema (e.g. propose_context's `files` is
  // schema'd as {path, role}[] (docs/adr/0021), but a model has been
  // observed emitting bare path strings there instead). Left undefined,
  // the parsed draft is used as-is. Backend Finalize decodes into a fixed
  // Go struct, so an unrepaired shape mismatch there surfaces only as an
  // opaque "invalid request body" 400 — normalizing here, before the human
  // ever sees the draft, fixes both that and the editable form rendering
  // it correctly.
  normalizeDraft?: (draft: D) => D
  // autoStart controls whether the panel fires its opening turn the moment
  // it mounts on an empty conversation, or waits for an explicit Start
  // click. All current callers (GrillMe, Planning, Review) pass false: the
  // model/executor pickers need to be settled by the human before the first
  // turn fires, since the mount effect's auto-resolved executor default
  // would otherwise start the conversation before the picker is
  // interactive. Left as an opt-in flag (rather than removed) for a future
  // stage that genuinely wants to fire on mount.
  autoStart?: boolean
  // startLabel names the explicit Start button shown when autoStart is false
  // (e.g. "Start Review"); ignored when autoStart is true.
  startLabel?: string
  // cycleStartAt, when set, is the timestamp this stage was most recently
  // (re-)entered — e.g. the latest review→implementation→review round's
  // start. A stage's Conversation is one continuous, never-reset file
  // across every revisit (Review can be entered many times over a task's
  // life), so a tool call proposed in an earlier round is still the "most
  // recent" one in that file until a new round adds to it. Without this,
  // reopening the stage after a needs_changes verdict silently rehydrates
  // that earlier round's already-finalized Draft as if it were fresh,
  // letting Finalize resubmit stale, un-reviewed feedback against a diff
  // that's since changed — this is exactly the mechanism that produced
  // three byte-identical "needs_changes" reviews in a row on a real task.
  // Left undefined (GrillMe, Planning — not yet revisited this way in
  // practice), rehydration behaves exactly as before.
  cycleStartAt?: string
  // draftIsEditable controls what Request Changes does with the main Draft.
  // true (default — GrillMe, Planning): renderDraft's fields are real
  // inputs, so Request Changes forwards the (possibly human-edited) draft
  // back to the model as a fenced JSON block, plus an optional typed
  // comment, per handleRequestChanges' own doc comment. false (Review):
  // renderDraft has no real inputs to edit — a verdict isn't something to
  // hand-tweak, only to accept or send back — so Request Changes just closes
  // the draft panel and reveals the ordinary reply box beneath it, with no
  // comment field of its own, since the human's next plain message already
  // reaches the model the normal way.
  draftIsEditable?: boolean
  // secondaryDraft, if set, additionally tracks and renders a second Draft
  // tool independent of the main one — see SecondaryDraftConfig.
  secondaryDraft?: SecondaryDraftConfig<S>
}

// localChatOption represents the local-LLM chat path (server-side, "" maps
// to the same health-checked "local" AgentRunner every other executor goes
// through — resolveStageStreamTarget, internal/api/stage_conversation.go).
// It is only ever added to executorOptions when listAgentExecutors reports
// "local" itself as healthy — offering it unconditionally would let the
// human pick an executor that's known not to be responding.
const localChatOption = { value: '', label: 'Local LLM chat' }

// executorLabels maps an agent executor key (internal/agentrunner) to its
// display label, for whichever keys listAgentExecutors currently reports
// healthy — an executor that isn't live right now is never offered, so
// selecting one can't 400.
const executorLabels: Record<string, string> = { 'claude-code': 'Claude Code', codex: 'Codex CLI' }

// StageConversationPanel is the mechanism shared by GrillMe, Planning Mode,
// and Review (CONTEXT.md): a persisted Conversation transcript, a message
// input that streams the assistant's reply, and — when the model calls its
// registered tool — a Draft, shown via renderDraft, that takes over the
// reply box's spot until the human Finalizes it, asks for changes, or
// dismisses it (which just clears local state; no API call). See
// draftIsEditable for the one behavioral difference between stages: whether
// the draft's fields are real inputs the human can hand-edit.
export function StageConversationPanel<D, S = never>({
  conversationKey,
  ops,
  readOnly = false,
  title,
  description,
  emptyDraft,
  renderDraft,
  onFinalize,
  autoStart = true,
  startLabel,
  cycleStartAt,
  draftIsEditable = true,
  secondaryDraft,
  normalizeDraft,
}: StageConversationPanelProps<D, S>) {
  const [messages, setMessages] = useState<DisplayMessage[]>([])
  const [loadError, setLoadError] = useState<string | null>(null)
  // initializing is true until the mount effect has resolved the existing
  // conversation (and models/executors) — gates the explicit-start button so
  // it can't flash before we know whether the conversation is actually empty.
  const [initializing, setInitializing] = useState(true)
  // resolvedStart holds the model/executor the mount effect settled on, so a
  // later Start click fires with the same values auto-start would have used
  // (reading selectedModel/executor state is fine post-init, but this keeps
  // the manual and automatic paths identical).
  const resolvedStart = useRef({ model: '', executor: '' })
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [selectedModel, setSelectedModel] = useState('')
  const [executor, setExecutor] = useState('')
  // executorOptions starts empty rather than defaulting to [localChatOption]
  // — until listAgentExecutors actually reports "local" healthy, offering it
  // would be the same silent-default bug this and executorsError below both
  // guard against.
  const [executorOptions, setExecutorOptions] = useState<{ value: string; label: string }[]>([])
  // executorsError is set when listAgentExecutors itself fails (server
  // unreachable, 500, etc.) — distinct from "the request succeeded and
  // reported zero healthy executors", which is a legitimate state that
  // leaves this null. Without this, a fetch failure and "nothing healthy"
  // looked identical: the picker silently fell back to Local LLM chat with
  // no indication the server couldn't even be reached.
  const [executorsError, setExecutorsError] = useState<string | null>(null)
  const [pendingDraft, setPendingDraft] = useState<D | null>(null)
  // pendingQuestion holds the most recent ask_question call still awaiting
  // an answer — rendered as option buttons beside the reply textarea below.
  // Unlike pendingDraft/pendingSecondaryDraft, it's cleared the instant any
  // reply goes out (sendMessage), typed or clicked, since a question is
  // "answered" by the human's very next message either way — there is
  // nothing to Finalize or Discard.
  const [pendingQuestion, setPendingQuestion] = useState<AskQuestionArgs | null>(null)
  const [finalizing, setFinalizing] = useState(false)
  const [finalizeError, setFinalizeError] = useState<string | null>(null)
  // pendingSecondaryDraft/secondaryFinalizing/secondaryError mirror the
  // main draft's own trio, for secondaryDraft (see SecondaryDraftConfig) —
  // kept entirely independent so both a review verdict and a knowledge
  // proposal can be pending at the same time.
  const [pendingSecondaryDraft, setPendingSecondaryDraft] = useState<S | null>(null)
  const [secondaryFinalizing, setSecondaryFinalizing] = useState(false)
  const [secondaryError, setSecondaryError] = useState<string | null>(null)
  const [requestChangesComment, setRequestChangesComment] = useState('')
  // editingIndex is the position of the user message currently being
  // edited (draft holds its in-progress edited text), or null when not
  // editing — mutually exclusive with normal sending.
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  // abortControllerRef tracks the in-flight stream's controller so the
  // Stop button can cancel it — a ref rather than state since aborting
  // never needs to trigger a re-render itself (setSending's finally
  // already does that).
  const abortControllerRef = useRef<AbortController | null>(null)
  // streamedChars/finalTokens feed the live token-estimate indicator
  // (docs/adr/0018): streamedChars accumulates as content/reasoning_content
  // deltas arrive, finalTokens is set once (if ever) a stream's real usage
  // total arrives — both reset to their empty state at the start of every
  // new turn (sendMessage/startConversation/truncateAndResend).
  const [streamedChars, setStreamedChars] = useState(0)
  const [finalTokens, setFinalTokens] = useState<number | undefined>(undefined)
  const liveTurnStatus = useLiveTurnStatus(sending, streamedChars, finalTokens)
  const historyRef = useStickyAutoScroll(messages)

  useEffect(() => {
    let cancelled = false

    async function init() {
      let loadedEmpty = false

      try {
        const conv = await ops.getConversation()
        const loaded = conv.messages ?? []
        if (cancelled) {
          return
        }
        setMessages(loaded.map(toDisplayMessage))
        loadedEmpty = loaded.length === 0
        // A read-only view (TaskDraftView.tsx) only ever shows the
        // transcript — no pending Draft/question to rehydrate (there's
        // nothing here to Finalize or answer), no model/executor picker,
        // and never an auto-started turn against a frozen conversation.
        if (readOnly) {
          setInitializing(false)
          return
        }
        // Rehydrate the pending draft(s) from the most recent proposed tool
        // call of each kind — a reload or server restart must not lose a
        // proposal the human hasn't finalized or discarded yet, since it's
        // already sat for review in a prior session. Walked once, tracking
        // the main and secondary tool calls independently since they can be
        // interleaved throughout the conversation. Note: this can re-surface
        // a secondaryDraft proposal that was already accepted/rejected in a
        // prior session, since accept/reject leave no mark on the
        // conversation history itself — accepted/lightly re-confirms only
        // (KnowledgeStore.Put is a no-op re-write; reject was always a
        // no-op), not a correctness issue, just a UX rough edge.
        //
        // Restricted to messages at or after cycleStartAt when set (see its
        // doc comment): a tool call from an earlier round of a revisited
        // stage is history, not a live pending proposal — rehydrating it
        // would let Finalize resubmit a verdict about a diff that's since
        // moved on, silently and indistinguishably from a fresh one.
        const rehydrationPool = cycleStartAt ? loaded.filter((m) => m.created_at >= cycleStartAt) : loaded
        let latestMainCall: { arguments: string } | undefined
        let latestSecondaryCall: { arguments: string } | undefined
        for (let i = rehydrationPool.length - 1; i >= 0; i--) {
          const toolCall = rehydrationPool[i].tool_call
          if (!toolCall || toolCall.name === askQuestionToolName) {
            // ask_question is handled separately below (only resurfaced
            // when it's the very last message) — skipped here so it's
            // never mistaken for a RequirementsDraft/TaskPlan proposal by
            // the "any non-secondary tool call is the main draft" fallback
            // below.
            continue
          }
          if (secondaryDraft && toolCall.name === secondaryDraft.toolName) {
            latestSecondaryCall ??= toolCall
          } else {
            latestMainCall ??= toolCall
          }
          if (latestMainCall && (!secondaryDraft || latestSecondaryCall)) {
            break
          }
        }
        if (latestMainCall) {
          try {
            const merged = mergeDraftDefaults(emptyDraft, JSON.parse(latestMainCall.arguments))
            setPendingDraft(normalizeDraft ? normalizeDraft(merged) : merged)
          } catch {
            // Malformed arguments JSON from a past turn — nothing to
            // rehydrate, the human can just keep chatting.
          }
        }
        if (secondaryDraft && latestSecondaryCall) {
          try {
            setPendingSecondaryDraft(mergeDraftDefaults(secondaryDraft.emptyDraft, JSON.parse(latestSecondaryCall.arguments)))
          } catch {
            // Same as above.
          }
        }
        // ask_question rehydrates only off the very last loaded message,
        // unlike the Draft proposals above — a question from several turns
        // back was already answered by whatever the human said next, so
        // resurfacing it here would offer stale, already-moot options. Also
        // restricted to rehydrationPool for the same reason as the Draft
        // proposals above.
        const lastToolCall = rehydrationPool[rehydrationPool.length - 1]?.tool_call
        if (lastToolCall?.name === askQuestionToolName) {
          try {
            setPendingQuestion(parseAskQuestionArgs(lastToolCall.arguments))
          } catch {
            // Malformed arguments JSON — no buttons to rehydrate, the
            // human can just keep chatting.
          }
        }
      } catch (err) {
        // Can't confirm the conversation is actually empty, so don't risk
        // auto-starting on top of unknown state — the human can still send
        // a message manually once the load error is visible.
        if (!cancelled) {
          setLoadError(err instanceof Error ? err.message : String(err))
          setInitializing(false)
        }
        return
      }

      let resolvedModel = ''
      try {
        const result = await listModels()
        if (!cancelled) {
          setModels(result.models)
          resolvedModel = result.models[0] ?? ''
          setSelectedModel((current) => current || resolvedModel)
        }
      } catch (err) {
        if (!cancelled) {
          setModelsError(err instanceof Error ? err.message : String(err))
        }
      }

      let resolvedExecutor = ''
      try {
        const result = await listAgentExecutors()
        if (!cancelled) {
          // "local" resolves server-side to the exact same health-checked
          // AgentRunner as the "" (localChatOption) value — resolveStageStreamTarget
          // maps "" to defaultChatExecutor ("local") before the lookup — so
          // its presence here is the live signal for whether local chat is
          // actually reachable right now. Split out (not just mapped
          // alongside the rest) so it's represented by localChatOption's ""
          // value instead of a second, redundant "local"-keyed entry.
          const localHealthy = result.executors.includes('local')
          const executors = result.executors.filter((key) => key !== 'local')
          setExecutorOptions([
            ...(localHealthy ? [localChatOption] : []),
            ...executors.map((key) => ({ value: key, label: executorLabels[key] ?? key })),
          ])
          // claude-code can ground its questions in the actual repository
          // (Read/Grep/Glob), unlike the local chat path, so it's preferred
          // whenever it's healthy — the picker still lets the human switch
          // to local chat when that's healthy too.
          if (executors.includes('claude-code')) {
            resolvedExecutor = 'claude-code'
            setExecutor((current) => current || 'claude-code')
          }
        }
      } catch (err) {
        // The request itself failed (server unreachable, 500, etc.) — as
        // opposed to succeeding with zero healthy executors, which isn't an
        // error and just leaves executorOptions empty. Surfaced below so a
        // fetch failure reads as exactly that, not a silent "no executors"
        // default.
        if (!cancelled) {
          setExecutorsError(err instanceof Error ? err.message : String(err))
        }
      }

      if (cancelled) {
        return
      }
      resolvedStart.current = { model: resolvedModel, executor: resolvedExecutor }
      setInitializing(false)

      // A brand-new conversation has nothing for the human to reply to —
      // GrillMe/Planning Mode asks the opening question itself instead of
      // waiting on a message that doesn't exist yet. When autoStart is false
      // (Review), the human fires the opening turn explicitly via handleStart.
      if (loadedEmpty && autoStart) {
        await startConversation(resolvedModel, resolvedExecutor)
      }
    }

    init()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversationKey, readOnly])

  function updateLastMessage(update: (msg: DisplayMessage) => DisplayMessage) {
    setMessages((prev) => {
      if (prev.length === 0) {
        return prev
      }
      const next = prev.slice()
      next[next.length - 1] = update(next[next.length - 1])
      return next
    })
  }

  // updateMessageAt is updateLastMessage's general form, for surfacing a
  // delete failure inline on the specific message that failed to delete
  // (not necessarily the last one).
  function updateMessageAt(index: number, update: (msg: DisplayMessage) => DisplayMessage) {
    setMessages((prev) => {
      if (index < 0 || index >= prev.length) {
        return prev
      }
      const next = prev.slice()
      next[index] = update(next[index])
      return next
    })
  }

  // handleStreamEvent applies one streamed ChatStreamEvent to the
  // in-progress last message — shared by sendMessage's postStageMessage
  // call and startConversation's startStageConversation call, since both
  // stream the identical event shape onto an assistant bubble that's
  // already been appended by their respective callers.
  function handleStreamEvent(event: ChatStreamEvent) {
    if (event.error) {
      // A user-initiated Stop cancels the request context, which the
      // backend surfaces as a normal SSE error event (e.g. "context
      // canceled") rather than a thrown exception — isAbortError's catch-
      // block check never sees it. Checking the controller's own signal
      // here is what actually distinguishes "stopped on purpose" from a
      // real failure, so a deliberate stop doesn't read as an error.
      if (abortControllerRef.current?.signal.aborted) {
        return
      }
      updateLastMessage((msg) => ({ ...msg, error: event.error! }))
      return
    }
    if (event.tool_call) {
      updateLastMessage((msg) => ({ ...msg, toolCallName: event.tool_call!.name }))
      if (event.tool_call.name === askQuestionToolName) {
        // Checked before the main/secondary draft bucketing below, so
        // ask_question is never misparsed as a RequirementsDraft/TaskPlan —
        // same reasoning as the rehydration walk's skip above.
        try {
          setPendingQuestion(parseAskQuestionArgs(event.tool_call.arguments))
        } catch {
          // Malformed arguments JSON is surfaced via the chip only; the
          // human can keep chatting and ask the model to try again.
        }
        return
      }
      if (secondaryDraft && event.tool_call.name === secondaryDraft.toolName) {
        try {
          setPendingSecondaryDraft(mergeDraftDefaults(secondaryDraft.emptyDraft, JSON.parse(event.tool_call.arguments)))
        } catch {
          // Malformed arguments JSON is surfaced via the chip only; the
          // human can keep chatting and ask the model to try again.
        }
        return
      }
      try {
        const merged = mergeDraftDefaults(emptyDraft, JSON.parse(event.tool_call.arguments))
        setPendingDraft(normalizeDraft ? normalizeDraft(merged) : merged)
      } catch {
        // Malformed arguments JSON is surfaced via the chip only; the
        // human can keep chatting and ask the model to try again.
      }
      return
    }
    if (event.tool_activity) {
      const ta = event.tool_activity
      if (ta.phase === 'call') {
        updateLastMessage((msg) => ({ ...msg, segments: appendToolCallBlock(msg.segments, { id: ta.id, name: ta.name, arguments: ta.arguments }) }))
      } else {
        updateLastMessage((msg) => ({ ...msg, segments: appendToolResultBlock(msg.segments, ta.id, ta.result ?? '', ta.is_error) }))
      }
      return
    }
    if (event.usage) {
      setFinalTokens(event.usage.total_tokens)
    }
    if (event.reasoning_content) {
      updateLastMessage((msg) => ({ ...msg, reasoningContent: msg.reasoningContent + event.reasoning_content }))
      setStreamedChars((n) => n + event.reasoning_content!.length)
    }
    if (event.content) {
      updateLastMessage((msg) => ({
        ...msg,
        segments: appendTextBlock(msg.segments, event.content!),
        content: msg.content + event.content,
        thinkingCollapsed: true,
      }))
      setStreamedChars((n) => n + event.content!.length)
    }
  }

  async function sendMessage(text: string) {
    if (!text || sending) {
      return
    }

    setMessages((prev) => [
      ...prev,
      { role: 'user', content: text, reasoningContent: '', toolCallName: null, segments: [{ kind: 'text', text }], error: null, thinkingCollapsed: true, created_at: new Date().toISOString() },
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, segments: [], error: null, thinkingCollapsed: false, created_at: new Date().toISOString() },
    ])
    setSending(true)
    setStreamedChars(0)
    setFinalTokens(undefined)
    // Sending any reply — typed or via an ask_question option click, which
    // just calls this same function with the clicked label as text —
    // answers whatever question was pending, so it's cleared here rather
    // than only on the click path.
    setPendingQuestion(null)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await ops.postMessage(text, selectedModel, executor, handleStreamEvent, controller.signal)
    } catch (err) {
      if (!isAbortError(err)) {
        // A rejection here (as opposed to a mid-stream {error} SSE event,
        // handled by handleStreamEvent above) means the request failed
        // before the server ever sent its streaming response headers —
        // handlePostStageMessage only reaches AppendConversationMessages
        // after those headers are sent, so nothing was persisted. Leaving
        // the optimistic [user, assistant] pair appended above in local
        // state would permanently diverge from the server's conversation —
        // exactly what previously surfaced later as a "message index N out
        // of range" 400 from Edit/Regenerate/Delete acting on a message
        // the server never knew existed. Roll it back and hand the typed
        // text back to the draft box so nothing is lost.
        setMessages((prev) => prev.slice(0, -2))
        setDraft(text)
      }
    } finally {
      abortControllerRef.current = null
      setSending(false)
    }
  }

  // handleStop aborts the in-flight stream (Stop button) — Go's net/http
  // cancels the handler's request context when the client aborts the
  // connection, so this actually interrupts the backend run, not just the
  // frontend's rendering of it.
  function handleStop() {
    abortControllerRef.current?.abort()
  }

  // startConversation kicks off a brand-new, empty stage Conversation on
  // load (see the init effect above) — there's no human message to pair
  // with the opening question, so only an assistant bubble is appended.
  // model/executorKey are passed explicitly rather than read from
  // selectedModel/executor state, since this runs as part of the same
  // effect that resolves them — reading state here could race against
  // React not having flushed those updates yet.
  async function startConversation(model: string, executorKey: string) {
    setMessages((prev) => [
      ...prev,
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, segments: [], error: null, thinkingCollapsed: false, created_at: new Date().toISOString() },
    ])
    setSending(true)
    setStreamedChars(0)
    setFinalTokens(undefined)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await ops.startConversation(model, executorKey, handleStreamEvent, controller.signal)
    } catch (err) {
      if (!isAbortError(err)) {
        updateLastMessage((msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
      }
    } finally {
      abortControllerRef.current = null
      setSending(false)
    }
  }

  // handleStart fires the opening turn for an autoStart=false panel (Review),
  // using the model/executor the mount effect resolved — the explicit analog
  // of the automatic startConversation call GrillMe/Planning make on mount.
  async function handleStart() {
    if (sending) {
      return
    }
    await startConversation(resolvedStart.current.model, resolvedStart.current.executor)
  }

  async function handleSend() {
    const text = draft.trim()
    if (!text || sending) {
      return
    }
    if (editingIndex !== null) {
      const index = editingIndex
      setEditingIndex(null)
      setDraft('')
      await truncateAndResend(index, text)
      return
    }
    setDraft('')
    await sendMessage(text)
  }

  // truncateAndResend discards everything from index onward (locally and,
  // via regenerateStageMessage, server-side) and resends content as a
  // fresh [user, assistant] pair — the shared core of both Edit (index is
  // the edited message's own position, content is the new text) and
  // Regenerate (index is the preceding user message, content is its
  // existing text unchanged). Any pending Draft is cleared: whatever tool
  // call produced it may no longer be part of history after truncation.
  async function truncateAndResend(index: number, content: string) {
    if (sending) {
      return
    }
    const previousMessages = messages
    setMessages((prev) => [
      ...prev.slice(0, index),
      { role: 'user', content, reasoningContent: '', toolCallName: null, segments: [{ kind: 'text', text: content }], error: null, thinkingCollapsed: true, created_at: new Date().toISOString() },
      { role: 'assistant', content: '', reasoningContent: '', toolCallName: null, segments: [], error: null, thinkingCollapsed: false, created_at: new Date().toISOString() },
    ])
    setPendingDraft(null)
    setPendingQuestion(null)
    setSending(true)
    setStreamedChars(0)
    setFinalTokens(undefined)
    const controller = new AbortController()
    abortControllerRef.current = controller

    try {
      await ops.regenerateMessage(index, content, selectedModel, executor, handleStreamEvent, controller.signal)
    } catch (err) {
      if (!isAbortError(err)) {
        // Same reasoning as sendMessage's catch: a rejection here means
        // handleRegenerateStageMessage failed before it ever reached
        // ReplaceConversationMessages, so nothing changed server-side.
        // Restore local state to exactly what it was before this attempt
        // (not just the truncated prefix) so it can't drift from the
        // server's real conversation, and hand the content back to the
        // draft box so a retry doesn't require retyping it.
        setMessages(previousMessages)
        setDraft(content)
      }
    } finally {
      abortControllerRef.current = null
      setSending(false)
    }
  }

  function handleCopyMessage(content: string) {
    void navigator.clipboard.writeText(content)
  }

  function handleEditMessage(index: number) {
    setEditingIndex(index)
    setDraft(messages[index].content)
  }

  function handleCancelEdit() {
    setEditingIndex(null)
    setDraft('')
  }

  // handleRegenerateMessage targets assistantIndex's preceding user
  // message (a well-formed conversation always alternates user/assistant,
  // so assistantIndex - 1 is that user turn) and resends its existing
  // content unchanged.
  function handleRegenerateMessage(assistantIndex: number) {
    const userIndex = assistantIndex - 1
    if (userIndex < 0 || sending) {
      return
    }
    void truncateAndResend(userIndex, messages[userIndex].content)
  }

  async function handleDeleteMessage(index: number) {
    if (sending) {
      return
    }
    try {
      await ops.deleteMessage(index)
      setMessages((prev) => prev.filter((_, i) => i !== index))
      if (editingIndex === index) {
        setEditingIndex(null)
        setDraft('')
      }
    } catch (err) {
      updateMessageAt(index, (msg) => ({ ...msg, error: err instanceof Error ? err.message : String(err) }))
    }
  }

  // handleAnswerQuestion sends a clicked ask_question option through the
  // exact same sendMessage path a typed reply uses — the transcript shows
  // a plain user message with the option's text, indistinguishable from
  // the human having typed it (sendMessage itself clears pendingQuestion).
  function handleAnswerQuestion(option: string) {
    if (sending) {
      return
    }
    void sendMessage(option)
  }

  // Enter sends the reply, matching most chat UIs; Alt+Enter inserts a
  // newline instead, for the rare multi-line reply. The newline is spliced
  // in at the cursor explicitly (rather than left to the textarea's
  // default handling) so the cursor lands in the right place regardless of
  // modifier keys, since a plain, unmodified Enter is the one case a
  // browser textarea inserts a newline for by default — Alt+Enter isn't.
  // The textarea is disabled while sending, so this never fires mid-stream.
  function handleDraftKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Enter') {
      return
    }
    if (e.altKey) {
      e.preventDefault()
      const el = e.currentTarget
      const start = el.selectionStart ?? draft.length
      const end = el.selectionEnd ?? draft.length
      const next = draft.slice(0, start) + '\n' + draft.slice(end)
      el.value = next
      el.selectionStart = el.selectionEnd = start + 1
      setDraft(next)
      return
    }
    e.preventDefault()
    void handleSend()
  }

  // handleRequestChanges's behavior depends on draftIsEditable (see its doc
  // comment). Editable: sends the human's edited draft back to the model as
  // a single message — the comment plus the current (edited) draft as a
  // fenced JSON block — through the same postStageMessage path a normal
  // reply uses, so the transcript shows exactly what the model saw (no
  // hidden state). The system prompt (buildStagePrompt) tells the model to
  // treat such a block as the authoritative starting point for its
  // revision. Not editable: there's no edit to forward, so this just closes
  // the draft panel — the reply box that reappears once pendingDraft clears
  // is where the human explains what should change, as an ordinary message.
  async function handleRequestChanges() {
    if (!pendingDraft || sending) {
      return
    }
    if (!draftIsEditable) {
      setPendingDraft(null)
      return
    }
    const comment = requestChangesComment.trim()
    const text = `${comment || 'Please revise the draft below.'}\n\n\`\`\`json\n${JSON.stringify(pendingDraft, null, 2)}\n\`\`\``
    setRequestChangesComment('')
    setPendingDraft(null)
    await sendMessage(text)
  }

  async function handleFinalize() {
    if (!pendingDraft || finalizing) {
      return
    }
    setFinalizing(true)
    setFinalizeError(null)
    try {
      await onFinalize(pendingDraft)
      setPendingDraft(null)
    } catch (err) {
      setFinalizeError(err instanceof Error ? err.message : String(err))
    } finally {
      setFinalizing(false)
    }
  }

  // handleSecondaryDecision drives both Accept and Reject for
  // secondaryDraft — same shape, differing only in which of
  // onAccept/onReject runs, mirroring how handleFinalize just runs
  // onFinalize with whatever decision is already encoded in the draft.
  async function handleSecondaryDecision(action: (draft: S) => Promise<void>) {
    if (!secondaryDraft || !pendingSecondaryDraft || secondaryFinalizing) {
      return
    }
    setSecondaryFinalizing(true)
    setSecondaryError(null)
    try {
      await action(pendingSecondaryDraft)
      setPendingSecondaryDraft(null)
    } catch (err) {
      setSecondaryError(err instanceof Error ? err.message : String(err))
    } finally {
      setSecondaryFinalizing(false)
    }
  }

  // notStarted is the pre-Start state of an autoStart=false panel (Review):
  // the mount effect has finished, the conversation is genuinely empty, and
  // nothing errored — so show the explicit Start button and withhold the
  // reply box until the human fires the opening turn. Never true in
  // readOnly mode — a frozen conversation is never started from here.
  const notStarted = !readOnly && !autoStart && !initializing && messages.length === 0 && !loadError

  // needsCycleKickoff is notStarted's counterpart for a revisited stage: the
  // Conversation already has history from an earlier round, but nothing has
  // been said yet in *this* round per cycleStartAt. Unlike notStarted, this
  // doesn't need its own action — the ordinary reply box below already
  // works (postMessage/resolveStageRun re-resolves the latest execution's
  // context fresh on every turn, not just a dedicated "start" call), so
  // typing anything and sending it already does the right thing. This only
  // drives an explanatory note so that's not left implicit.
  const needsCycleKickoff =
    !readOnly &&
    !autoStart &&
    !initializing &&
    !loadError &&
    cycleStartAt !== undefined &&
    messages.length > 0 &&
    !messages.some((m) => m.created_at >= cycleStartAt)

  return (
    <div className="stage-conversation">
      <div className="stage-conversation-header">
        <h4>{title}</h4>
        <p className="stage-conversation-intro">{description}</p>
      </div>

      {!readOnly && (
        <>
          <div className="chat-model-row">
            <label htmlFor={`stage-executor-${conversationKey}`}>Executor</label>
            <select
              id={`stage-executor-${conversationKey}`}
              value={executor}
              onChange={(e) => setExecutor(e.target.value)}
              disabled={executorOptions.length === 0}
            >
              {executorOptions.length === 0 && <option value="">No executor available</option>}
              {executorOptions.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>

            {!executor && executorOptions.some((opt) => opt.value === '') && (
              <>
                <label htmlFor={`stage-model-${conversationKey}`}>Model</label>
                <select id={`stage-model-${conversationKey}`} value={selectedModel} onChange={(e) => setSelectedModel(e.target.value)} disabled={models.length === 0}>
                  {models.length === 0 && <option value="">No models available</option>}
                  {models.map((model) => (
                    <option key={model} value={model}>
                      {model}
                    </option>
                  ))}
                </select>
              </>
            )}
          </div>

          {executorsError && <p className="error">Could not reach the server for agent executors: {executorsError}</p>}
          {!executor && executorOptions.some((opt) => opt.value === '') && modelsError && (
            <p className="error">Could not load models: {modelsError}</p>
          )}
        </>
      )}

      {loadError && <p className="error">Could not load conversation: {loadError}</p>}

      {notStarted && (
        <div className="stage-start">
          <button type="button" onClick={handleStart} disabled={sending}>
            {startLabel ?? 'Start'}
          </button>
        </div>
      )}

      <div className="chat-history" ref={historyRef}>
        {messages.map((message, index) => (
          <div key={index} className={`chat-message chat-message-${message.role}`}>
            {message.reasoningContent && (
              <details className="thinking-panel" open={!message.thinkingCollapsed}>
                <summary>Thinking</summary>
                <div className="thinking-content">{message.reasoningContent}</div>
              </details>
            )}
            <strong>{message.role}:</strong>
            {message.segments.map((segment, segIndex) => (
              <div key={segIndex}>
                {segment.kind === 'text' && <MarkdownMessage content={segment.text} />}
                {segment.kind === 'tools' && (
                  <ToolActivitySequence
                    activities={segment.activities}
                    live={sending && index === messages.length - 1 && segIndex === message.segments.length - 1}
                  />
                )}
              </div>
            ))}
            {message.toolCallName && message.toolCallName !== askQuestionToolName && (
              <span className="tool-call-chip">Proposed a draft ({message.toolCallName})</span>
            )}
            {message.toolCallName === askQuestionToolName && <span className="tool-call-chip">Asked a question</span>}
            {message.error &&
              (isMaxTurnsError(message.error) && !readOnly ? (
                <div className="error max-turns-notice">
                  <p>
                    This turn ran out of its turn budget before finishing — reply below to continue, or click Continue.
                    The conversation isn't stuck: the next turn gets a fresh budget, it just doesn't restart on its own.
                  </p>
                  <button
                    type="button"
                    className="action-btn"
                    onClick={() => sendMessage('Please continue where you left off.')}
                    disabled={sending}
                  >
                    Continue
                  </button>
                </div>
              ) : (
                <p className="error">{message.error}</p>
              ))}
            <div className="message-actions">
              <button type="button" className="action-btn" title="Copy" aria-label="Copy" onClick={() => handleCopyMessage(message.content)}>
                <CopyIcon />
              </button>
              {!readOnly && message.role === 'user' && (
                <button
                  type="button"
                  className="action-btn"
                  title="Edit and continue from here"
                  aria-label="Edit"
                  onClick={() => handleEditMessage(index)}
                  disabled={sending}
                >
                  <EditIcon />
                </button>
              )}
              {!readOnly && message.role === 'assistant' && index > 0 && (
                <button
                  type="button"
                  className="action-btn"
                  title="Regenerate"
                  aria-label="Regenerate"
                  onClick={() => handleRegenerateMessage(index)}
                  disabled={sending}
                >
                  <RegenerateIcon />
                </button>
              )}
              {!readOnly && (
                <button
                  type="button"
                  className="action-btn"
                  title="Delete"
                  aria-label="Delete"
                  onClick={() => handleDeleteMessage(index)}
                  disabled={sending}
                >
                  <DeleteIcon />
                </button>
              )}
            </div>
          </div>
        ))}
      </div>

      {!notStarted && !readOnly && !pendingDraft && (
      <>
      {pendingQuestion && pendingQuestion.options.length > 0 && (
        // Structured alternative to reading the recommended answer out of
        // prose and retyping it: each option is a real button that sends
        // its own label through handleAnswerQuestion, so clicking one is
        // indistinguishable in the transcript from having typed it. The
        // free-text textarea below stays live the whole time — a
        // recommendation is a default to accept or redirect, never the
        // only way to answer.
        <div className="ask-question-options" role="group" aria-label="Suggested answers">
          {pendingQuestion.options.map((option) => {
            const isRecommended = option === pendingQuestion.recommended_option
            return (
              <button
                key={option}
                type="button"
                className={isRecommended ? 'ask-question-option ask-question-option-recommended' : 'ask-question-option'}
                onClick={() => handleAnswerQuestion(option)}
                disabled={sending}
                title={isRecommended ? pendingQuestion.recommendation_reason : undefined}
              >
                {option}
                {isRecommended && <span className="ask-question-recommended-badge">Recommended</span>}
              </button>
            )
          })}
          {pendingQuestion.recommendation_reason && (
            <p className="ask-question-reason">{pendingQuestion.recommendation_reason}</p>
          )}
        </div>
      )}
      {needsCycleKickoff && (
        <p className="cycle-boundary-notice">Nothing&apos;s been said about this attempt yet — reply below to have the agent take a look.</p>
      )}
      <div className="chat-input">
        <textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleDraftKeyDown}
          placeholder={editingIndex !== null ? 'Editing message...' : 'Reply...'}
          rows={3}
          disabled={sending}
        />
        {editingIndex !== null ? (
          <div className="chat-input-edit-controls">
            <button type="button" className="action-btn-cancel" onClick={handleCancelEdit} disabled={sending}>
              Cancel
            </button>
            <button type="button" onClick={handleSend} disabled={sending || !draft.trim()}>
              {sending ? 'Saving...' : 'Save'}
            </button>
          </div>
        ) : sending ? (
          <button type="button" className="action-btn-stop" onClick={handleStop}>
            Stop
          </button>
        ) : (
          <button type="button" onClick={handleSend} disabled={!draft.trim()}>
            Send
          </button>
        )}
      </div>
      {sending ? (
        <p className="turn-status" aria-live="polite">
          <span className="turn-status-spinner" aria-hidden="true" />
          {liveTurnStatus.elapsedSeconds}s
          {liveTurnStatus.tokens > 0 && (
            <>
              {' '}&middot; {liveTurnStatus.isEstimate ? '~' : ''}
              {liveTurnStatus.tokens} tokens
            </>
          )}
        </p>
      ) : (
        <p className="chat-input-hint">Enter to send &middot; Alt+Enter for a new line</p>
      )}
      </>
      )}

      {pendingDraft && !readOnly && (
        <div className="draft-review">
          <h4>Proposed draft</h4>
          {renderDraft(pendingDraft, setPendingDraft)}
          {finalizeError && <p className="error">{finalizeError}</p>}
          {draftIsEditable && (
            <textarea
              value={requestChangesComment}
              onChange={(e) => setRequestChangesComment(e.target.value)}
              placeholder="What should change? (optional — edit the draft above first)"
              rows={2}
            />
          )}
          <div className="stage-actions">
            <button type="button" onClick={handleFinalize} disabled={finalizing}>
              {finalizing ? 'Finalizing...' : 'Finalize'}
            </button>
            <button type="button" onClick={handleRequestChanges} disabled={finalizing || sending}>
              Request changes
            </button>
            <button type="button" onClick={() => setPendingDraft(null)} disabled={finalizing}>
              Dismiss draft
            </button>
          </div>
        </div>
      )}

      {secondaryDraft && pendingSecondaryDraft && !readOnly && (
        <div className="draft-review secondary-draft-review">
          <h4>{secondaryDraft.heading}</h4>
          {secondaryDraft.renderDraft(pendingSecondaryDraft, setPendingSecondaryDraft)}
          {secondaryError && <p className="error">{secondaryError}</p>}
          <div className="stage-actions">
            <button type="button" onClick={() => handleSecondaryDecision(secondaryDraft.onAccept)} disabled={secondaryFinalizing}>
              {secondaryFinalizing ? 'Saving...' : 'Accept'}
            </button>
            <button type="button" onClick={() => handleSecondaryDecision(secondaryDraft.onReject)} disabled={secondaryFinalizing}>
              Reject
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
