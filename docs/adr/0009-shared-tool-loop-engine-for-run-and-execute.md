# Build one shared tool-call-loop engine for ChatClientRunner.Run and .Execute, not two

Milestone 8 needs `ChatClientRunner.Execute` to gain a real tool-execution
loop (send message → run any tool calls → feed results back → repeat) so
the `local` executor can do actual Implementation-stage work, closing the
`ErrExecuteNotSupported` stub. A second, narrower need already existed as
a drafted task, `chatclient-tool-loop`: give `ChatClientRunner.Run` a
*read-only* Read/Grep/Glob loop so stage conversations (GrillMe/Planning
Mode) can ground interview questions in the reference repository. We
chose to build one generic loop-driving engine — parameterized by
toolset, workspace, and stop condition — and instantiate it twice, rather
than building Execute's loop first and treating Run's read-only loop as a
smaller, separate implementation later.

The two instantiations differ in every dimension except the loop
mechanics themselves: Run offers Read/Grep/Glob only, against the shared
checkout (`ResolveWorkspace`), bounded to roughly `claudeRunnerMaxTurns`
(30), stopping on text or a Draft-tool-call; Execute offers the full
toolset (`agentrunner.executionTools`: Read/Grep/Glob/Write/Edit/Bash),
against an isolated execution worktree (`ResolveExecutionWorkspace`),
bounded to roughly `claudeExecutionMaxTurns` (100), stopping only once the
model finishes autonomously. Building a single engine parameterized this
way means Run's read-only trust boundary (no Write/Edit/Bash — the same
framing `readOnlyTools`'s doc comment already establishes for
`ClaudeRunner`) is enforced by construction — the toolset passed in — not
by a second, independently-maintained implementation that could drift out
of sync with Execute's.

This is also what absorbs the standalone `chatclient-tool-loop` task into
Milestone 8: rather than closing it as a separate piece of work, it
becomes the read-only instantiation of the same engine Execute uses, so
shipping Execute's loop and Run's loop are two configurations of one
deliverable, not two features built and maintained independently.

The tradeoff: a generic engine is more upfront design work than
hand-building Execute's loop alone and revisiting Run's separately later.
We accepted this because the two loops are close enough in shape — both
are fundamentally an OpenAI-compatible tool-calling round-trip, bounded,
against a scoped workspace — that building them twice would very likely
converge on needing this same abstraction after the fact anyway, at
higher cost once both exist as independent, harder-to-reconcile
implementations.
