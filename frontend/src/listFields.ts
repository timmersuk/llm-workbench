// Shared helpers for editing string[] fields (constraints, assumptions,
// success criteria, steps, risks, ...) as a newline-separated textarea —
// used by TaskForm, RequirementsDraftForm, and PlanDraftForm.
export function linesToList(value: string): string[] {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

export function listToLines(items: string[]): string {
  return items.join('\n')
}
