import { beforeEach, describe, expect, it, vi } from 'vitest'
const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('../../client', () => ({ apiClient: client }))
import { aiOpsControlAPI, type AIOpsPolicy } from '../aiOpsControl'

beforeEach(() => { for (const fn of Object.values(client)) fn.mockReset().mockResolvedValue({ data: {} }) })

describe('AI operations control API boundary', () => {
  it('previews by immutable run and index, then applies only its ID and digest', async () => {
    const signal = new AbortController().signal
    await aiOpsControlAPI.preview('run-a', 2, signal)
    await aiOpsControlAPI.apply('proposal/with?segments', 'digest', signal)
    expect(client.post.mock.calls).toEqual([
      ['/admin/ops/ai/proposals/preview', { run_id: 'run-a', proposal_index: 2 }, { signal }],
      ['/admin/ops/ai/proposals/proposal%2Fwith%3Fsegments/apply', { digest: 'digest', confirm: true }, { signal }],
    ])
  })
  it('cancels only an exact frozen proposal without submitting an action', async () => {
    const signal = new AbortController().signal
    await aiOpsControlAPI.cancel('proposal/1', 'frozen-digest', signal)
    expect(client.post.mock.calls).toEqual([
      ['/admin/ops/ai/proposals/proposal%2F1/cancel', { digest: 'frozen-digest' }, { signal }],
    ])
  })
  it('binds rollback to ownership revision and keeps halt body empty', async () => {
    const signal = new AbortController().signal
    await aiOpsControlAPI.rollback('action/1', 9, signal)
    await aiOpsControlAPI.halt(signal)
    expect(client.post.mock.calls).toEqual([
      ['/admin/ops/ai/actions/action%2F1/rollback', { expected_revision: 9, confirm: true }, { signal }],
      ['/admin/ops/ai/halt', {}, { signal }],
    ])
  })
  it('preserves policy revision for CAS and supports cancelling read requests', async () => {
    const signal = new AbortController().signal
    const policy = { revision: 7, mode: 'disabled' } as AIOpsPolicy
    await aiOpsControlAPI.policy(signal); await aiOpsControlAPI.actions(signal)
    await aiOpsControlAPI.savePolicy(policy, signal)
    expect(client.put).toHaveBeenCalledWith('/admin/ops/ai/policy', policy, { signal })
    expect(client.get.mock.calls).toEqual([
      ['/admin/ops/ai/policy', { signal }], ['/admin/ops/ai/actions', { signal }],
    ])
  })
  it('submits only the explicitly confirmed account/model probe with no prompt', async () => {
    const signal = new AbortController().signal
    await aiOpsControlAPI.probe(3, 'synthetic-model', signal)
    expect(client.post).toHaveBeenCalledWith('/admin/ops/ai/probes', { account_id: 3, model: 'synthetic-model', confirm: true }, { signal })
    expect(client.post).toHaveBeenCalledOnce()
  })
})
