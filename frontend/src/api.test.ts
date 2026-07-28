import { describe, expect, it, vi } from 'vitest'
import {
  closeChatSession,
  createProject,
  createProjectTask,
  finalizePlan,
  finalizeRequirements,
  getHealthStatus,
  getProject,
  getProjectTask,
  getStageConversation,
  getTaskContext,
  getTaskPlan,
  listAgentExecutors,
  listModels,
  listProjectTasks,
  listProjects,
  postStageMessage,
  reviseRequirements,
  revisePlan,
  streamChatCompletion,
  updateProject,
  updateProjectTask,
} from './api'
import type {
  ChatStreamEvent,
  CreateProjectRequest,
  CreateTaskRequest,
  RequirementsDraft,
  TaskPlan,
  UpdateProjectRequest,
  UpdateTaskRequest,
} from './types'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function textResponse(body: string, status = 500): Response {
  return new Response(body, { status })
}

function sseResponse(chunks: string[], status = 200): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(new TextEncoder().encode(chunk))
      }
      controller.close()
    },
  })
  return new Response(body, { status })
}

function stubFetch(...responses: Response[]) {
  const fn = vi.fn()
  for (const res of responses) {
    fn.mockResolvedValueOnce(res)
  }
  vi.stubGlobal('fetch', fn)
  return fn
}

const emptyTaskRefs = { knowledge: [], repo: [] }

describe('getJSON-backed requests', () => {
  it('listProjects returns the parsed body on success', async () => {
    const result = { projects: [{ id: 'demo' }], errors: [] }
    const fetchMock = stubFetch(jsonResponse(result))
    await expect(listProjects()).resolves.toEqual(result)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects', { signal: expect.any(AbortSignal) })
  })

  it('listProjects throws the exact "<path> returned <status>" message on non-2xx', async () => {
    stubFetch(jsonResponse({}, 500))
    await expect(listProjects()).rejects.toThrow('/api/v1/projects returned 500')
  })

  it('getProjectTask encodeURIComponent-escapes both path segments', async () => {
    const fetchMock = stubFetch(jsonResponse({ id: 'a b' }))
    await getProjectTask('demo project', 'task one')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo%20project/tasks/task%20one', { signal: expect.any(AbortSignal) })
  })

  it('getProject hits the right path and returns the parsed body', async () => {
    const body = { id: 'demo' }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(getProject('demo')).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo', { signal: expect.any(AbortSignal) })
  })

  it('getProject encodeURIComponent-escapes the id', async () => {
    const fetchMock = stubFetch(jsonResponse({ id: 'a b' }))
    await getProject('demo project')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo%20project', { signal: expect.any(AbortSignal) })
  })

  it('getProject throws the exact "<path> returned <status>" message on non-2xx', async () => {
    stubFetch(jsonResponse({}, 404))
    await expect(getProject('missing')).rejects.toThrow('/api/v1/projects/missing returned 404')
  })

  it('listProjectTasks hits the right path and returns the parsed body', async () => {
    const body = { tasks: [], errors: [] }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(listProjectTasks('demo')).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo/tasks', { signal: expect.any(AbortSignal) })
  })

  it('getTaskContext hits the right path and returns the parsed body', async () => {
    const body = { summary: 'x', background: '', files: [], detail: '', verification: [], open_questions: [] }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(getTaskContext('demo', 'task-a')).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo/tasks/task-a/context', { signal: expect.any(AbortSignal) })
  })

  it('getTaskPlan hits the right path and returns the parsed body', async () => {
    const body = { approach: 'x', steps: [], risks: [], estimated_complexity: 'low', recommended_executor: '' }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(getTaskPlan('demo', 'task-a')).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo/tasks/task-a/plan', { signal: expect.any(AbortSignal) })
  })

  it('getStageConversation hits the right path and returns the parsed body', async () => {
    const body = { stage: 'requirements', messages: [] }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(getStageConversation('demo', 'task-a', 'requirements')).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects/demo/tasks/task-a/stages/requirements/conversation', {
      signal: expect.any(AbortSignal),
    })
  })

  it('listModels hits the right path and returns the parsed body', async () => {
    const body = { models: ['model-a'] }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(listModels()).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/chat/models', { signal: expect.any(AbortSignal) })
  })

  it('listAgentExecutors hits the right path and returns the parsed body', async () => {
    const body = { executors: ['claude-code'] }
    const fetchMock = stubFetch(jsonResponse(body))
    await expect(listAgentExecutors()).resolves.toEqual(body)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/agent-executors', { signal: expect.any(AbortSignal) })
  })

  it('listAgentExecutors surfaces a friendly message when the request times out', async () => {
    const timeoutSignal = AbortSignal.abort(new DOMException('signal timed out', 'TimeoutError'))
    vi.spyOn(AbortSignal, 'timeout').mockReturnValue(timeoutSignal)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((_path: string, init?: { signal?: AbortSignal }) => Promise.reject(init?.signal?.reason)),
    )
    await expect(listAgentExecutors()).rejects.toThrow('/api/v1/agent-executors timed out after 15s')
  })
})

