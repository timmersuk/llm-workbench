import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NewTaskPanel } from './NewTaskPanel'
import * as api from './api'
import type { ChatStreamEvent, Task } from './types'

vi.mock('./api')

const projectId = 'demo'
const sessionId = 'session-1'

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 'fix-login-bug',
    title: 'Fix login bug',
    project: projectId,
    stage: 'requirements',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    objective: 'ship a fix',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: { knowledge: [], repo: [] },
    draft_session_id: sessionId,
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listModels).mockResolvedValue({ models: ['model-a'] })
  vi.mocked(api.listAgentExecutors).mockResolvedValue({ executors: ['local'] })
  vi.mocked(api.getTaskDraftConversation).mockResolvedValue({ stage: '', messages: [] })
  vi.mocked(api.startTaskDraftConversation).mockResolvedValue()
})

describe('NewTaskPanel — initial load', () => {
  it('labels the panel "New Task" with an explanatory intro', async () => {
    render(<NewTaskPanel projectId={projectId} sessionId={sessionId} onCreated={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'New Task' })).toBeInTheDocument()
    expect(screen.getByText(/Describe what you want done/)).toBeInTheDocument()
  })

  it('does not auto-start — it waits for an explicit Start, then calls startTaskDraftConversation', async () => {
    const user = userEvent.setup()
    render(<NewTaskPanel projectId={projectId} sessionId={sessionId} onCreated={vi.fn()} />)

    const start = await screen.findByRole('button', { name: 'Start' })
    expect(api.startTaskDraftConversation).not.toHaveBeenCalled()

    await user.click(start)
    expect(api.startTaskDraftConversation).toHaveBeenCalledWith(
      projectId,
      sessionId,
      expect.anything(),
      expect.anything(),
      expect.anything(),
      expect.anything(),
    )
  })

  it('renders a populated transcript from getTaskDraftConversation', async () => {
    vi.mocked(api.getTaskDraftConversation).mockResolvedValue({
      stage: '',
      messages: [
        { role: 'user', content: 'I want a login page', created_at: '2026-01-01T00:00:00Z' },
        { role: 'assistant', content: 'What should happen when a login fails?', created_at: '2026-01-01T00:00:01Z' },
      ],
    })

    render(<NewTaskPanel projectId={projectId} sessionId={sessionId} onCreated={vi.fn()} />)

    expect(await screen.findByText('I want a login page')).toBeInTheDocument()
    expect(screen.getByText('What should happen when a login fails?')).toBeInTheDocument()
  })
})

describe('NewTaskPanel — propose_task draft and confirmation', () => {
  async function sendAndReceiveToolCall(argsObject: Record<string, unknown>) {
    const user = userEvent.setup()
    let deliver!: (event: ChatStreamEvent) => void
    vi.mocked(api.postTaskDraftMessage).mockImplementation((_p, _s, _c, _m, _e, onEvent) => {
      deliver = onEvent
      return Promise.resolve()
    })

    render(<NewTaskPanel projectId={projectId} sessionId={sessionId} onCreated={vi.fn()} />)
    await user.click(await screen.findByRole('button', { name: 'Start' }))

    await user.type(screen.getByPlaceholderText('Reply...'), 'That is everything')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    act(() => deliver({ tool_call: { name: 'propose_task', arguments: JSON.stringify(argsObject) } }))
    return user
  }

  it('surfaces the draft review UI populated from the tool_call arguments', async () => {
    await sendAndReceiveToolCall({
      id: 'fix-login-bug',
      title: 'Fix login bug',
      objective: 'ship a fix',
      constraints: [],
      assumptions: [],
      success_criteria: [],
      references: { knowledge: [], repo: [] },
    })

    expect(screen.getByText('Proposed draft')).toBeInTheDocument()
    expect(screen.getByLabelText('ID')).toHaveValue('fix-login-bug')
    expect(screen.getByLabelText('Title')).toHaveValue('Fix login bug')
    expect(screen.getByLabelText('Objective')).toHaveValue('ship a fix')
  })

  it('Finalize calls createProjectTask with draft_session_id and hands the created task to onCreated', async () => {
    const onCreated = vi.fn()
    const createdTask = makeTask()
    vi.mocked(api.createProjectTask).mockResolvedValue(createdTask)

    const user = await sendAndReceiveToolCall({
      id: 'fix-login-bug',
      title: 'Fix login bug',
      objective: 'ship a fix',
      constraints: ['no new deps'],
      assumptions: [],
      success_criteria: ['login succeeds with valid credentials'],
      references: { knowledge: [], repo: [] },
    })

    await user.click(screen.getByRole('button', { name: 'Finalize' }))

    await vi.waitFor(() =>
      expect(api.createProjectTask).toHaveBeenCalledWith(projectId, {
        id: 'fix-login-bug',
        title: 'Fix login bug',
        references: { knowledge: [], repo: [] },
        objective: 'ship a fix',
        constraints: ['no new deps'],
        assumptions: [],
        success_criteria: ['login succeeds with valid credentials'],
        draft_session_id: sessionId,
      }),
    )
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalledWith(createdTask))
  })
})
