import { useEffect, useState } from 'react'
import { listExecutorCapabilities, updateProjectTask } from './api'
import type { AgentDefaults, AgentSelection, ExecutorCapability, Task } from './types'

interface Props { projectId: string; task: Task; onSaved: (task: Task) => void }

function SelectionEditor({ label, value, capabilities, onChange }: { label: string; value: AgentSelection; capabilities: ExecutorCapability[]; onChange: (value: AgentSelection) => void }) {
  const capability = capabilities.find((entry) => entry.name === value.executor)
  const valid = capability && (capability.models.length === 0 ? value.model === '' : capability.models.includes(value.model)) && capability.efforts.includes(value.effort)
  return <fieldset className="agent-default-group">
    <legend>{label}</legend>
    <select aria-label={`${label} executor`} value={value.executor} onChange={(e) => {
      const next = capabilities.find((entry) => entry.name === e.target.value)
      if (next) onChange({ executor: next.name, model: next.default_model, effort: next.default_effort })
    }}>
      {!capability && <option value={value.executor}>{value.executor} (unavailable)</option>}
      {capabilities.map((entry) => <option key={entry.name} value={entry.name}>{entry.name}</option>)}
    </select>
    <select aria-label={`${label} model`} value={value.model} onChange={(e) => onChange({ ...value, model: e.target.value })}>
      {!capability?.models.includes(value.model) && <option value={value.model}>{value.model} (unsupported)</option>}
      {capability?.models.map((model) => <option key={model}>{model}</option>)}
    </select>
    <select aria-label={`${label} effort`} value={value.effort} onChange={(e) => onChange({ ...value, effort: e.target.value as AgentSelection['effort'] })}>
      {!capability?.efforts.includes(value.effort) && <option value={value.effort}>{value.effort} (unsupported)</option>}
      {capability?.efforts.map((effort) => <option key={effort}>{effort}</option>)}
    </select>
    {!valid && <span className="error">This persisted default is no longer supported.</span>}
  </fieldset>
}

export function AgentDefaultsEditor({ projectId, task, onSaved }: Props) {
  const [capabilities, setCapabilities] = useState<ExecutorCapability[]>([])
  const [value, setValue] = useState<AgentDefaults | undefined>(task.agent_defaults)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => { listExecutorCapabilities().then((r) => setCapabilities(r.executors)).catch((e) => setError(String(e))) }, [])
  useEffect(() => setValue(task.agent_defaults), [task])
  if (!value) return <section><h3>Defaults</h3><p className="error">This task has no agent defaults. Run the explicit migration or provide per-turn overrides.</p></section>
  const valid = [value.stage_conversation, value.execution].every((selection) => {
    const c = capabilities.find((entry) => entry.name === selection.executor)
    return c && (c.models.length === 0 ? selection.model === '' : c.models.includes(selection.model)) && c.efforts.includes(selection.effort)
  })
  return <section><h3>Defaults</h3>
    <SelectionEditor label="Stage conversations" value={value.stage_conversation} capabilities={capabilities} onChange={(stage_conversation) => setValue({ ...value, stage_conversation })} />
    <SelectionEditor label="Execute runs" value={value.execution} capabilities={capabilities} onChange={(execution) => setValue({ ...value, execution })} />
    <button disabled={!valid} onClick={() => updateProjectTask(projectId, task.id, { title: task.title, objective: task.objective, constraints: task.constraints, assumptions: task.assumptions, success_criteria: task.success_criteria, references: task.references, agent_defaults: value }).then(onSaved).catch((e) => setError(String(e)))}>Save defaults</button>
    {error && <p className="error">{error}</p>}
  </section>
}
