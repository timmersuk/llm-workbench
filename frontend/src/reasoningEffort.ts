import type { ReasoningEffort } from './types'

// ALL_REASONING_EFFORTS is the fallback effort list a per-turn picker shows
// before real capability data has loaded (or when a test fixture's
// ModelsListResult doesn't carry `efforts` at all — see its doc comment in
// types.ts). Once listModels resolves a real executor's capability entry,
// callers should prefer its `efforts` (and re-derive it again on every
// executor change), the same way the model picker already prefers the
// resolved `models` over any static list.
export const ALL_REASONING_EFFORTS: ReasoningEffort[] = ['low', 'medium', 'high']

// resolveEffort picks the value a picker should actually show once a
// (possibly narrower) capability effort list is known: the current value if
// it's still supported, otherwise the capability's own declared default, or
// failing that the first supported effort — mirroring how the model picker
// resets `selectedModel` to `models[0]` on an executor change that
// invalidates the previous choice, so an effort selection never silently
// remains on a value the newly-selected executor doesn't advertise.
export function resolveEffort(current: ReasoningEffort, efforts: ReasoningEffort[], defaultEffort?: ReasoningEffort): ReasoningEffort {
  if (efforts.includes(current)) {
    return current
  }
  if (defaultEffort && efforts.includes(defaultEffort)) {
    return defaultEffort
  }
  return efforts[0] ?? current
}
