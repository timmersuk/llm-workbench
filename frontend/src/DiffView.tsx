import { useMemo, useState } from 'react'
import { Diff, Hunk, isNormal, parseDiff, tokenize } from 'react-diff-view'
import type { FileData, HunkTokens, ViewType } from 'react-diff-view'
import refractor from 'refractor'
import go from 'refractor/lang/go'
import typescript from 'refractor/lang/typescript'
import tsx from 'refractor/lang/tsx'
import jsx from 'refractor/lang/jsx'
import json from 'refractor/lang/json'
import yaml from 'refractor/lang/yaml'
import markdown from 'refractor/lang/markdown'
import bash from 'refractor/lang/bash'
import 'react-diff-view/style/index.css'

// react-diff-view's `tokenize` only understands the array-of-hast-nodes
// shape refractor@3.x's `highlight()` returns — refractor@4+ wraps that
// array in a `{ type: 'root', children: [...] }` object instead, which
// react-diff-view's tree walker can't iterate (see react-diff-view's own
// README: "it isn't compatible with refractor@4.x currently, use
// refractor@3.x instead"). Hence the `^3.6.0` pin in package.json.
//
// The bare `refractor` import already registers markup/css/clike/javascript
// (refractor@3.x's own default bundle); every other language this workbench
// highlights is registered individually below. `registered` guards against
// double-registration across repeated module evaluation in tests.
for (const lang of [go, typescript, tsx, jsx, json, yaml, markdown, bash]) {
  if (!refractor.registered(lang.displayName)) {
    refractor.register(lang)
  }
}

// Files with more changed lines than this render collapsed by default — a
// 2000-line generated-file diff shouldn't push every other file in the same
// patch off screen. Smaller, more reviewable files stay expanded.
const COLLAPSE_LINE_THRESHOLD = 300

const EXTENSION_LANGUAGE: Record<string, string> = {
  go: 'go',
  ts: 'typescript',
  tsx: 'tsx',
  js: 'javascript',
  jsx: 'jsx',
  mjs: 'javascript',
  cjs: 'javascript',
  css: 'css',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  md: 'markdown',
  sh: 'bash',
  bash: 'bash',
}

function languageFor(path: string): string | undefined {
  const ext = path.split('.').pop()?.toLowerCase()
  return ext ? EXTENSION_LANGUAGE[ext] : undefined
}

// A file's displayable path — renames/deletes have no meaningful `newPath`
// (gitdiff-parser reports it as `/dev/null`), so fall back to `oldPath`.
function displayPath(file: FileData): string {
  return file.newPath !== '/dev/null' ? file.newPath : file.oldPath
}

// Counted as added + removed lines only — unchanged context lines a hunk
// carries for readability shouldn't count toward the collapse threshold.
function changedLineCount(file: FileData): number {
  return file.hunks.reduce((total, hunk) => total + hunk.changes.filter((change) => !isNormal(change)).length, 0)
}

function tokensFor(file: FileData, language: string | undefined): HunkTokens | undefined {
  if (!language) {
    return undefined
  }
  try {
    return tokenize(file.hunks, { highlight: true, refractor, language })
  } catch {
    // Malformed/unsupported source for this language shouldn't take down
    // the whole diff view — fall back to unhighlighted rendering.
    return undefined
  }
}

interface FileDiffProps {
  file: FileData
  viewType: ViewType
}

function FileDiff({ file, viewType }: FileDiffProps) {
  const path = displayPath(file)
  const lineCount = useMemo(() => changedLineCount(file), [file])
  const [open, setOpen] = useState(lineCount <= COLLAPSE_LINE_THRESHOLD)
  const tokens = useMemo(() => tokensFor(file, languageFor(path)), [file, path])

  return (
    <details className="diff-file" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        <span className="diff-file-path">{path}</span>
        <span className="diff-file-stat">{lineCount} changed line{lineCount === 1 ? '' : 's'}</span>
      </summary>
      <Diff viewType={viewType} diffType={file.type} hunks={file.hunks} tokens={tokens}>
        {(hunks) => hunks.map((hunk) => <Hunk key={hunk.content} hunk={hunk} />)}
      </Diff>
    </details>
  )
}

interface DiffViewProps {
  patch: string | null | undefined
}

// DiffView renders a unified-diff patch (as produced by
// agentrunner.CollectExecutionPatch) as one collapsible section per changed
// file, with a single view-wide unified/split toggle — the same reading
// experience as a PR review tool, in place of a raw `<pre>` dump of the
// patch text. A null/empty/unparseable patch renders nothing, so callers
// keep their existing "omit the diff section" fallback unchanged.
export function DiffView({ patch }: DiffViewProps) {
  const [viewType, setViewType] = useState<ViewType>('unified')

  const files = useMemo(() => {
    if (!patch) {
      return []
    }
    try {
      return parseDiff(patch)
    } catch {
      return []
    }
  }, [patch])

  if (files.length === 0) {
    return null
  }

  return (
    <div className="diff-view">
      <div className="diff-view-toggle" role="group" aria-label="Diff view mode">
        <button
          type="button"
          className={viewType === 'unified' ? 'active' : ''}
          aria-pressed={viewType === 'unified'}
          onClick={() => setViewType('unified')}
        >
          Unified
        </button>
        <button
          type="button"
          className={viewType === 'split' ? 'active' : ''}
          aria-pressed={viewType === 'split'}
          onClick={() => setViewType('split')}
        >
          Split
        </button>
      </div>
      {files.map((file) => (
        <FileDiff key={`${file.oldRevision}:${file.newRevision}:${displayPath(file)}`} file={file} viewType={viewType} />
      ))}
    </div>
  )
}