describe('mutateJSON-backed requests', () => {
  it('createProject sends POST with JSON content-type and body, returns the parsed body', async () => {
    const req: CreateProjectRequest = { name: 'Demo', description: '', repositories: [], knowledge: [], constraints: [] }
    const created = { id: 'demo', ...req, created_at: '', updated_at: '' }
    const fetchMock = stubFetch(jsonResponse(created, 201))
    await expect(createProject(req)).resolves.toEqual(created)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
  })

  const updateReq: UpdateTaskRequest = {
    title: 'Updated',
    objective: '',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    references: emptyTaskRefs,
  }

  it('updateProjectTask throws the response body text as the error message when non-empty', async () => {
    stubFetch(textResponse('task id mismatch', 400))
    await expect(updateProjectTask('demo', 'task-a', updateReq)).rejects.toThrow('task id mismatch')
  })

  it('updateProjectTask falls back to the generic "<method> <path> returned <status>" message when the body text is empty', async () => {
    stubFetch(textResponse('', 500))
    await expect(updateProjectTask('demo', 'task-a', updateReq)).rejects.toThrow(
      'PUT /api/v1/projects/demo/tasks/task-a returned 500',
    )
  })

  it('updateProjectTask falls back to the generic message when reading the body text itself rejects', async () => {
    const brokenResponse = {
      ok: false,
      status: 500,
      text: () => Promise.reject(new Error('body already consumed')),
    } as unknown as Response
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(brokenResponse))
    await expect(updateProjectTask('demo', 'task-a', updateReq)).rejects.toThrow(
      'PUT /api/v1/projects/demo/tasks/task-a returned 500',
    )
  })

  it('createProjectTask hits the right method/path', async () => {
    const req: CreateTaskRequest = { id: 'a', title: 'A', references: emptyTaskRefs }
    const fetchMock = stubFetch(jsonResponse({}))
    await createProjectTask('demo', req)
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/projects/demo/tasks')
    expect(init).toMatchObject({ method: 'POST' })
  })

  it('updateProject hits the right method/path', async () => {
    const req: UpdateProjectRequest = { name: 'Demo', description: '', repositories: [], knowledge: [], constraints: [] }
    const fetchMock = stubFetch(jsonResponse({}))
    await updateProject('demo', req)
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/projects/demo')
    expect(init).toMatchObject({ method: 'PUT' })
  })

  const draft: RequirementsDraft = {
    objective: 'ship it',
    constraints: [],
    assumptions: [],
    success_criteria: [],
    context: { summary: '', background: '', files: [], detail: '', verification: [], open_questions: [] },
  }

  it('finalizeRequirements hits the right method/path', async () => {
    const fetchMock = stubFetch(jsonResponse({}))
    await finalizeRequirements('demo', 'task-a', draft)
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/projects/demo/tasks/task-a/requirements/finalize')
    expect(init).toMatchObject({ method: 'POST' })
  })

  const plan: TaskPlan = { approach: '', steps: [], risks: [], estimated_complexity: '', recommended_executor: '' }

  it('finalizePlan hits the right method/path', async () => {
    const fetchMock = stubFetch(jsonResponse({}))
    await finalizePlan('demo', 'task-a', plan)
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/projects/demo/tasks/task-a/plan/finalize')
    expect(init).toMatchObject({ method: 'POST' })
  })

  it('reviseRequirements POSTs an empty JSON body to the requirements/revise path', async () => {
    const fetchMock = stubFetch(jsonResponse({}))
    await reviseRequirements('demo', 'task-a')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/projects/demo/tasks/task-a/requirements/revise',
      expect.objectContaining({ method: 'POST', body: '{}' }),
    )
  })

  it('revisePlan POSTs an empty JSON body to the plan/revise path', async () => {
    const fetchMock = stubFetch(jsonResponse({}))
    await revisePlan('demo', 'task-a')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/projects/demo/tasks/task-a/plan/revise',
      expect.objectContaining({ method: 'POST', body: '{}' }),
    )
  })
})

