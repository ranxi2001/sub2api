import { beforeEach, describe, expect, it, vi } from 'vitest'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../client', () => ({ apiClient: client }))
import { aiOpsAPI } from '../aiOps'

beforeEach(() => {
  client.get.mockReset().mockResolvedValue({ data: {} })
  client.post.mockReset().mockResolvedValue({ data: {} })
})

describe('AI operations API boundary', () => {
  it('creates only the fixed empty-body diagnostic request and forwards cancellation', async () => {
    const controller = new AbortController()
    await aiOpsAPI.create(controller.signal)
    expect(client.post).toHaveBeenCalledOnce()
    expect(client.post).toHaveBeenCalledWith('/admin/ops/ai/runs', {}, { signal: controller.signal })
    expect(client.get).not.toHaveBeenCalled()
  })

  it('encodes the selected ID as a path segment and supports aborting every read', async () => {
    const controller = new AbortController()
    await aiOpsAPI.status(controller.signal)
    await aiOpsAPI.list(controller.signal)
    await aiOpsAPI.get('run/with?characters', controller.signal)
    expect(client.get.mock.calls).toEqual([
      ['/admin/ops/ai/status', { signal: controller.signal }],
      ['/admin/ops/ai/runs', { signal: controller.signal }],
      ['/admin/ops/ai/runs/run%2Fwith%3Fcharacters', { signal: controller.signal }],
    ])
    expect(client.post).not.toHaveBeenCalled()
  })
})
