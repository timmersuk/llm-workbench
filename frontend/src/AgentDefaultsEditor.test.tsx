import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AgentDefaultsEditor } from './AgentDefaultsEditor'
import * as api from './api'
import type { AgentDefaults, Task } from './types'

vi.mock('./api')

const projectId = 'demo'

const validDefaults: AgentDefaults = {
  stage_conversation: { executor: 'local', model: 'test-model', effort: 'medium' },
  execution: { executor: 'claude-code', model: 'sonnet', effort: 'high' },
}

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-a',
    title: 'Task A',
    project: projectId,
    stage: 'planning',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    objective: 'ship it',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: { knowledge: [], repo: [] },
    agent_defaults: validDefaults,
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listExecutorCapabilities).mockResolvedValue({
    executors: [
      { name: 'local', models: ['test-model', 'other-model'], efforts: ['low', 'medium', 'high'], default_model: 'test-model', default_effort: 'medium' },
      { name: 'claude-code', models: ['sonnet', 'opus', 'haiku'], efforts: ['low', 'medium', 'high'], default_model: 'sonnet', default_effort: 'high' },
    ],
  })
})

describe('AgentDefaultsEditor', () => {
  it('shows a clear message and no editor when the task has no agent_defaults', async () => {
    render(<AgentDefaultsEditor projectId={projectId} task={makeTask({ agent_defaults: undefined })} onSaved={vi.fn()} />)

    expect(await screen.findByText(/This task has no agent defaults/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save defaults' })).not.toBeInTheDocument()
  })

  it('renders both default groups pre-populated from the task, with the executor list drawn from capabilities', async () => {
    render(<AgentDefaultsEditor projectId={projectId} task={makeTask()} onSaved={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Stage conversations executor')).toHaveValue('local'))
    expect(screen.getByLabelText('Stage conversations model')).toHaveValue('test-model')
    expect(screen.getByLabelText('Stage conversations effort')).toHaveValue('medium')
    expect(screen.getByLabelText('Execute runs executor')).toHaveValue('claude-code')
    expect(screen.getByLabelText('Execute runs model')).toHaveValue('sonnet')
    expect(screen.getByLabelText('Execute runs effort')).toHaveValue('high')
    expect(within(screen.getByLabelText('Stage conversations executor')).getByRole('option', { name: 'claude-code' })).toBeInTheDocument()
  })

  it('enables Save once capabilities have loaded for a fully valid task, and round-trips through the whole-task PUT', async () => {
    const user = userEvent.setup()
    const task = makeTask()
    const onSaved = vi.fn()
    const saved = { ...task, title: 'Task A' }
    vi.mocked(api.updateProjectTask).mockResolvedValue(saved)

    render(<AgentDefaultsEditor projectId={projectId} task={task} onSaved={onSaved} />)

    const save = await screen.findByRole('button', { name: 'Save defaults' })
    await waitFor(() => expect(save).not.toBeDisabled())

    await user.click(save)

    await waitFor(() =>
      expect(api.updateProjectTask).toHaveBeenCalledWith(projectId, task.id, {
        title: task.title,
        objective: task.objective,
        constraints: task.constraints,
        assumptions: task.assumptions,
        success_criteria: task.success_criteria,
        references: task.references,
        agent_defaults: validDefaults,
      }),
    )
    expect(onSaved).toHaveBeenCalledWith(saved)
  })

  it('shows a stale-default error and disables Save when a persisted executor is no longer advertised, without rewriting it', async () => {
    const stale: AgentDefaults = {
      stage_conversation: { executor: 'retired-executor', model: 'old-model', effort: 'high' },
      execution: { executor: 'claude-code', model: 'sonnet', effort: 'high' },
    }
    render(<AgentDefaultsEditor projectId={projectId} task={makeTask({ agent_defaults: stale })} onSaved={vi.fn()} />)

    // The stale executor is preserved and visibly shown (not silently
    // swapped for a supported one) with an "(unavailable)" marker.
    await waitFor(() => expect(screen.getByLabelText('Stage conversations executor')).toHaveValue('retired-executor'))
    expect(screen.getByRole('option', { name: 'retired-executor (unavailable)' })).toBeInTheDocument()
    expect(screen.getAllByText('This persisted default is no longer supported.')).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Save defaults' })).toBeDisabled()
    expect(api.updateProjectTask).not.toHaveBeenCalled()
  })

  it('shows a stale-default error when the persisted model is not among the executor\'s advertised models', async () => {
    const stale: AgentDefaults = {
      stage_conversation: { executor: 'local', model: 'retired-model', effort: 'medium' },
      execution: { executor: 'claude-code', model: 'sonnet', effort: 'high' },
    }
    render(<AgentDefaultsEditor projectId={projectId} task={makeTask({ agent_defaults: stale })} onSaved={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Stage conversations model')).toHaveValue('retired-model'))
    expect(screen.getByRole('option', { name: 'retired-model (unsupported)' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save defaults' })).toBeDisabled()
  })

  it('selecting a different executor resets that group to the new executor\'s default model and effort', async () => {
    const user = userEvent.setup()
    render(<AgentDefaultsEditor projectId={projectId} task={makeTask()} onSaved={vi.fn()} />)

    await waitFor(() => expect(screen.getByLabelText('Stage conversations executor')).toHaveValue('local'))
    await user.selectOptions(screen.getByLabelText('Stage conversations executor'), 'claude-code')

    expect(screen.getByLabelText('Stage conversations executor')).toHaveValue('claude-code')
    expect(screen.getByLabelText('Stage conversations model')).toHaveValue('sonnet')
    expect(screen.getByLabelText('Stage conversations effort')).toHaveValue('high')
    // The independent execution group is untouched by editing the
    // stage-conversation group.
    expect(screen.getByLabelText('Execute runs executor')).toHaveValue('claude-code')
  })

  it('surfaces an inline error and keeps the form editable when the save request rejects', async () => {
    const user = userEvent.setup()
    vi.mocked(api.updateProjectTask).mockRejectedValue(new Error('conflict'))
    const onSaved = vi.fn()

    render(<AgentDefaultsEditor projectId={projectId} task={makeTask()} onSaved={onSaved} />)

    const save = await screen.findByRole('button', { name: 'Save defaults' })
    await waitFor(() => expect(save).not.toBeDisabled())
    await user.click(save)

    expect(await screen.findByText('Error: conflict')).toBeInTheDocument()
    expect(onSaved).not.toHaveBeenCalled()
  })
})