describe('getHealthStatus', () => {
  it('returns the parsed status on 200', async () => {
    stubFetch(jsonResponse({ status: 'ok', build_id: 'abc123' }))
    await expect(getHealthStatus()).resolves.toEqual({ status: 'ok', build_id: 'abc123' })
  })

  it('throws the error field from a parseable non-ok JSON body', async () => {
    stubFetch(jsonResponse({ status: 'error', build_id: '', error: 'boom' }, 503))
    await expect(getHealthStatus()).rejects.toThrow('boom')
  })

  it('falls back to a status-based message when the non-ok body is not valid JSON', async () => {
    stubFetch(textResponse('not json', 503))
    await expect(getHealthStatus()).rejects.toThrow('healthcheck failed with status 503')
  })
})

describe('streaming (via streamChatCompletion)', () => {
  it('delivers multiple SSE events in order', async () => {
    stubFetch(sseResponse(['data: {"content":"Hello"}\n\n', 'data: {"content":" world"}\n\n']))
    const events: ChatStreamEvent[] = []
    await streamChatCompletion('hi', 'model', 'local', 'sess-1', (e) => events.push(e))
    expect(events).toEqual([{ content: 'Hello' }, { content: ' world' }])
  })

  it('accumulates a data: line split across chunks', async () => {
    stubFetch(sseResponse(['data: {"conte', 'nt":"hi"}\n\n']))
    const events: ChatStreamEvent[] = []
    await streamChatCompletion('hi', 'model', 'local', 'sess-1', (e) => events.push(e))
    expect(events).toEqual([{ content: 'hi' }])
  })

  it('rejects without calling onEvent when the response is non-ok', async () => {
    stubFetch(sseResponse(['data: {"content":"hi"}\n\n'], 500))
    const onEvent = vi.fn()
    await expect(streamChatCompletion('hi', 'model', 'local', 'sess-1', onEvent)).rejects.toThrow('request failed with status 500')
    expect(onEvent).not.toHaveBeenCalled()
  })

  it('rejects when the response is ok but has no body', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response(null, { status: 200 })))
    const onEvent = vi.fn()
    await expect(streamChatCompletion('hi', 'model', 'local', 'sess-1', onEvent)).rejects.toThrow('request failed with status 200')
    expect(onEvent).not.toHaveBeenCalled()
  })

  it('silently drops a trailing partial line with no terminating newline', async () => {
    stubFetch(sseResponse(['data: {"content":"first"}\n\ndata: {"content":"incomplete"']))
    const events: ChatStreamEvent[] = []
    await streamChatCompletion('hi', 'model', 'local', 'sess-1', (e) => events.push(e))
    expect(events).toEqual([{ content: 'first' }])
  })

  it('skips non-"data: " lines', async () => {
    stubFetch(sseResponse([': keep-alive\n\ndata: {"content":"real"}\n\n']))
    const events: ChatStreamEvent[] = []
    await streamChatCompletion('hi', 'model', 'local', 'sess-1', (e) => events.push(e))
    expect(events).toEqual([{ content: 'real' }])
  })
})

describe('streamChatCompletion / postStageMessage request shape', () => {
  it('streamChatCompletion POSTs content+model+executor+session_key to /api/v1/chat/completions', async () => {
    const fetchMock = stubFetch(sseResponse(['data: {"content":"hi"}\n\n']))
    await streamChatCompletion('hello', 'my-model', 'local', 'sess-1', vi.fn())
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: 'hello', model: 'my-model', executor: 'local', session_key: 'sess-1' }),
    })
  })

  it('closeChatSession POSTs session_key to /api/v1/chat/sessions/close', async () => {
    const fetchMock = stubFetch(new Response(null, { status: 204 }))
    await closeChatSession('sess-1')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/chat/sessions/close', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_key: 'sess-1' }),
    })
  })

  it('postStageMessage POSTs content+model+executor to the stage-scoped, escaped messages endpoint', async () => {
    const fetchMock = stubFetch(sseResponse(['data: {"content":"hi"}\n\n']))
    await postStageMessage('demo project', 'task one', 'requirements', 'hello', 'my-model', 'claude-code', vi.fn())
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/projects/demo%20project/tasks/task%20one/stages/requirements/messages',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content: 'hello', model: 'my-model', executor: 'claude-code' }),
      },
    )
  })
})
