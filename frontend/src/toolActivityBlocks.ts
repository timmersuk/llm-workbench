import type { ToolActivityEntry } from './ToolActivity'

// toolActivityBlocks.ts is the one shared implementation of "coalesce a
// stream of narration/tool-call events into blocks, attach a result to its
// call" — previously three independent copies (StageConversationPanel.tsx's
// DisplaySegment/appendTextSegment/appendToolCallSegment/appendToolResultSegment,
// ExecutePanel.tsx's TraceBlock/appendText/appendToolCall/appendToolResult,
// and ExecutionHistoryList.tsx's logEventsToTraceBlocks), each built against
// a different wire event shape (ChatStreamEvent.tool_activity,
// ExecuteStreamEvent, ExecutionLogEvent) but implementing the identical
// algorithm. That duplication is exactly how a position-based pairing bug
// could exist correctly-by-luck in one copy and not another — see the
// correlation-by-id fix this module is part of. Every caller adapts its own
// event shape into calls to these three functions instead of hand-rolling
// its own copy.

export type ToolActivityBlock = { kind: 'text'; text: string } | { kind: 'tools'; activities: ToolActivityEntry[] }

// appendTextBlock coalesces consecutive text deltas into one growing block
// rather than one block per delta. Any trailing 'tools' block is left as-is
// — already closed, since its sequence ended the moment this text started.
export function appendTextBlock(blocks: ToolActivityBlock[], text: string): ToolActivityBlock[] {
  const last = blocks[blocks.length - 1]
  if (last && last.kind === 'text') {
    return [...blocks.slice(0, -1), { ...last, text: last.text + text }]
  }
  return [...blocks, { kind: 'text', text }]
}

// appendToolCallBlock starts a new pending activity — continuing the
// trailing 'tools' block (same sequence) if there is one, or opening a new
// one.
export function appendToolCallBlock(blocks: ToolActivityBlock[], activity: ToolActivityEntry): ToolActivityBlock[] {
  const last = blocks[blocks.length - 1]
  if (last && last.kind === 'tools') {
    return [...blocks.slice(0, -1), { ...last, activities: [...last.activities, activity] }]
  }
  return [...blocks, { kind: 'tools', activities: [activity] }]
}

// appendToolResultBlock attaches a result to the activity with matching id
// — searching every 'tools' block, not assuming the trailing block's last
// activity is the one this result belongs to. A provider can declare
// several calls before returning any of their results (the claude CLI
// does, for parallel read-only tool calls), so a result can arrive for any
// still-pending call, not just the most recently declared one; matching by
// id is what makes that safe regardless of arrival order.
//
// A falsy id (an execution log recorded before this fix existed — see
// ExecutionLogEvent.id's doc comment, frontend/src/types.ts) falls back to
// the old "attach to the trailing tools block's last still-pending
// activity" heuristic, reproducing exactly today's rendering for that
// legacy data: real order isn't recoverable retroactively, but a
// no-longer-recognized id must not just leave every old call stuck
// pending forever either. Every call/result pair recorded going forward
// always carries a real id, so this fallback only ever applies to
// already-persisted, pre-fix data.
export function appendToolResultBlock(
  blocks: ToolActivityBlock[],
  id: string,
  result: string,
  isError: boolean | undefined,
): ToolActivityBlock[] {
  if (!id) {
    const last = blocks[blocks.length - 1]
    if (!last || last.kind !== 'tools' || last.activities.length === 0) {
      return blocks
    }
    const activities = [...last.activities]
    activities[activities.length - 1] = { ...activities[activities.length - 1], result, isError }
    return [...blocks.slice(0, -1), { ...last, activities }]
  }
  return blocks.map((block) =>
    block.kind !== 'tools'
      ? block
      : { ...block, activities: block.activities.map((a) => (a.id === id ? { ...a, result, isError } : a)) },
  )
}
