# Defer OS-level Bash sandboxing for the execution harness; confine to the worktree cwd only

Milestone 8 gives `ChatClientRunner.Execute` a Bash tool alongside
Read/Grep/Glob/Write/Edit, so the `local` executor can run tests/builds/
lints the way `ClaudeRunner`/`CodexRunner` already do via their own CLI's
tools. We considered adding OS-level sandboxing for that Bash tool —
Linux's Landlock (via `landlock-lsm/go-landlock`, or shelling out through
`Zouuup/landrun`) for real kernel-level path allow-listing, or Windows's
Sandboxie for a Windows-first deployment — and decided not to add either
for v1. Bash execution is confined only to the execution worktree's
current working directory, the same trust level `ClaudeRunner` and
`CodexRunner` already operate at today: both already run arbitrary bash
inside the worktree via their own CLI's tools with no deeper OS-level
confinement.

Sandboxie in particular turned out not to be the "quick win" it first
looked like: its default isolation model redirects writes into a separate
sandbox folder for later, optional recovery — built for "run this
untrusted installer, then decide whether to keep its changes" workflows,
not "let this agent write freely inside the worktree it's supposed to
edit." Getting directory-allowlist behavior out of it requires
configuring explicit "Open Path" rules, and it's Windows-only regardless,
so it wouldn't give parity across whatever other platforms this
workbench's planned system-tray/background-service packaging eventually
targets. Landlock is the more architecturally honest fit (a real path
allowlist, enforced by the kernel) but is Linux-only (5.13+), which
doesn't help a Windows-first deployment today.

Given the `local` executor's whole purpose is reaching parity with
executors that already operate at this trust level, gating Milestone 8 on
solving a sandboxing problem neither `ClaudeRunner` nor `CodexRunner` has
solved either would hold up the milestone for a hardening step that isn't
actually a regression relative to what's already shipped.

This is a deferral, not a rejection — two concrete candidates are on
record for whoever revisits it: Landlock for a Linux deployment target,
Sandboxie (with explicit Open Path configuration) for a Windows one.
Revisit if either an actual sandbox-escape incident occurs, or if this
workbench moves toward running less-trusted models or tasks than it does
today.
